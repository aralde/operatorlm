package server

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/aralde/operatorlm/internal/cassette"
	"github.com/aralde/operatorlm/internal/config"
)

const maxRecordBytes = 64 << 20

type cassetteState struct {
	mu    sync.Mutex
	store *cassette.Store
	cfg   config.CassetteConfig
	ready bool
}

func (s *Server) cassetteMode() string {
	switch m := s.cfg.GetCassette().Mode; m {
	case cassette.ModeRecord, cassette.ModeReplay:
		return m
	}
	return cassette.ModeOff
}

// cassetteStore returns the store loaded for the current settings, reloading when they change.
func (s *Server) cassetteStore() *cassette.Store {
	cc := s.cfg.GetCassette()
	st := s.cassette
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.ready || st.cfg.Dir != cc.Dir || st.cfg.Mode != cc.Mode || len(st.cfg.IgnoreFields) != len(cc.IgnoreFields) {
		if err := st.store.Configure(cc.Dir, cc.IgnoreFields); err != nil {
			log.Printf("cassette: load %s: %v", cc.Dir, err)
		}
		if cc.Mode == cassette.ModeRecord && !st.ready {
			log.Printf("cassette: recording to %s — recorded prompts and responses are stored as-is in plain text (API keys and headers are never stored); review before committing", cc.Dir)
		}
		st.cfg, st.ready = cc, true
	}
	return st.store
}

type recordWriter struct {
	http.ResponseWriter
	status   int
	buf      bytes.Buffer
	overflow bool
}

func (c *recordWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *recordWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.buf.Len()+len(p) > maxRecordBytes {
		c.overflow = true
	} else {
		c.buf.Write(p)
	}
	return c.ResponseWriter.Write(p)
}

func (c *recordWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) recordCassette(r *http.Request, body []byte, model string, stream bool, rw *recordWriter) {
	if rw.status < 200 || rw.status >= 300 || rw.overflow {
		return
	}
	if err := s.cassetteStore().Record(r.URL.Path, model, stream, body, rw.status, rw.Header().Get("Content-Type"), rw.buf.Bytes()); err != nil {
		log.Printf("cassette: record failed: %v", err)
	}
}

func (s *Server) replayCassette(w http.ResponseWriter, r *http.Request, body []byte, model string, stream bool) (int, string) {
	e, miss := s.cassetteStore().Lookup(r.URL.Path, model, stream, body)
	if e == nil {
		log.Printf("cassette: miss: %s", miss)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"message": "cassette replay: " + miss,
			"type":    "cassette_miss",
		}})
		return http.StatusNotFound, miss
	}
	if e.ContentType != "" {
		w.Header().Set("Content-Type", e.ContentType)
	}
	w.WriteHeader(e.Status)
	_, _ = w.Write([]byte(e.Response))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return e.Status, ""
}

type cassetteStatus struct {
	config.CassetteConfig
	Entries int `json:"entries"`
}

func (s *Server) handleCassette(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cc := s.cfg.GetCassette()
		writeJSON(w, http.StatusOK, cassetteStatus{cc, s.cassetteStore().Len()})
	case http.MethodPost:
		var cc config.CassetteConfig
		if err := json.NewDecoder(r.Body).Decode(&cc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch cc.Mode {
		case "", cassette.ModeOff, cassette.ModeRecord, cassette.ModeReplay:
		default:
			http.Error(w, `mode must be "off", "record" or "replay"`, http.StatusBadRequest)
			return
		}
		s.cfg.SetCassette(cc)
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, cassetteStatus{s.cfg.GetCassette(), s.cassetteStore().Len()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

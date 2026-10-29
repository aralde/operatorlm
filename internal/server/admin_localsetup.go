package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/providers"
)

// Local capabilities the one-click setup can turn on.
const (
	capTTS        = "tts"
	capSTT        = "stt"
	capEmbeddings = "embeddings"
	capChat       = "chat"
)

// setupState is the live progress of one capability's setup, keyed by
// capability in GET /admin/localmodels.
type setupState struct {
	Choice     string `json:"choice"`
	Status     string `json:"status"` // "running" | "done" | "error"
	Step       string `json:"step"`   // "engine" | "model" | "starting"
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Error      string `json:"error,omitempty"`
}

type setupTracker struct {
	mu     sync.Mutex
	states map[string]*setupState
}

func (t *setupTracker) get(capability string) (setupState, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.states[capability]
	if !ok {
		return setupState{}, false
	}
	return *st, true
}

func (t *setupTracker) all() map[string]setupState {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]setupState, len(t.states))
	for k, v := range t.states {
		out[k] = *v
	}
	return out
}

func (t *setupTracker) update(capability string, fn func(*setupState)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.states == nil {
		t.states = map[string]*setupState{}
	}
	st, ok := t.states[capability]
	if !ok {
		st = &setupState{}
		t.states[capability] = st
	}
	fn(st)
}

// handleLocalSetup turns a local capability on or off in one call:
//
//	POST /admin/localmodels/setup {"capability":"tts","choice":"es_AR-daniela-high","enable":true}
//
// Enabling downloads whatever is missing (engine binary, then the chosen
// model or voice) in the background and only then switches the capability
// on, so the user never ends up with an enabled engine that cannot start.
func (s *Server) handleLocalSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Capability string `json:"capability"`
		Choice     string `json:"choice"`
		Enable     bool   `json:"enable"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch body.Capability {
	case capTTS, capSTT, capEmbeddings, capChat:
	default:
		http.Error(w, "unknown capability", http.StatusBadRequest)
		return
	}

	if !body.Enable {
		lm := s.cfg.GetLocalModels()
		setCapability(&lm, body.Capability, false)
		if err := s.saveLocalModels(lm); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.setup.update(body.Capability, func(st *setupState) { *st = setupState{} })
		writeJSON(w, http.StatusOK, s.localModelsStatus())
		return
	}

	if cur, ok := s.setup.get(body.Capability); ok && cur.Status == "running" {
		http.Error(w, "setup already running for "+body.Capability, http.StatusConflict)
		return
	}
	plan, err := s.planSetup(r.Context(), body.Capability, body.Choice)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.setup.update(body.Capability, func(st *setupState) {
		*st = setupState{Choice: body.Choice, Status: "running", Step: "engine"}
	})
	go s.runSetup(body.Capability, body.Choice, plan)
	writeJSON(w, http.StatusOK, s.localModelsStatus())
}

// setupPlan is what one capability needs before it can be switched on.
type setupPlan struct {
	tool    string // engine binary id
	toolBin string // configured path of that binary
	model   providers.CatalogEntry
	apply   func(*config.LocalModelsConfig)
}

func (s *Server) planSetup(ctx context.Context, capability, choice string) (setupPlan, error) {
	lm := s.cfg.GetLocalModels()
	switch capability {
	case capTTS:
		voice, err := providers.PiperVoiceByKey(ctx, choice)
		if err != nil {
			return setupPlan{}, err
		}
		return setupPlan{
			tool: "piper", toolBin: lm.PiperPath, model: voice.CatalogEntry(),
			apply: func(c *config.LocalModelsConfig) { c.PiperModel = voice.Key + ".onnx" },
		}, nil
	case capSTT:
		entry, err := catalogEntryWithTag(choice, "stt")
		if err != nil {
			return setupPlan{}, err
		}
		return setupPlan{
			tool: "whisper-server", toolBin: lm.WhisperServerPath, model: entry,
			apply: func(c *config.LocalModelsConfig) { c.WhisperModel = entry.ModelID },
		}, nil
	case capEmbeddings:
		entry, err := catalogEntryWithTag(choice, "embedding")
		if err != nil {
			return setupPlan{}, err
		}
		return setupPlan{
			tool: "llama-server", toolBin: lm.LlamaServerPath, model: entry,
			apply: func(c *config.LocalModelsConfig) { c.EmbeddingsModel = entry.ModelID },
		}, nil
	default:
		entry, err := catalogEntryWithTag(choice, "chat")
		if err != nil {
			return setupPlan{}, err
		}
		return setupPlan{tool: "llama-server", toolBin: lm.LlamaServerPath, model: entry}, nil
	}
}

func catalogEntryWithTag(id, tag string) (providers.CatalogEntry, error) {
	entry, ok := providers.CatalogByID(id)
	if !ok {
		return entry, fmt.Errorf("unknown model %q", id)
	}
	for _, t := range entry.Tags {
		if t == tag {
			return entry, nil
		}
	}
	return entry, fmt.Errorf("%s is not a %s model", entry.Name, tag)
}

func (s *Server) runSetup(capability, choice string, plan setupPlan) {
	fail := func(err error) {
		log.Printf("local setup %s: %v", capability, err)
		s.setup.update(capability, func(st *setupState) {
			st.Status = "error"
			st.Error = err.Error()
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()

	// 1. Engine binary.
	bin := plan.toolBin
	if !fileOrPathExists(bin) {
		dir := filepath.Dir(bin)
		if dir == "" || dir == "." {
			dir = filepath.Dir(defaultToolPath(plan.tool))
			bin = defaultToolPath(plan.tool)
		}
		if _, err := s.dl.StartTool(plan.tool, dir); err != nil {
			fail(err)
			return
		}
		if st := s.waitDownload(ctx, capability, plan.tool); st.Status != "done" {
			fail(fmt.Errorf("installing %s: %s", plan.tool, firstNonEmpty(st.Error, "interrupted")))
			return
		}
	}
	if err := providers.CheckTool(bin); err != nil {
		fail(err)
		return
	}

	// 2. Model or voice.
	s.setup.update(capability, func(st *setupState) { st.Step = "model"; st.Downloaded, st.Total = 0, 0 })
	modelsDir := s.cfg.GetLocalModels().ModelsDir
	if _, err := s.dl.Start(plan.model, modelsDir); err != nil {
		fail(err)
		return
	}
	if st := s.waitDownload(ctx, capability, plan.model.ID); st.Status != "done" {
		fail(fmt.Errorf("downloading %s: %s", plan.model.Name, firstNonEmpty(st.Error, "interrupted")))
		return
	}

	// 3. Switch it on.
	s.setup.update(capability, func(st *setupState) { st.Step = "starting" })
	lm := s.cfg.GetLocalModels()
	if bin != plan.toolBin {
		switch plan.tool {
		case "piper":
			lm.PiperPath = bin
		case "whisper-server":
			lm.WhisperServerPath = bin
		case "llama-server":
			lm.LlamaServerPath = bin
		}
	}
	if plan.apply != nil {
		plan.apply(&lm)
	}
	// Stop our own sidecar first so its port reads as free, then make sure no
	// other program owns it: the engine would fail to bind while requests hit
	// that program instead.
	if capabilityEnabled(lm, capability) {
		off := lm
		setCapability(&off, capability, false)
		if err := s.saveLocalModels(off); err != nil {
			fail(err)
			return
		}
	}
	if port := capabilityPort(&lm, capability); port != nil && !portFree(*port) {
		free, err := findFreePort(*port + 10)
		if err != nil {
			fail(err)
			return
		}
		log.Printf("local setup %s: port %d is in use, using %d", capability, *port, free)
		*port = free
	}
	setCapability(&lm, capability, true)
	if err := s.saveLocalModels(lm); err != nil {
		fail(err)
		return
	}
	s.reg.RefreshLocalEngines()

	// Chat starts llama-server on the first request; the sidecars must be up now.
	if port := capabilityPort(&lm, capability); port != nil && capability != capChat && !waitListening(ctx, *port, 90*time.Second) {
		lm = s.cfg.GetLocalModels()
		setCapability(&lm, capability, false)
		_ = s.saveLocalModels(lm)
		fail(fmt.Errorf("%s did not start on port %d; see operatorlm.log for its output", plan.tool, *port))
		return
	}
	s.setup.update(capability, func(st *setupState) { st.Status = "done"; st.Step = "" })
	log.Printf("local setup %s: %s ready", capability, choice)
}

// waitDownload mirrors a downloader job's progress into the setup state until
// it finishes.
func (s *Server) waitDownload(ctx context.Context, capability, id string) providers.DownloadState {
	for {
		st := s.dl.Status(id)
		s.setup.update(capability, func(ss *setupState) { ss.Downloaded, ss.Total = st.Downloaded, st.Total })
		if st.Status != "downloading" {
			return st
		}
		select {
		case <-ctx.Done():
			return st
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func setCapability(lm *config.LocalModelsConfig, capability string, on bool) {
	switch capability {
	case capTTS:
		lm.PiperEnabled = on
	case capSTT:
		lm.WhisperEnabled = on
	case capEmbeddings:
		lm.EmbeddingsEnabled = on
	case capChat:
		lm.Enabled = on
	}
}

func capabilityEnabled(lm config.LocalModelsConfig, capability string) bool {
	switch capability {
	case capTTS:
		return lm.PiperEnabled
	case capSTT:
		return lm.WhisperEnabled
	case capEmbeddings:
		return lm.EmbeddingsEnabled
	}
	return lm.Enabled
}

func capabilityPort(lm *config.LocalModelsConfig, capability string) *int {
	switch capability {
	case capTTS:
		return &lm.PiperPort
	case capSTT:
		return &lm.WhisperPort
	case capEmbeddings:
		return &lm.EmbeddingsPort
	case capChat:
		return &lm.Port
	}
	return nil
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func findFreePort(from int) (int, error) {
	for p := from; p < from+200 && p < 65536; p++ {
		if portFree(p) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free local port found from %d", from)
}

func waitListening(ctx context.Context, port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	addr := "127.0.0.1:" + strconv.Itoa(port)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func (s *Server) saveLocalModels(lm config.LocalModelsConfig) error {
	s.cfg.SetLocalModels(lm)
	if err := s.cfg.Save(); err != nil {
		return err
	}
	s.reg.Reload()
	return nil
}

func defaultToolPath(tool string) string {
	whisper, piper := config.GetDefaultAudioPaths()
	_, llama := config.GetDefaultPaths()
	switch tool {
	case "piper":
		return piper
	case "whisper-server":
		return whisper
	}
	return llama
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

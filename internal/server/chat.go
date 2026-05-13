package server

import (
	"errors"
	"io"
	"net/http"

	"github.com/aralde/operatorlm/internal/providers"
)

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	s.handleAPI(w, r, providers.KindChat, true)
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	s.handleAPI(w, r, providers.KindImages, false)
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	s.handleAPI(w, r, providers.KindResponses, true)
}

// handleAPI is the shared entry for /v1/chat/completions, /v1/images/generations,
// and /v1/responses. It enforces an upper bound on the request body, parses
// the meta envelope (model + stream flag), and hands off to dispatch.
// honorStream=false forces stream=false even if the client requested it (images).
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request, kind providers.Kind, honorStream bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := s.cfg.MaxRequestBodyBytes()
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	model, stream, err := extractRequestMeta(body)
	if err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !honorStream {
		stream = false
	}
	s.dispatch(w, r, kind, body, model, stream)
}

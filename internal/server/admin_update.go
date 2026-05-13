package server

import (
	"context"
	"net/http"
)

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.upd == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "disabled"})
		return
	}
	writeJSON(w, http.StatusOK, s.upd.Snapshot())
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.upd == nil {
		http.Error(w, "updater disabled", http.StatusServiceUnavailable)
		return
	}
	// Fire and forget: the handler returns immediately; the client polls
	// /admin/update/status to follow progress. Using a fresh context so the
	// update isn't cancelled when the HTTP request returns.
	go s.upd.CheckAndUpdate(context.Background())
	writeJSON(w, http.StatusAccepted, s.upd.Snapshot())
}

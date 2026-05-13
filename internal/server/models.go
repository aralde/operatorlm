package server

import (
	"encoding/json"
	"net/http"
	"time"
)

type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()
	var data []modelInfo

	for _, p := range s.reg.All() {
		for _, m := range p.Models() {
			data = append(data, modelInfo{
				ID:      p.Prefix() + m,
				Object:  "model",
				Created: now,
				OwnedBy: p.Name(),
			})
		}
	}
	for _, a := range s.cfg.AliasSnapshot() {
		if a.Disabled {
			continue
		}
		data = append(data, modelInfo{
			ID:      a.Name,
			Object:  "model",
			Created: now,
			OwnedBy: "alias",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
	})
}

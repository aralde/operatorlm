package server

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/providers"
)

// localModelsStatus is the response shape for GET /admin/localmodels.
type localModelsStatus struct {
	config.LocalModelsConfig
	Models               []providers.LocalModel  `json:"models"`
	Current              string                  `json:"current"`
	Running              bool                    `json:"running"`
	LlamaServerInstalled bool                    `json:"llama_server_installed"`
	LlamaServerDownload  providers.DownloadState `json:"llama_server_download"`

	WhisperInstalled     bool                    `json:"whisper_installed"`
	WhisperDownload      providers.DownloadState `json:"whisper_download"`

	PiperInstalled       bool                    `json:"piper_installed"`
	PiperDownload        providers.DownloadState `json:"piper_download"`
}

func (s *Server) handleLocalModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.localModelsStatus())
	case http.MethodPost:
		var lm config.LocalModelsConfig
		if err := json.NewDecoder(r.Body).Decode(&lm); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.cfg.SetLocalModels(lm)
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.reg.Reload()
		writeJSON(w, http.StatusOK, s.localModelsStatus())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleLocalModelsScan forces a rescan of every local engine's models dir.
func (s *Server) handleLocalModelsScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.reg.RefreshLocalEngines()
	writeJSON(w, http.StatusOK, s.localModelsStatus())
}

// catalogItem augments a catalog entry with install/download status.
type catalogItem struct {
	providers.CatalogEntry
	Installed bool                    `json:"installed"`
	Download  providers.DownloadState `json:"download"`
}

// handleLocalCatalog lists the curated model catalog with install/download state.
func (s *Server) handleLocalCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir := s.reg.LocalModelsDir()
	installed := map[string]bool{}
	if dir != "" {
		if models, err := providers.ScanLocalModels(dir); err == nil {
			for _, m := range models {
				installed[m.ID] = true
			}
		}
	}
	items := make([]catalogItem, 0)
	for _, e := range providers.Catalog() {
		isInstalled := false
		if installed[e.ModelID] {
			isInstalled = true
		} else if dir != "" {
			if _, err := os.Stat(filepath.Join(dir, e.ModelID)); err == nil {
				isInstalled = true
			}
		}

		items = append(items, catalogItem{
			CatalogEntry: e,
			Installed:    isInstalled,
			Download:     s.dl.Status(e.ID),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models_dir": dir,
		"items":      items,
	})
}

// handleLocalCatalogDownload starts (or reports) a catalog model download.
func (s *Server) handleLocalCatalogDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	entry, ok := providers.CatalogByID(body.ID)
	if !ok {
		http.Error(w, "unknown catalog id", http.StatusNotFound)
		return
	}
	dir := s.reg.LocalModelsDir()
	if dir == "" {
		http.Error(w, "no models directory configured: add a llama-server provider or enable local models first", http.StatusBadRequest)
		return
	}
	st, err := s.dl.Start(entry, dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func fileOrPathExists(p string) bool {
	if p == "" {
		return false
	}
	info, err := os.Stat(p)
	if err == nil && !info.IsDir() {
		return true
	}
	_, err = exec.LookPath(p)
	return err == nil
}

func (s *Server) localModelsStatus() localModelsStatus {
	lm := s.cfg.GetLocalModels()
	st := localModelsStatus{
		LocalModelsConfig:    lm,
		Models:               []providers.LocalModel{},
		LlamaServerInstalled: fileOrPathExists(lm.LlamaServerPath),
		LlamaServerDownload:  s.dl.Status("llama-server"),

		WhisperInstalled:     fileOrPathExists(lm.WhisperServerPath),
		WhisperDownload:      s.dl.Status("whisper-server"),

		PiperInstalled:       fileOrPathExists(lm.PiperPath),
		PiperDownload:        s.dl.Status("piper"),
	}

	if eng := s.reg.LocalEngine(); eng != nil {
		st.Models = eng.Models()
		if cur, ok := eng.Current(); ok {
			st.Current = cur
			st.Running = true
		}
	} else if lm.ModelsDir != "" {
		// Disabled: still preview what would be discovered.
		if models, err := providers.ScanLocalModels(lm.ModelsDir); err == nil {
			st.Models = models
		}
	}
	return st
}

// handleLlamaServerDownload initiates the download of the llama-server binary.
func (s *Server) handleLlamaServerDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	lm := s.cfg.GetLocalModels()
	// Extract the directory where llama-server is configured to reside.
	destDir := filepath.Dir(lm.LlamaServerPath)
	if destDir == "" || destDir == "." {
		// If it's a bare command name (e.g. "llama-server"), download to local subfolder.
		_, defaultLlamaServer := config.GetDefaultPaths()
		destDir = filepath.Dir(defaultLlamaServer)
	}

	st, err := s.dl.StartLlamaServer(destDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleWhisperServerDownload initiates the download of the whisper-server binary.
func (s *Server) handleWhisperServerDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	lm := s.cfg.GetLocalModels()
	destDir := filepath.Dir(lm.WhisperServerPath)
	if destDir == "" || destDir == "." {
		defaultWhisper, _ := config.GetDefaultAudioPaths()
		destDir = filepath.Dir(defaultWhisper)
	}

	st, err := s.dl.StartWhisperServer(destDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handlePiperDownload initiates the download of the piper binary.
func (s *Server) handlePiperDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	lm := s.cfg.GetLocalModels()
	destDir := filepath.Dir(lm.PiperPath)
	if destDir == "" || destDir == "." {
		_, defaultPiper := config.GetDefaultAudioPaths()
		destDir = filepath.Dir(defaultPiper)
	}

	st, err := s.dl.StartPiper(destDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

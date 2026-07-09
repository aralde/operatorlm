package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aralde/operatorlm/internal/audit"
	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/providers"
)

type providerPayload struct {
	config.Provider
	APIKey string `json:"api_key,omitempty"`
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		var out []config.Provider
		for _, p := range s.reg.All() {
			out = append(out, config.Provider{
				Name:   p.Name(),
				Type:   p.Type(),
				Prefix: p.Prefix(),
				Models: p.Models(),
			})
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var p providerPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if p.Name == "" || p.Type == "" {
			http.Error(w, "name and type required", http.StatusBadRequest)
			return
		}
		if p.Type == "llama-server" {
			http.Error(w, "the llama-server provider type was replaced by the built-in engine: configure it in the Local models tab", http.StatusBadRequest)
			return
		}
		applyDefaults(&p.Provider)
		if p.BaseURL == "" && p.Type != "chatgpt-codex" && p.Type != "antigravity" {
			http.Error(w, "base_url required", http.StatusBadRequest)
			return
		}
		if p.APIKeyRef == "" {
			p.APIKeyRef = "operatorlm:" + p.Name
		}
		if p.APIKey != "" {
			if err := config.SetSecret(p.APIKeyRef, p.APIKey); err != nil {
				http.Error(w, "store secret: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		s.cfg.UpsertProvider(p.Provider)
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.reg.Reload()
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProviderItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/admin/providers/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	name := parts[0]

	// Sub-resource: keys
	if len(parts) >= 2 && parts[1] == "keys" {
		s.handleProviderKeys(w, r, name, parts[2:])
		return
	}

	switch r.Method {
	case http.MethodDelete:
		for _, p := range s.cfg.Snapshot() {
			if p.Name == name {
				_ = config.DeleteSecret(p.APIKeyRef)
				for _, k := range p.Keys {
					_ = config.DeleteSecret(k.APIKeyRef)
				}
				break
			}
		}
		if !s.cfg.DeleteProvider(name) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.reg.Reload()
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	case http.MethodPatch:
		var p struct {
			Disabled bool `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !s.cfg.SetProviderDisabled(name, p.Disabled) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.reg.Reload()
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type keyPayload struct {
	Name   string `json:"name"`
	APIKey string `json:"api_key"`
}

func (s *Server) handleProviderKeys(w http.ResponseWriter, r *http.Request, providerName string, sub []string) {
	prov, ok := s.cfg.FindProvider(providerName)
	if !ok {
		http.Error(w, "provider not found", http.StatusNotFound)
		return
	}

	switch {
	case len(sub) == 0 || sub[0] == "":
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, prov.Keys)
		case http.MethodPost:
			var p keyPayload
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if p.Name == "" || p.Name == "default" {
				http.Error(w, "key name required (and cannot be 'default')", http.StatusBadRequest)
				return
			}
			if p.APIKey == "" {
				http.Error(w, "api_key required", http.StatusBadRequest)
				return
			}
			ref := "operatorlm:" + providerName + "/" + p.Name
			if err := config.SetSecret(ref, p.APIKey); err != nil {
				http.Error(w, "store secret: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if !s.cfg.UpsertProviderKey(providerName, config.KeySlot{Name: p.Name, APIKeyRef: ref}) {
				http.Error(w, "provider not found", http.StatusNotFound)
				return
			}
			if err := s.cfg.Save(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			s.reg.Reload()
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case len(sub) == 1 && sub[0] != "":
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		slot, ok := s.cfg.DeleteProviderKey(providerName, sub[0])
		if !ok {
			http.Error(w, "key not found", http.StatusNotFound)
			return
		}
		_ = config.DeleteSecret(slot.APIKeyRef)
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.reg.Reload()
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// --- Aliases ---

func (s *Server) handleAliases(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.cfg.AliasSnapshot())
	case http.MethodPost:
		var a config.Alias
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if a.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if a.Strategy == "" || a.Strategy == "fallback" {
			a.Strategy = "order"
		}
		if a.Strategy != "order" {
			http.Error(w, "only strategy 'order' is supported in v1", http.StatusBadRequest)
			return
		}
		// Validate targets reference existing providers/keys. "local",
		// "whisper-local" and "piper-local" are built-in (local engine).
		builtin := map[string]bool{"local": true, "whisper-local": true, "piper-local": true}
		for i, t := range a.Targets {
			if builtin[t.Provider] {
				if t.UpstreamModel == "" {
					http.Error(w, "target #"+itoa(i)+": upstream_model required", http.StatusBadRequest)
					return
				}
				continue
			}
			p, ok := s.cfg.FindProvider(t.Provider)
			if !ok {
				http.Error(w, "target #"+itoa(i)+": provider not found", http.StatusBadRequest)
				return
			}
			if p.KeyRef(t.Key) == "" && p.Type != "llamacpp" {
				http.Error(w, "target #"+itoa(i)+": key not found", http.StatusBadRequest)
				return
			}
			if t.UpstreamModel == "" {
				http.Error(w, "target #"+itoa(i)+": upstream_model required", http.StatusBadRequest)
				return
			}
		}
		s.cfg.UpsertAlias(a)
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAliasItem(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/admin/aliases/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if !s.cfg.DeleteAlias(name) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	case http.MethodPatch:
		var p struct {
			Disabled bool `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !s.cfg.SetAliasDisabled(name, p.Disabled) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// --- Reliability ---

func (s *Server) handleReliability(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.cfg.GetReliability())
	case http.MethodPost:
		var rc config.ReliabilityConfig
		if err := json.NewDecoder(r.Body).Decode(&rc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Validate fallback alias exists when set.
		if rc.DefaultFallbackAlias != "" {
			if _, ok := s.cfg.FindAlias(rc.DefaultFallbackAlias); !ok {
				http.Error(w, "default_fallback_alias not found: "+rc.DefaultFallbackAlias, http.StatusBadRequest)
				return
			}
		}
		s.cfg.SetReliability(rc)
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.rt.Reconfigure(s.cfg.GetReliability())
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Health (breaker state) ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"targets": s.rt.Breaker().Snapshot(),
		"recent":  s.getRecentRequests(),
	})
}

func (s *Server) handleHealthClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var p struct{ ID string `json:"id"` }
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if p.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	s.rt.Breaker().Clear(p.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

// --- Audit ---

type auditStatus struct {
	config.AuditConfig
	Written  int64  `json:"written"`
	Dropped  int64  `json:"dropped"`
	Active   bool   `json:"active"`
	Effective string `json:"effective_path"`
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		written, dropped, active := s.audit.Stats()
		opts := s.audit.Options()
		ac := s.cfg.GetAudit()
		writeJSON(w, http.StatusOK, auditStatus{
			AuditConfig: ac,
			Written:     written,
			Dropped:     dropped,
			Active:      active,
			Effective:   opts.Path,
		})
	case http.MethodPost:
		var ac config.AuditConfig
		if err := json.NewDecoder(r.Body).Decode(&ac); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if ac.Enabled && ac.Path == "" {
			home, _ := os.UserHomeDir()
			ac.Path = filepath.Join(home, ".operatorlm", "audit.log")
		}
		// Redaction is mandatory when audit is enabled (the logger enforces
		// it anyway — keep the persisted config in sync so the UI reflects
		// reality).
		if ac.Enabled {
			ac.Redact = true
		}
		s.cfg.SetAudit(ac)
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		opts := audit.Options{
			Path:                 ac.Path,
			BufferSize:           ac.BufferSize,
			MaxRequestBodyBytes:  ac.MaxRequestBodyBytes,
			MaxResponseBodyBytes: ac.MaxResponseBodyBytes,
			Redact:               ac.Redact,
		}
		if err := s.audit.Reconfigure(ac.Enabled, opts); err != nil {
			http.Error(w, "audit reconfigure: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Local API key (client -> proxy auth on /v1/*) ---

const localAuthKeyRef = "operatorlm:local-api-key"

type localAuthPayload struct {
	Enabled  bool   `json:"enabled"`
	APIKey   string `json:"api_key,omitempty"`
	ClearKey bool   `json:"clear_key,omitempty"`
}

func (s *Server) handleLocalAuth(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		la := s.cfg.GetLocalAuth()
		keySet := false
		if la.KeyRef != "" {
			if v, err := config.GetSecret(la.KeyRef); err == nil && v != "" {
				keySet = true
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": la.Enabled,
			"key_set": keySet,
		})
	case http.MethodPost:
		// CSRF defense: require a custom header that browsers will only send
		// after a successful CORS preflight. Combined with no-CORS-on-/admin
		// and Host-header validation, this blocks cross-origin form POSTs and
		// fetch requests from rebound DNS targets.
		if r.Header.Get("X-OperatorLM-Admin") != "1" {
			http.Error(w, "forbidden: missing X-OperatorLM-Admin header", http.StatusForbidden)
			return
		}
		var p localAuthPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		la := s.cfg.GetLocalAuth()

		if p.ClearKey {
			if la.KeyRef != "" {
				_ = config.DeleteSecret(la.KeyRef)
			}
			la.KeyRef = ""
		}

		if p.APIKey != "" {
			if err := config.SetSecret(localAuthKeyRef, p.APIKey); err != nil {
				http.Error(w, "store secret: "+err.Error(), http.StatusInternalServerError)
				return
			}
			la.KeyRef = localAuthKeyRef
		}

		if p.Enabled {
			keyAvailable := false
			if la.KeyRef != "" {
				if v, err := config.GetSecret(la.KeyRef); err == nil && v != "" {
					keyAvailable = true
				}
			}
			if !keyAvailable {
				http.Error(w, "cannot enable: no API key stored", http.StatusBadRequest)
				return
			}
		}
		la.Enabled = p.Enabled

		s.cfg.SetLocalAuth(la)
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

var providerDefaults = map[string]config.Provider{
	"openai":     {BaseURL: "https://api.openai.com/v1", Prefix: "openai/"},
	"openrouter": {BaseURL: "https://openrouter.ai/api/v1", Prefix: "openrouter/"},
	"groq":       {BaseURL: "https://api.groq.com/openai/v1", Prefix: "groq/"},
	"gemini":     {BaseURL: "https://generativelanguage.googleapis.com/v1beta", Prefix: "gemini/"},
	"nvidia-nim":   {BaseURL: "https://integrate.api.nvidia.com/v1", Prefix: "nvidia/"},
	"opencode-zen": {BaseURL: "https://opencode.ai/zen/v1", Prefix: "zen/"},
	"mistral":      {BaseURL: "https://api.mistral.ai/v1", Prefix: "mistral/"},
	"bedrock":      {BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com/openai/v1", Prefix: "bedrock/"},
	"azure-openai": {Prefix: "azure/"},
	"custom":       {Prefix: "custom/"},
	// chatgpt-codex uses a hardcoded base URL inside the provider; no API key.
	"chatgpt-codex": {Prefix: "chatgpt/"},
	"antigravity":   {Prefix: "antigravity/"},
}

func applyDefaults(p *config.Provider) {
	d, ok := providerDefaults[p.Type]
	if !ok {
		return
	}
	if p.BaseURL == "" {
		p.BaseURL = d.BaseURL
	}
	if p.Prefix == "" {
		p.Prefix = d.Prefix
	}
}

type probeReq struct {
	Type       string `json:"type"`
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
	Provider   string `json:"provider"`
	ApiVersion string `json:"api_version,omitempty"`
	ModelsDir  string `json:"models_dir,omitempty"`
}

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var p probeReq
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tmp := config.Provider{Type: p.Type, BaseURL: p.BaseURL, ApiVersion: p.ApiVersion, ModelsDir: p.ModelsDir}
	apiKey := p.APIKey
	if p.Provider != "" {
		existing, ok := s.cfg.FindProvider(p.Provider)
		if !ok {
			http.Error(w, "provider not found", http.StatusNotFound)
			return
		}
		if tmp.Type == "" {
			tmp.Type = existing.Type
		}
		if tmp.BaseURL == "" {
			tmp.BaseURL = existing.BaseURL
		}
		if tmp.ApiVersion == "" {
			tmp.ApiVersion = existing.ApiVersion
		}
		if tmp.ModelsDir == "" {
			tmp.ModelsDir = existing.ModelsDir
		}
		if apiKey == "" {
			ref := existing.APIKeyRef
			if ref == "" {
				ref = "operatorlm:" + existing.Name
			}
			secret, err := config.GetSecret(ref)
			if err != nil || secret == "" {
				if tmp.Type != "custom" {
					http.Error(w, "no stored API key for provider", http.StatusNotFound)
					return
				}
			} else {
				apiKey = secret
			}
		}
	}
	applyDefaults(&tmp)
	if tmp.BaseURL == "" && tmp.Type != "antigravity" {
		http.Error(w, "base_url required", http.StatusBadRequest)
		return
	}
	// API key is optional for custom/local providers (Ollama, LM Studio, etc.) and antigravity.
	if apiKey == "" && tmp.Type != "custom" && tmp.Type != "antigravity" {
		http.Error(w, "api_key required", http.StatusBadRequest)
		return
	}
	models, err := providers.ProbeModels(r.Context(), tmp.Type, tmp.BaseURL, apiKey, providers.ProbeOptions{
		ApiVersion: tmp.ApiVersion,
		ModelsDir:  tmp.ModelsDir,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) handleAntigravityProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projects := providers.GetDiscoveredProjects()
	if projects == nil {
		projects = []providers.DiscoveredProject{}
	}
	writeJSON(w, http.StatusOK, projects)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

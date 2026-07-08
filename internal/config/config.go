package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/BurntSushi/toml"
)

type KeySlot struct {
	Name      string `toml:"name"         json:"name"`
	APIKeyRef string `toml:"api_key_ref"  json:"api_key_ref"`
}

type Provider struct {
	Name      string    `toml:"name"             json:"name"`
	Type      string    `toml:"type"             json:"type"`
	BaseURL   string    `toml:"base_url"         json:"base_url"`
	Prefix    string    `toml:"prefix"           json:"prefix"`
	APIKeyRef string    `toml:"api_key_ref"      json:"api_key_ref"`
	// ApiVersion is the api-version query string used by some upstreams
	// (currently only azure-openai, where it is required).
	ApiVersion string    `toml:"api_version,omitempty" json:"api_version,omitempty"`
	ProjectID  string    `toml:"project_id,omitempty"  json:"project_id,omitempty"`
	Models     []string  `toml:"models,omitempty" json:"models,omitempty"`
	Keys       []KeySlot `toml:"keys,omitempty"   json:"keys,omitempty"`
	Disabled   bool      `toml:"disabled,omitempty" json:"disabled,omitempty"`

	// llama-server local models fields
	ModelsDir       string   `toml:"models_dir,omitempty"      json:"models_dir,omitempty"`
	LlamaServerPath string   `toml:"llama_server_path,omitempty" json:"llama_server_path,omitempty"`
	Port            int      `toml:"port,omitempty"             json:"port,omitempty"`
	ContextSize     int      `toml:"context_size,omitempty"     json:"context_size,omitempty"`
	NGPULayers      int      `toml:"ngpu_layers,omitempty"      json:"ngpu_layers,omitempty"`
	ExtraArgs       []string `toml:"extra_args,omitempty"       json:"extra_args,omitempty"`
}

// KeyRef returns the api_key_ref to use for a given key name.
// If keyName is "" or "default", returns the provider's default APIKeyRef.
// If keyName matches a slot, returns that slot's ref. Otherwise returns "".
func (p Provider) KeyRef(keyName string) string {
	if keyName == "" || keyName == "default" {
		return p.APIKeyRef
	}
	for _, k := range p.Keys {
		if k.Name == keyName {
			return k.APIKeyRef
		}
	}
	return ""
}

type AliasTarget struct {
	Provider      string `toml:"provider"          json:"provider"`
	Key           string `toml:"key,omitempty"     json:"key,omitempty"`
	UpstreamModel string `toml:"upstream_model"    json:"upstream_model"`
	Order         int    `toml:"order"             json:"order"`
	RPM           int    `toml:"rpm,omitempty"     json:"rpm,omitempty"`
	// MaxOutputTokens, when > 0, clamps the request body's max_tokens /
	// max_completion_tokens / max_output_tokens to this value before sending
	// upstream. Use it to respect per-model output caps (e.g. Groq's
	// Llama-4-Scout: 8192) so clients that send larger values don't get 400s.
	MaxOutputTokens int `toml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
}

type Alias struct {
	Name     string        `toml:"name"     json:"name"`
	Strategy string        `toml:"strategy" json:"strategy"`
	Targets  []AliasTarget `toml:"targets"  json:"targets"`
	Disabled bool          `toml:"disabled,omitempty" json:"disabled,omitempty"`
}

type ReliabilityConfig struct {
	// Retry
	MaxRetries    int `toml:"max_retries"     json:"max_retries"`     // per target. default 2
	BackoffBaseMs int `toml:"backoff_base_ms" json:"backoff_base_ms"` // default 500
	BackoffCapMs  int `toml:"backoff_cap_ms"  json:"backoff_cap_ms"`  // default 10000

	// Circuit breaker
	OpenAfterFailures   int `toml:"open_after_failures"     json:"open_after_failures"`     // consecutive failures to trip. default 3
	CooldownRateLimitMs int `toml:"cooldown_rate_limit_ms"  json:"cooldown_rate_limit_ms"`  // 429 cooldown. default 15000
	CooldownServerMs    int `toml:"cooldown_server_ms"      json:"cooldown_server_ms"`      // 5xx cooldown. default 60000
	CooldownNetworkMs   int `toml:"cooldown_network_ms"     json:"cooldown_network_ms"`     // network/timeout cooldown. default 90000

	// Timeouts (all in milliseconds)
	ConnectTimeoutMs    int `toml:"connect_timeout_ms"      json:"connect_timeout_ms"`      // default 5000
	PerAttemptTimeoutMs int `toml:"per_attempt_timeout_ms"  json:"per_attempt_timeout_ms"`  // default 60000
	StreamIdleTimeoutMs int `toml:"stream_idle_timeout_ms"  json:"stream_idle_timeout_ms"`  // default 30000
	TotalTimeoutMs      int `toml:"total_timeout_ms"        json:"total_timeout_ms"`        // default 180000

	// Fallback
	DefaultFallbackAlias string `toml:"default_fallback_alias,omitempty" json:"default_fallback_alias,omitempty"`
}

// WithDefaults returns a copy with zero values replaced by sensible defaults.
func (r ReliabilityConfig) WithDefaults() ReliabilityConfig {
	if r.MaxRetries < 0 {
		r.MaxRetries = 0
	}
	if r.MaxRetries == 0 && r.BackoffBaseMs == 0 && r.BackoffCapMs == 0 && r.OpenAfterFailures == 0 {
		// Untouched: load full defaults.
		r.MaxRetries = 2
		r.BackoffBaseMs = 500
		r.BackoffCapMs = 10000
		r.OpenAfterFailures = 3
		r.CooldownRateLimitMs = 15000
		r.CooldownServerMs = 60000
		r.CooldownNetworkMs = 90000
		r.ConnectTimeoutMs = 5000
		r.PerAttemptTimeoutMs = 60000
		r.StreamIdleTimeoutMs = 30000
		r.TotalTimeoutMs = 180000
		return r
	}
	if r.BackoffBaseMs <= 0 {
		r.BackoffBaseMs = 500
	}
	if r.BackoffCapMs <= 0 {
		r.BackoffCapMs = 10000
	}
	if r.OpenAfterFailures <= 0 {
		r.OpenAfterFailures = 3
	}
	if r.CooldownRateLimitMs <= 0 {
		r.CooldownRateLimitMs = 15000
	}
	if r.CooldownServerMs <= 0 {
		r.CooldownServerMs = 60000
	}
	if r.CooldownNetworkMs <= 0 {
		r.CooldownNetworkMs = 90000
	}
	if r.ConnectTimeoutMs <= 0 {
		r.ConnectTimeoutMs = 5000
	}
	if r.PerAttemptTimeoutMs <= 0 {
		r.PerAttemptTimeoutMs = 60000
	}
	if r.StreamIdleTimeoutMs <= 0 {
		r.StreamIdleTimeoutMs = 30000
	}
	if r.TotalTimeoutMs <= 0 {
		r.TotalTimeoutMs = 180000
	}
	return r
}

type AuditConfig struct {
	Enabled              bool   `toml:"enabled"                  json:"enabled"`
	Path                 string `toml:"path,omitempty"           json:"path,omitempty"`
	BufferSize           int    `toml:"buffer_size,omitempty"    json:"buffer_size,omitempty"`
	MaxRequestBodyBytes  int    `toml:"max_request_body_bytes,omitempty"  json:"max_request_body_bytes,omitempty"`
	MaxResponseBodyBytes int    `toml:"max_response_body_bytes,omitempty" json:"max_response_body_bytes,omitempty"`
	Redact               bool   `toml:"redact"                   json:"redact"`
}

type LocalAuthConfig struct {
	Enabled bool   `toml:"enabled"          json:"enabled"`
	KeyRef  string `toml:"key_ref,omitempty" json:"key_ref,omitempty"`
}

// LocalModelsConfig drives the built-in local inference engine: operatorlm
// scans ModelsDir for *.gguf files and, on demand, spawns LlamaServerPath
// (llama.cpp's llama-server) loading the requested model, then proxies to it.
// This makes the proxy self-contained ("Ollama-like") without depending on a
// separate daemon.
type LocalModelsConfig struct {
	Enabled bool `toml:"enabled" json:"enabled"`
	// ModelsDir is scanned recursively for *.gguf model files.
	ModelsDir string `toml:"models_dir,omitempty" json:"models_dir,omitempty"`
	// LlamaServerPath is the path to the llama-server executable. If empty,
	// "llama-server" is looked up on PATH.
	LlamaServerPath string `toml:"llama_server_path,omitempty" json:"llama_server_path,omitempty"`
	// Prefix routes models to this engine (default "local/").
	Prefix string `toml:"prefix,omitempty" json:"prefix,omitempty"`
	// Port is the loopback port the spawned llama-server listens on.
	Port int `toml:"port,omitempty" json:"port,omitempty"`
	// ContextSize is the -c / --ctx-size passed to llama-server.
	ContextSize int `toml:"context_size,omitempty" json:"context_size,omitempty"`
	// NGPULayers is -ngl: number of layers offloaded to GPU (0 = CPU only).
	NGPULayers int `toml:"ngpu_layers,omitempty" json:"ngpu_layers,omitempty"`
	// ExtraArgs are appended verbatim to the llama-server command line.
	ExtraArgs []string `toml:"extra_args,omitempty" json:"extra_args,omitempty"`

	// Local Audio STT (whisper.cpp)
	WhisperEnabled    bool   `toml:"whisper_enabled" json:"whisper_enabled"`
	WhisperServerPath string `toml:"whisper_server_path,omitempty" json:"whisper_server_path,omitempty"`
	WhisperPort       int    `toml:"whisper_port,omitempty" json:"whisper_port,omitempty"`
	WhisperModel      string `toml:"whisper_model,omitempty" json:"whisper_model,omitempty"`

	// Local Audio TTS (Piper)
	PiperEnabled bool   `toml:"piper_enabled" json:"piper_enabled"`
	PiperPath    string `toml:"piper_path,omitempty" json:"piper_path,omitempty"`
	PiperPort    int    `toml:"piper_port,omitempty" json:"piper_port,omitempty"`
	PiperModel   string `toml:"piper_model,omitempty" json:"piper_model,omitempty"`
}

// GetDefaultPaths returns default models directory and llama-server path
// relative to the current executable.
func GetDefaultPaths() (modelsDir string, llamaServerPath string) {
	exePath, err := os.Executable()
	var baseDir string
	if err == nil {
		baseDir = filepath.Dir(exePath)
	} else {
		baseDir = "."
	}
	absBaseDir, err := filepath.Abs(baseDir)
	if err == nil {
		baseDir = absBaseDir
	}
	modelsDir = filepath.Join(baseDir, "localModels")
	if runtime.GOOS == "windows" {
		llamaServerPath = filepath.Join(baseDir, "llama-server", "llama-server.exe")
	} else {
		llamaServerPath = filepath.Join(baseDir, "llama-server", "llama-server")
	}
	return modelsDir, llamaServerPath
}

// GetDefaultAudioPaths returns default whisper-server and piper paths
// relative to the current executable.
func GetDefaultAudioPaths() (whisperPath string, piperPath string) {
	exePath, err := os.Executable()
	var baseDir string
	if err == nil {
		baseDir = filepath.Dir(exePath)
	} else {
		baseDir = "."
	}
	absBaseDir, err := filepath.Abs(baseDir)
	if err == nil {
		baseDir = absBaseDir
	}
	if runtime.GOOS == "windows" {
		whisperPath = filepath.Join(baseDir, "whisper-server", "whisper-server.exe")
		piperPath = filepath.Join(baseDir, "piper", "piper.exe")
	} else {
		whisperPath = filepath.Join(baseDir, "whisper-server", "whisper-server")
		piperPath = filepath.Join(baseDir, "piper", "piper")
	}
	return whisperPath, piperPath
}

// WithDefaults fills zero values with sensible defaults.
func (l LocalModelsConfig) WithDefaults() LocalModelsConfig {
	if l.Prefix == "" {
		l.Prefix = "local/"
	}
	if l.Port == 0 {
		l.Port = 8081
	}
	if l.ContextSize == 0 {
		l.ContextSize = 4096
	}

	defaultModelsDir, defaultLlamaServer := GetDefaultPaths()
	if l.ModelsDir == "" {
		l.ModelsDir = defaultModelsDir
	}
	if l.LlamaServerPath == "" {
		if _, err := exec.LookPath("llama-server"); err == nil {
			l.LlamaServerPath = "llama-server"
		} else {
			l.LlamaServerPath = defaultLlamaServer
		}
	}

	// Audio defaults
	if l.WhisperPort == 0 {
		l.WhisperPort = 8082
	}
	defaultWhisper, defaultPiper := GetDefaultAudioPaths()
	if l.WhisperServerPath == "" {
		if _, err := exec.LookPath("whisper-server"); err == nil {
			l.WhisperServerPath = "whisper-server"
		} else {
			l.WhisperServerPath = defaultWhisper
		}
	}
	if l.PiperPort == 0 {
		l.PiperPort = 8083
	}
	if l.PiperPath == "" {
		if _, err := exec.LookPath("piper"); err == nil {
			l.PiperPath = "piper"
		} else {
			l.PiperPath = defaultPiper
		}
	}
	return l
}

type Config struct {
	Port            int               `toml:"port"`
	CORSOrigins     []string          `toml:"cors_origins"`
	MaxRequestBytes int64             `toml:"max_request_bytes,omitempty"`
	Providers       []Provider        `toml:"providers"`
	Aliases         []Alias           `toml:"aliases,omitempty"`
	Audit           AuditConfig       `toml:"audit,omitempty"`
	Reliability     ReliabilityConfig `toml:"reliability,omitempty"`
	LocalAuth       LocalAuthConfig   `toml:"local_auth,omitempty"`
	LocalModels     LocalModelsConfig `toml:"local_models,omitempty"`

	mu   sync.RWMutex `toml:"-"`
	path string       `toml:"-"`
}

func defaultConfig(path string) *Config {
	return &Config{
		Port:        11434,
		CORSOrigins: []string{"*"},
		Providers: []Provider{
			{Name: "openai", Type: "openai", BaseURL: "https://api.openai.com/v1", Prefix: "openai/", APIKeyRef: "operatorlm:openai"},
			{Name: "openrouter", Type: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Prefix: "openrouter/", APIKeyRef: "operatorlm:openrouter"},
			{Name: "groq", Type: "groq", BaseURL: "https://api.groq.com/openai/v1", Prefix: "groq/", APIKeyRef: "operatorlm:groq"},
			{Name: "gemini", Type: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta", Prefix: "gemini/", APIKeyRef: "operatorlm:gemini"},
		},
		Aliases: []Alias{
			{
				Name:     "whisper-1",
				Strategy: "fallback",
				Targets: []AliasTarget{
					{Provider: "whisper-local", UpstreamModel: "whisper-1", Order: 1},
				},
			},
			{
				Name:     "tts-1",
				Strategy: "fallback",
				Targets: []AliasTarget{
					{Provider: "piper-local", UpstreamModel: "tts-1", Order: 1},
				},
			},
		},
		path: path,
	}
}

func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".operatorlm"), nil
}

func Load() (*Config, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.toml")

	cfg := defaultConfig(path)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.path = path
	return cfg, nil
}

func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	tmp := c.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := toml.NewEncoder(f)
	if err := enc.Encode(c); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *Config) ListenAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", c.Port)
}

// MaxRequestBodyBytes returns the configured cap for inbound request bodies.
// Default 64 MiB if unset, leaving headroom over typical 50 MB payloads.
func (c *Config) MaxRequestBodyBytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.MaxRequestBytes <= 0 {
		return 64 << 20
	}
	return c.MaxRequestBytes
}

func (c *Config) Snapshot() []Provider {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Provider, len(c.Providers))
	copy(out, c.Providers)
	return out
}

func (c *Config) GetReliability() ReliabilityConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Reliability.WithDefaults()
}

func (c *Config) SetReliability(r ReliabilityConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Reliability = r
}

func (c *Config) GetAudit() AuditConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Audit
}

func (c *Config) SetAudit(a AuditConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Audit = a
}

func (c *Config) GetLocalAuth() LocalAuthConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.LocalAuth
}

func (c *Config) SetLocalAuth(la LocalAuthConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.LocalAuth = la
}

func (c *Config) GetLocalModels() LocalModelsConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.LocalModels.WithDefaults()
}

func (c *Config) SetLocalModels(lm LocalModelsConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.LocalModels = lm
}

func (c *Config) AliasSnapshot() []Alias {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Alias, len(c.Aliases))
	copy(out, c.Aliases)
	return out
}

func (c *Config) FindProvider(name string) (Provider, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range c.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

func (c *Config) FindAlias(name string) (Alias, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, a := range c.Aliases {
		if a.Name == name {
			return a, true
		}
	}
	return Alias{}, false
}

func (c *Config) UpsertProvider(p Provider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, existing := range c.Providers {
		if existing.Name == p.Name {
			// preserve existing keys if not provided
			if p.Keys == nil {
				p.Keys = existing.Keys
			}
			// preserve disabled state across edits from forms that don't set it
			if !p.Disabled {
				p.Disabled = existing.Disabled
			}
			c.Providers[i] = p
			return
		}
	}
	c.Providers = append(c.Providers, p)
}

func (c *Config) DeleteProvider(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, p := range c.Providers {
		if p.Name == name {
			c.Providers = append(c.Providers[:i], c.Providers[i+1:]...)
			return true
		}
	}
	return false
}

func (c *Config) UpsertProviderKey(providerName string, slot KeySlot) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, p := range c.Providers {
		if p.Name != providerName {
			continue
		}
		for j, k := range p.Keys {
			if k.Name == slot.Name {
				c.Providers[i].Keys[j] = slot
				return true
			}
		}
		c.Providers[i].Keys = append(c.Providers[i].Keys, slot)
		return true
	}
	return false
}

func (c *Config) DeleteProviderKey(providerName, keyName string) (KeySlot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, p := range c.Providers {
		if p.Name != providerName {
			continue
		}
		for j, k := range p.Keys {
			if k.Name == keyName {
				c.Providers[i].Keys = append(c.Providers[i].Keys[:j], c.Providers[i].Keys[j+1:]...)
				return k, true
			}
		}
	}
	return KeySlot{}, false
}

func (c *Config) UpsertAlias(a Alias) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, existing := range c.Aliases {
		if existing.Name == a.Name {
			c.Aliases[i] = a
			return
		}
	}
	c.Aliases = append(c.Aliases, a)
}

func (c *Config) SetProviderDisabled(name string, disabled bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, p := range c.Providers {
		if p.Name == name {
			c.Providers[i].Disabled = disabled
			return true
		}
	}
	return false
}

func (c *Config) SetAliasDisabled(name string, disabled bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, a := range c.Aliases {
		if a.Name == name {
			c.Aliases[i].Disabled = disabled
			return true
		}
	}
	return false
}

func (c *Config) DeleteAlias(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, a := range c.Aliases {
		if a.Name == name {
			c.Aliases = append(c.Aliases[:i], c.Aliases[i+1:]...)
			return true
		}
	}
	return false
}

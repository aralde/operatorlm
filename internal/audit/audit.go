// Package audit provides non-blocking audit logging for proxy requests.
//
// Design: a single writer goroutine drains a buffered channel and appends
// NDJSON lines to disk. Producers (HTTP handlers) submit records via Write,
// which is wait-free: if the channel is full, the record is dropped and a
// counter increments. This guarantees disk I/O can never stall the proxy.
package audit

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type UpstreamInfo struct {
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Status  int               `json:"status,omitempty"`
}

type Record struct {
	ID              string            `json:"id"`
	Timestamp       time.Time         `json:"ts"`
	Client          string            `json:"client"`
	UserAgent       string            `json:"ua,omitempty"`
	Method          string            `json:"method"`
	Path            string            `json:"path"`
	Model           string            `json:"model,omitempty"`
	Stream          bool              `json:"stream,omitempty"`
	RequestHeaders  map[string]string `json:"req_headers,omitempty"`
	RequestBody     json.RawMessage   `json:"req_body,omitempty"`
	Attempt         string            `json:"attempt,omitempty"`
	Upstream        *UpstreamInfo     `json:"upstream,omitempty"`
	Status          int               `json:"status"`
	ResponseHeaders map[string]string `json:"resp_headers,omitempty"`
	ResponseBody    json.RawMessage   `json:"resp_body,omitempty"`
	DurationMs      int64             `json:"duration_ms"`
	Error           string            `json:"error,omitempty"`
}

// Options control logger behaviour. Sensible defaults are applied for zero values.
type Options struct {
	Path                 string
	BufferSize           int   // default 1024
	MaxRequestBodyBytes  int   // default 64 KiB
	MaxResponseBodyBytes int   // default 1 MiB
	Redact               bool  // default true
}

func (o *Options) defaults() {
	if o.BufferSize <= 0 {
		o.BufferSize = 1024
	}
	if o.MaxRequestBodyBytes <= 0 {
		o.MaxRequestBodyBytes = 64 * 1024
	}
	if o.MaxResponseBodyBytes <= 0 {
		o.MaxResponseBodyBytes = 1024 * 1024
	}
}

type Logger struct {
	mu       sync.RWMutex
	ch       chan Record
	file     *os.File
	enabled  bool
	opts     Options
	dropped  atomic.Int64
	written  atomic.Int64
	done     chan struct{}
}

// New creates a new logger. enabled=false returns a no-op logger that
// can later be activated via Reconfigure.
func New() *Logger {
	return &Logger{}
}

// Reconfigure atomically swaps the logger's destination/state.
// Closing previous channel drains in-flight records first.
//
// When enabled, redaction is forced on regardless of opts.Redact: a
// public release should never write Authorization headers or API keys
// to disk in cleartext, even if a user misconfigures the option.
func (l *Logger) Reconfigure(enabled bool, opts Options) error {
	opts.defaults()
	if enabled {
		opts.Redact = true
	}

	l.mu.Lock()
	prevCh := l.ch
	prevFile := l.file
	prevDone := l.done
	l.mu.Unlock()

	// Drain previous writer if any.
	if prevCh != nil {
		close(prevCh)
		<-prevDone
		_ = prevFile.Close()
	}

	if !enabled || opts.Path == "" {
		l.mu.Lock()
		l.enabled = false
		l.ch = nil
		l.file = nil
		l.done = nil
		l.opts = opts
		l.mu.Unlock()
		return nil
	}

	f, err := os.OpenFile(opts.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	ch := make(chan Record, opts.BufferSize)
	done := make(chan struct{})

	l.mu.Lock()
	l.enabled = true
	l.opts = opts
	l.file = f
	l.ch = ch
	l.done = done
	l.mu.Unlock()

	go l.run(ch, f, done)
	return nil
}

func (l *Logger) Close() error {
	return l.Reconfigure(false, Options{})
}

// Write submits a record. Wait-free: drops on full buffer.
func (l *Logger) Write(r Record) {
	l.mu.RLock()
	enabled := l.enabled
	ch := l.ch
	l.mu.RUnlock()
	if !enabled || ch == nil {
		return
	}
	select {
	case ch <- r:
	default:
		l.dropped.Add(1)
	}
}

func (l *Logger) Stats() (written, dropped int64, enabled bool) {
	l.mu.RLock()
	enabled = l.enabled
	l.mu.RUnlock()
	return l.written.Load(), l.dropped.Load(), enabled
}

func (l *Logger) Options() Options {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.opts
}

// MaxRequestBody / MaxResponseBody expose the truncation caps for callers.
func (l *Logger) MaxRequestBody() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.opts.MaxRequestBodyBytes <= 0 {
		return 64 * 1024
	}
	return l.opts.MaxRequestBodyBytes
}

func (l *Logger) MaxResponseBody() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.opts.MaxResponseBodyBytes <= 0 {
		return 1024 * 1024
	}
	return l.opts.MaxResponseBodyBytes
}

func (l *Logger) Enabled() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.enabled
}

func (l *Logger) Redact() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.opts.Redact
}

func (l *Logger) run(ch chan Record, f *os.File, done chan struct{}) {
	defer close(done)
	enc := json.NewEncoder(f)
	for r := range ch {
		if err := enc.Encode(&r); err != nil {
			log.Printf("audit: write error: %v", err)
			continue
		}
		l.written.Add(1)
	}
}

// --- helpers for redaction ---

var sensitiveHeaders = map[string]bool{
	"authorization":   true,
	"x-api-key":       true,
	"x-goog-api-key":  true,
	"openai-api-key":  true,
	"anthropic-api-key": true,
	"cookie":          true,
	"set-cookie":      true,
}

func RedactHeaders(h http.Header, redact bool) map[string]string {
	out := make(map[string]string, len(h))
	for k, vv := range h {
		v := strings.Join(vv, ", ")
		if redact {
			lower := strings.ToLower(k)
			if sensitiveHeaders[lower] || strings.Contains(lower, "api-key") {
				v = "[redacted]"
			}
		}
		out[k] = v
	}
	return out
}

func RedactURL(raw string, redact bool) string {
	if !redact {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := parsed.Query()
	changed := false
	for k := range q {
		lower := strings.ToLower(k)
		if lower == "key" || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") {
			q.Set(k, "[redacted]")
			changed = true
		}
	}
	if changed {
		parsed.RawQuery = q.Encode()
	}
	return parsed.String()
}

// TruncateJSON returns the input as-is if it's valid JSON within the size cap,
// or a truncated string-as-JSON wrapper if too large or not JSON.
func TruncateJSON(data []byte, max int) json.RawMessage {
	if max <= 0 || len(data) <= max {
		// Verify it's valid JSON; if not, wrap as string.
		var probe any
		if json.Unmarshal(data, &probe) == nil {
			return json.RawMessage(data)
		}
	}
	if len(data) > max {
		data = data[:max]
	}
	wrapped, _ := json.Marshal(map[string]any{
		"truncated": true,
		"size":      len(data),
		"data":      string(data),
	})
	return wrapped
}

package cassette

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ModeOff    = "off"
	ModeRecord = "record"
	ModeReplay = "replay"

	DefaultDir = "tests/cassette"
)

// DefaultIgnoreFields are request keys dropped at any depth before hashing:
// ids and timestamps that agents regenerate on every run.
var DefaultIgnoreFields = []string{
	"id", "request_id", "session_id", "conversation_id", "trace_id",
	"tool_call_id", "tool_use_id", "call_id", "timestamp", "created", "created_at", "user",
}

var (
	uuidRe = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	tsRe   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?`)
	tmpRe  = regexp.MustCompile(`(?:/tmp|/var/folders|/private/var/folders|[A-Za-z]:\\Users\\[^\\\s"]+\\AppData\\Local\\Temp)[^\s"'` + "`" + `]*`)
)

// Entry is one recorded exchange. Request headers are never stored.
type Entry struct {
	Key         string          `json:"key"`
	RecordedAt  time.Time       `json:"recorded_at"`
	Path        string          `json:"path"`
	Model       string          `json:"model"`
	Stream      bool            `json:"stream"`
	Request     json.RawMessage `json:"request,omitempty"`
	RequestText string          `json:"request_text,omitempty"`
	Status      int             `json:"status"`
	ContentType string          `json:"content_type,omitempty"`
	Response    string          `json:"response"`

	leaves map[string]struct{}
}

type Store struct {
	mu      sync.RWMutex
	dir     string
	ignore  map[string]bool
	entries map[string]*Entry
}

func New() *Store { return &Store{entries: map[string]*Entry{}, ignore: map[string]bool{}} }

// Configure points the store at dir and reloads every entry found there.
func (s *Store) Configure(dir string, ignoreFields []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dir = dir
	s.ignore = map[string]bool{}
	for _, f := range DefaultIgnoreFields {
		s.ignore[f] = true
	}
	for _, f := range ignoreFields {
		s.ignore[strings.TrimSpace(f)] = true
	}
	s.entries = map[string]*Entry{}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil || e.Key == "" {
			continue
		}
		s.entries[e.Key] = &e
	}
	return nil
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

func (s *Store) normalize(body []byte) (norm []byte, isJSON bool) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return body, false
	}
	out, _ := json.Marshal(s.scrub(v))
	return out, true
}

func (s *Store) scrub(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			if s.ignore[k] {
				continue
			}
			m[k] = s.scrub(val)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = s.scrub(val)
		}
		return out
	case string:
		t = uuidRe.ReplaceAllString(t, "<uuid>")
		t = tsRe.ReplaceAllString(t, "<ts>")
		return tmpRe.ReplaceAllString(t, "<tmp>")
	}
	return v
}

func (s *Store) keyFor(path, model string, stream bool, body []byte) (key string, norm []byte, isJSON bool) {
	norm, isJSON = s.normalize(body)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%t\x00", path, model, stream)
	h.Write(norm)
	return hex.EncodeToString(h.Sum(nil))[:24], norm, isJSON
}

// Record stores a response; an identical normalised request overwrites the previous one.
func (s *Store) Record(path, model string, stream bool, body []byte, status int, contentType string, response []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		return fmt.Errorf("cassette directory not configured")
	}
	key, norm, isJSON := s.keyFor(path, model, stream, body)
	e := &Entry{
		Key: key, RecordedAt: time.Now().UTC(), Path: path, Model: model, Stream: stream,
		Status: status, ContentType: contentType, Response: string(response),
	}
	if isJSON {
		e.Request = norm
	} else {
		e.RequestText = fmt.Sprintf("<%d bytes non-JSON body>", len(body))
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%s.json", sanitize(model), key)
	if err := os.WriteFile(filepath.Join(s.dir, name), data, 0o644); err != nil {
		return err
	}
	s.entries[key] = e
	return nil
}

func sanitize(s string) string {
	out := []rune(s)
	for i, r := range out {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			out[i] = '_'
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return string(out)
}

// Lookup returns the recorded entry, or a human-readable description of the closest miss.
func (s *Store) Lookup(path, model string, stream bool, body []byte) (*Entry, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, norm, isJSON := s.keyFor(path, model, stream, body)
	if e, ok := s.entries[key]; ok {
		return e, ""
	}
	return nil, s.closestMiss(key, path, model, stream, norm, isJSON)
}

func leavesOf(raw []byte) map[string]struct{} {
	var v any
	out := map[string]struct{}{}
	if json.Unmarshal(raw, &v) != nil {
		return out
	}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, val := range t {
				walk(prefix+"."+k, val)
			}
		case []any:
			for i, val := range t {
				walk(fmt.Sprintf("%s[%d]", prefix, i), val)
			}
		default:
			b, _ := json.Marshal(t)
			out[prefix+"="+string(b)] = struct{}{}
		}
	}
	walk("", v)
	return out
}

func (s *Store) closestMiss(key, path, model string, stream bool, norm []byte, isJSON bool) string {
	msg := fmt.Sprintf("no recorded response for %s model=%q stream=%t (request key %s, %d entries in %s)",
		path, model, stream, key, len(s.entries), s.dir)
	if len(s.entries) == 0 {
		return msg + "; the cassette is empty"
	}
	if !isJSON {
		return msg
	}
	want := leavesOf(norm)
	var best *Entry
	bestScore := -1.0
	var keys []string
	for k := range s.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := s.entries[k]
		if e.Path != path {
			continue
		}
		if e.leaves == nil {
			e.leaves = leavesOf(e.Request)
		}
		inter := 0
		for l := range want {
			if _, ok := e.leaves[l]; ok {
				inter++
			}
		}
		union := len(want) + len(e.leaves) - inter
		score := 0.0
		if union > 0 {
			score = float64(inter) / float64(union)
		}
		if e.Model == model {
			score += 1
		}
		if score > bestScore {
			best, bestScore = e, score
		}
	}
	if best == nil {
		return msg + "; no entry for this endpoint"
	}
	var diffs []string
	for l := range want {
		if _, ok := best.leaves[l]; !ok {
			diffs = append(diffs, "request has "+clip(l))
		}
	}
	for l := range best.leaves {
		if _, ok := want[l]; !ok {
			diffs = append(diffs, "cassette has "+clip(l))
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 6 {
		diffs = append(diffs[:6], fmt.Sprintf("... and %d more", len(diffs)-6))
	}
	return fmt.Sprintf("%s; closest entry %s (model=%q) differs: %s", msg, best.Key, best.Model, strings.Join(diffs, "; "))
}

func clip(s string) string {
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

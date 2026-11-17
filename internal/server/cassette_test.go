package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/providers"
)

func newCassetteServer(t *testing.T, upstream string, cc config.CassetteConfig) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers = []config.Provider{{Name: "up", Type: "openai", BaseURL: upstream, Prefix: "up/"}}
	cfg.SetCassette(cc)
	srv := New(cfg, providers.NewRegistry(cfg), nil, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func post(t *testing.T, base, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(base+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestCassetteRecordThenReplayOffline(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		w.(http.Flusher).Flush()
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	dir := t.TempDir()

	rec := newCassetteServer(t, up.URL, config.CassetteConfig{Mode: "record", Dir: dir})
	reqA := `{"model":"up/m","stream":true,"messages":[{"role":"user","content":"hello"}],"user":"a"}`
	status, recorded := post(t, rec.URL, reqA)
	if status != 200 || !strings.Contains(recorded, "[DONE]") {
		t.Fatalf("record run: %d %s", status, recorded)
	}
	up.Close()

	rep := newCassetteServer(t, up.URL, config.CassetteConfig{Mode: "replay", Dir: dir})
	status, replayed := post(t, rep.URL, `{"user":"b","stream":true,"model":"up/m","messages":[{"role":"user","content":"hello"}]}`)
	if status != 200 || replayed != recorded {
		t.Fatalf("replay differs: %d %q vs %q", status, replayed, recorded)
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream hit %d times", hits.Load())
	}

	status, miss := post(t, rep.URL, `{"model":"up/m","stream":true,"messages":[{"role":"user","content":"bye"}]}`)
	if status != http.StatusNotFound || !strings.Contains(miss, "cassette_miss") || !strings.Contains(miss, "closest entry") {
		t.Fatalf("miss: %d %s", status, miss)
	}
}

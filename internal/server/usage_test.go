package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/providers"
	"github.com/aralde/operatorlm/internal/router"
)

func scanUsage(t *testing.T, stream bool, chunks ...string) (int64, int64, bool) {
	t.Helper()
	rec := httptest.NewRecorder()
	uw := &usageWriter{ResponseWriter: rec, stream: stream}
	for _, c := range chunks {
		if _, err := uw.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if got := rec.Body.String(); got != strings.Join(chunks, "") {
		t.Fatalf("body altered: %q", got)
	}
	return uw.result()
}

func TestUsageWriterFormats(t *testing.T) {
	cases := []struct {
		name       string
		stream     bool
		chunks     []string
		prompt     int64
		completion int64
		found      bool
	}{
		{"chat json", false, []string{`{"choices":[],"usage":{"prompt_tokens":12,`, `"completion_tokens":5}}`}, 12, 5, true},
		{"responses json", false, []string{`{"output":[],"usage":{"input_tokens":7,"output_tokens":3}}`}, 7, 3, true},
		{"no usage", false, []string{`{"choices":[]}`}, 0, 0, false},
		{"chat stream final chunk", true, []string{
			"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"usage\":null}\n\n",
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,",
			"\"completion_tokens\":4}}\n\ndata: [DONE]\n\n",
		}, 9, 4, true},
		{"responses stream completed", true, []string{
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n",
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":20,\"output_tokens\":8}}}\n\n",
		}, 20, 8, true},
		{"anthropic stream", true, []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":1}}}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":6}}\n\n",
		}, 11, 6, true},
		{"stream without usage", true, []string{"data: {\"choices\":[]}\n\ndata: [DONE]\n\n"}, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, comp, found := scanUsage(t, c.stream, c.chunks...)
			if p != c.prompt || comp != c.completion || found != c.found {
				t.Fatalf("got %d/%d/%v, want %d/%d/%v", p, comp, found, c.prompt, c.completion, c.found)
			}
		})
	}
}

func TestUsageEndpointCountsDispatchedReplies(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
	}))
	t.Cleanup(up.Close)

	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers = []config.Provider{{Name: "up", Type: "openai", BaseURL: up.URL, Prefix: "up/"}}
	ts := httptest.NewServer(New(cfg, providers.NewRegistry(cfg), nil, nil).Handler())
	t.Cleanup(ts.Close)

	for i := 0; i < 2; i++ {
		if status, body := post(t, ts.URL, `{"model":"up/m","messages":[{"role":"user","content":"hi"}]}`); status != 200 {
			t.Fatalf("%d %s", status, body)
		}
	}

	resp, err := http.Get(ts.URL + "/admin/usage")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var snap router.UsageSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Aliases) != 1 || snap.Aliases[0].Requests != 2 || snap.Aliases[0].PromptTokens != 20 || snap.Aliases[0].CompletionTokens != 4 {
		t.Fatalf("aliases: %+v", snap.Aliases)
	}
	if len(snap.Targets) != 1 || snap.Targets[0].ID != "slug:up/default/m" || snap.Targets[0].Provider != "up" {
		t.Fatalf("targets: %+v", snap.Targets)
	}
}

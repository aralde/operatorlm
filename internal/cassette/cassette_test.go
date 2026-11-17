package cassette

import (
	"bytes"
	"mime/multipart"
	"strings"
	"sync"
	"testing"
)

func TestNormalizedMatch(t *testing.T) {
	s := New()
	if err := s.Configure(t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	a := []byte(`{"model":"m","user":"u1","messages":[{"role":"user","content":"run in /tmp/abc123/x at 2026-01-02T03:04:05Z id 123e4567-e89b-12d3-a456-426614174000"}],"request_id":"r1"}`)
	b := []byte(`{"request_id":"r2","messages":[{"content":"run in /tmp/zzz/x at 2027-05-06T07:08:09Z id 00000000-e89b-12d3-a456-426614174999","role":"user"}],"model":"m","user":"u2"}`)
	if err := s.Record("/v1/chat/completions", "m", true, a, "application/json", 200, "text/event-stream", []byte("data: hi\n\n")); err != nil {
		t.Fatal(err)
	}
	e, miss := s.Lookup("/v1/chat/completions", "m", true, b, "application/json")
	if e == nil {
		t.Fatalf("expected hit, got miss: %s", miss)
	}
	if e.Response != "data: hi\n\n" || e.ContentType != "text/event-stream" {
		t.Fatalf("bad entry: %+v", e)
	}

	s2 := New()
	if err := s2.Configure(s.dir, nil); err != nil {
		t.Fatal(err)
	}
	if s2.Len() != 1 {
		t.Fatalf("reload found %d entries", s2.Len())
	}
}

func TestClosestMiss(t *testing.T) {
	s := New()
	if err := s.Configure(t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	_ = s.Record("/v1/chat/completions", "m", false, []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`), "application/json", 200, "application/json", []byte(`{}`))
	e, miss := s.Lookup("/v1/chat/completions", "m", false, []byte(`{"model":"m","messages":[{"role":"user","content":"goodbye"}]}`), "application/json")
	if e != nil {
		t.Fatal("expected miss")
	}
	for _, want := range []string{"closest entry", `request has .messages[0].content="goodbye"`, `cassette has .messages[0].content="hello"`} {
		if !strings.Contains(miss, want) {
			t.Errorf("miss message missing %q: %s", want, miss)
		}
	}
}

func TestEmptyCassette(t *testing.T) {
	s := New()
	_ = s.Configure(t.TempDir(), nil)
	if e, miss := s.Lookup("/v1/x", "m", false, []byte(`{}`), "application/json"); e != nil || !strings.Contains(miss, "empty") {
		t.Fatalf("got %v %q", e, miss)
	}
}

func multipartBody(t *testing.T, boundary string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.SetBoundary(boundary); err != nil {
		t.Fatal(err)
	}
	_ = w.WriteField("model", "whisper")
	fw, _ := w.CreateFormFile("file", "a.wav")
	_, _ = fw.Write([]byte("audio-bytes"))
	_ = w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

func TestMultipartIgnoresBoundary(t *testing.T) {
	s := New()
	if err := s.Configure(t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	a, ctA := multipartBody(t, "boundaryAAAAAAAAAA")
	b, ctB := multipartBody(t, "boundaryBBBBBBBBBB")
	if err := s.Record("/v1/audio/transcriptions", "whisper", false, a, ctA, 200, "application/json", []byte(`{"text":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if e, miss := s.Lookup("/v1/audio/transcriptions", "whisper", false, b, ctB); e == nil {
		t.Fatalf("expected hit, got miss: %s", miss)
	}
}

func TestParallelMisses(t *testing.T) {
	s := New()
	if err := s.Configure(t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	_ = s.Record("/v1/chat/completions", "m", false, []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`), "application/json", 200, "application/json", []byte(`{}`))
	s2 := New()
	if err := s2.Configure(s.dir, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e, _ := s2.Lookup("/v1/chat/completions", "m", false, []byte(`{"model":"m","messages":[{"role":"user","content":"bye"}]}`), "application/json"); e != nil {
				t.Error("expected miss")
			}
		}()
	}
	wg.Wait()
}

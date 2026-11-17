package audit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `{"id":"1","ts":"2026-01-01T10:00:00Z","client":"127.0.0.1:1","method":"POST","path":"/v1/chat/completions","status":200,"duration_ms":5}
not json
{"id":"2","ts":"2026-01-01T10:00:02Z","client":"127.0.0.1:2","ua":"curl","method":"POST","path":"/v1/chat/completions","status":500,"duration_ms":9}
{"id":"3","ts":"2026-01-01T10:00:03Z","client":"127.0.0.1:1","method":"POST","path":"/v1/messages","status":200,"duration_ms":7}
`

func writeSample(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(p, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExportWholeAndSession(t *testing.T) {
	p := writeSample(t)

	var all bytes.Buffer
	n, err := Export(p, "", &all)
	if err != nil || n != 3 {
		t.Fatalf("whole export: n=%d err=%v", n, err)
	}
	if strings.Contains(all.String(), "not json") {
		t.Fatal("invalid line leaked into export")
	}

	var one bytes.Buffer
	n, err = Export(p, "127.0.0.1:1", &one)
	if err != nil || n != 2 {
		t.Fatalf("session export: n=%d err=%v", n, err)
	}
	lines := strings.Split(strings.TrimSpace(one.String()), "\n")
	if !strings.HasPrefix(lines[0], `{"id":"1"`) || !strings.HasPrefix(lines[1], `{"id":"3"`) {
		t.Fatalf("records altered or reordered: %q", lines)
	}
}

func TestSessions(t *testing.T) {
	got, err := Sessions(writeSample(t))
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions: %v %v", got, err)
	}
	if got[0].Client != "127.0.0.1:1" || got[0].Records != 2 {
		t.Fatalf("unexpected first session: %+v", got[0])
	}
}

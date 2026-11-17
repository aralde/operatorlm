package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/aralde/operatorlm/internal/audit"
)

func (s *Server) auditPath() (string, error) {
	p := s.audit.Options().Path
	if p == "" {
		p = s.cfg.GetAudit().Path
	}
	if p == "" {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, ".operatorlm", "audit.log")
	}
	if _, err := os.Stat(p); err != nil {
		return "", errors.New("no audit log found at " + p)
	}
	return p, nil
}

func (s *Server) handleAuditSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, err := s.auditPath()
	if err != nil {
		writeJSON(w, http.StatusOK, []audit.Session{})
		return
	}
	sessions, err := audit.Sessions(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, err := s.auditPath()
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", exportName(r.URL.Query().Get("client"))))
	if _, err := audit.Export(p, r.URL.Query().Get("client"), w); err != nil {
		log.Printf("audit export: %v", err)
	}
}

func exportName(client string) string {
	stamp := time.Now().Format("20060102-150405")
	if client == "" {
		return "operatorlm-audit-" + stamp + ".ndjson"
	}
	return "operatorlm-audit-session-" + stamp + ".ndjson"
}

func (s *Server) handleAuditOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-OperatorLM-Admin") != "1" {
		http.Error(w, "forbidden: missing X-OperatorLM-Admin header", http.StatusForbidden)
		return
	}
	p, err := s.auditPath()
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	client := r.URL.Query().Get("client")
	f, err := os.CreateTemp("", "operatorlm-audit-*.ndjson")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	n, err := audit.Export(p, client, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n == 0 {
		os.Remove(f.Name())
		http.Error(w, "no records to open", http.StatusNotFound)
		return
	}
	if err := openInThreadline(f.Name()); err != nil {
		os.Remove(f.Name())
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "opened", "file": f.Name(), "records": n})
}

func threadlineBinary() string {
	names := []string{"ThreadlineLM", "threadlinelm"}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			candidates = append(candidates,
				filepath.Join(d, "Programs", "ThreadlineLM", "ThreadlineLM.exe"),
				filepath.Join(d, "ThreadlineLM", "ThreadlineLM.exe"))
		}
		if d := os.Getenv("ProgramFiles"); d != "" {
			candidates = append(candidates, filepath.Join(d, "ThreadlineLM", "ThreadlineLM.exe"))
		}
	case "darwin":
		candidates = append(candidates,
			"/Applications/ThreadlineLM.app",
			filepath.Join(home, "Applications", "ThreadlineLM.app"))
	default:
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "ThreadlineLM"),
			filepath.Join(home, ".local", "bin", "threadlinelm"),
			"/opt/ThreadlineLM/ThreadlineLM")
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// openInThreadline starts ThreadlineLM on file: the app binary when found,
// otherwise the OS file association.
func openInThreadline(file string) error {
	var cmd *exec.Cmd
	bin := threadlineBinary()
	switch {
	case bin != "" && runtime.GOOS == "darwin" && filepath.Ext(bin) == ".app":
		cmd = exec.Command("open", "-a", bin, file)
	case bin != "":
		cmd = exec.Command(bin, file)
	case runtime.GOOS == "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", file)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", file)
	default:
		opener, err := exec.LookPath("xdg-open")
		if err != nil {
			return errors.New("ThreadlineLM was not found; install it or use Export")
		}
		cmd = exec.Command(opener, file)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not launch ThreadlineLM: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

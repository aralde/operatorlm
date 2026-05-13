package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/providers"
)

// chatgptAuthFlow tracks one in-flight "Login with ChatGPT" attempt.
type chatgptAuthFlow struct {
	state        string
	verifier     string
	providerName string
	keyRef       string
	createdAt    time.Time

	mu     sync.Mutex
	status string // "pending" | "success" | "error"
	err    string
}

var (
	chatgptAuthMu     sync.Mutex
	chatgptAuthActive *chatgptAuthFlow
	chatgptAuthSrv    *http.Server
)

func (s *Server) handleChatGPTAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Provider == "" {
		http.Error(w, "provider required", http.StatusBadRequest)
		return
	}
	prov, ok := s.cfg.FindProvider(body.Provider)
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	if prov.Type != "chatgpt-codex" {
		http.Error(w, "provider type must be chatgpt-codex", http.StatusBadRequest)
		return
	}
	keyRef := prov.APIKeyRef
	if keyRef == "" {
		keyRef = "operatorlm:" + prov.Name
	}

	pkce, err := providers.GeneratePKCE()
	if err != nil {
		http.Error(w, "pkce: "+err.Error(), http.StatusInternalServerError)
		return
	}
	state, err := providers.RandomState()
	if err != nil {
		http.Error(w, "state: "+err.Error(), http.StatusInternalServerError)
		return
	}

	flow := &chatgptAuthFlow{
		state:        state,
		verifier:     pkce.Verifier,
		providerName: prov.Name,
		keyRef:       keyRef,
		createdAt:    time.Now(),
		status:       "pending",
	}

	chatgptAuthMu.Lock()
	// Best-effort cancel any prior in-flight flow.
	if chatgptAuthSrv != nil {
		_ = chatgptAuthSrv.Close()
		chatgptAuthSrv = nil
	}
	chatgptAuthActive = flow

	if err := startChatGPTCallbackServer(s); err != nil {
		chatgptAuthActive = nil
		chatgptAuthMu.Unlock()
		http.Error(w, "callback listener: "+err.Error(), http.StatusInternalServerError)
		return
	}
	chatgptAuthMu.Unlock()

	authURL := providers.BuildChatGPTAuthorizeURL(pkce.Challenge, state)
	openBrowser(authURL)

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "started",
		"state":  state,
		"url":    authURL,
	})
}

func (s *Server) handleChatGPTAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chatgptAuthMu.Lock()
	flow := chatgptAuthActive
	chatgptAuthMu.Unlock()
	if flow == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "idle"})
		return
	}
	flow.mu.Lock()
	resp := map[string]any{
		"status":   flow.status,
		"provider": flow.providerName,
	}
	if flow.err != "" {
		resp["error"] = flow.err
	}
	flow.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

// startChatGPTCallbackServer launches an http server on :1455 that handles
// /auth/callback. Caller must hold chatgptAuthMu.
func startChatGPTCallbackServer(s *Server) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		handleChatGPTCallback(s, w, r)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux}
	chatgptAuthSrv = srv
	go func() { _ = srv.Serve(ln) }()
	return nil
}

func handleChatGPTCallback(s *Server, w http.ResponseWriter, r *http.Request) {
	chatgptAuthMu.Lock()
	flow := chatgptAuthActive
	chatgptAuthMu.Unlock()

	if flow == nil {
		http.Error(w, "no active flow", http.StatusBadRequest)
		return
	}

	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		failChatGPTFlow(flow, errParam+": "+q.Get("error_description"))
		writeChatGPTHTML(w, false, errParam)
		shutdownChatGPTCallbackServer()
		return
	}
	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		failChatGPTFlow(flow, "missing code or state")
		writeChatGPTHTML(w, false, "missing code/state")
		shutdownChatGPTCallbackServer()
		return
	}
	if state != flow.state {
		failChatGPTFlow(flow, "state mismatch")
		writeChatGPTHTML(w, false, "state mismatch")
		shutdownChatGPTCallbackServer()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tokens, err := providers.ExchangeChatGPTCode(ctx, code, flow.verifier)
	if err != nil {
		failChatGPTFlow(flow, "token exchange: "+err.Error())
		writeChatGPTHTML(w, false, err.Error())
		shutdownChatGPTCallbackServer()
		return
	}

	data, err := json.Marshal(tokens)
	if err != nil {
		failChatGPTFlow(flow, "marshal tokens: "+err.Error())
		writeChatGPTHTML(w, false, err.Error())
		shutdownChatGPTCallbackServer()
		return
	}
	if err := config.SetSecret(flow.keyRef, string(data)); err != nil {
		failChatGPTFlow(flow, "save tokens: "+err.Error())
		writeChatGPTHTML(w, false, err.Error())
		shutdownChatGPTCallbackServer()
		return
	}

	flow.mu.Lock()
	flow.status = "success"
	flow.mu.Unlock()

	// Force a registry reload so the new tokens are picked up immediately.
	s.reg.Reload()

	writeChatGPTHTML(w, true, "")
	shutdownChatGPTCallbackServer()
}

func failChatGPTFlow(flow *chatgptAuthFlow, msg string) {
	flow.mu.Lock()
	flow.status = "error"
	flow.err = msg
	flow.mu.Unlock()
}

func shutdownChatGPTCallbackServer() {
	chatgptAuthMu.Lock()
	srv := chatgptAuthSrv
	chatgptAuthSrv = nil
	chatgptAuthMu.Unlock()
	if srv != nil {
		// Close after a short delay so the response can flush.
		go func() {
			time.Sleep(200 * time.Millisecond)
			_ = srv.Close()
		}()
	}
}

func writeChatGPTHTML(w http.ResponseWriter, ok bool, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if ok {
		fmt.Fprint(w, chatgptAuthSuccessHTML)
		return
	}
	fmt.Fprintf(w, chatgptAuthErrorHTML, errMsg)
}

const chatgptAuthSuccessHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Login OK</title>
<style>body{font-family:system-ui,sans-serif;background:#0b0b0c;color:#e5e7eb;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}.card{background:#161618;border:1px solid #27272a;border-radius:14px;padding:32px 40px;text-align:center;max-width:420px}h1{font-size:18px;margin:0 0 8px}p{font-size:14px;color:#a1a1aa;margin:0}</style>
</head><body><div class="card"><h1>OperatorLM &middot; ChatGPT login successful</h1><p>You can close this tab and return to the OperatorLM admin UI.</p></div></body></html>`

const chatgptAuthErrorHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Login error</title>
<style>body{font-family:system-ui,sans-serif;background:#0b0b0c;color:#e5e7eb;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}.card{background:#161618;border:1px solid #7f1d1d;border-radius:14px;padding:32px 40px;text-align:center;max-width:480px}h1{font-size:18px;margin:0 0 8px;color:#fca5a5}code{background:#0b0b0c;padding:2px 6px;border-radius:4px}</style>
</head><body><div class="card"><h1>Login failed</h1><p><code>%s</code></p></div></body></html>`

// openBrowser launches the user's default browser at url.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

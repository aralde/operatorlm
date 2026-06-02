package server

import (
	"crypto/subtle"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/rs/cors"

	"github.com/aralde/operatorlm/internal/audit"
	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/providers"
	"github.com/aralde/operatorlm/internal/router"
	"github.com/aralde/operatorlm/internal/update"
)

func logMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, sw.code, time.Since(start))
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

//go:embed all:web
var webFS embed.FS

type Server struct {
	cfg   *config.Config
	reg   *providers.Registry
	rt    *router.Router
	audit *audit.Logger
	upd   *update.Manager
}

func New(cfg *config.Config, reg *providers.Registry, auditLogger *audit.Logger, upd *update.Manager) *Server {
	return &Server{cfg: cfg, reg: reg, rt: router.New(cfg), audit: auditLogger, upd: upd}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	mux.HandleFunc("/v1/images/generations", s.handleImages)
	mux.HandleFunc("/v1/responses", s.handleResponses)
	mux.HandleFunc("/v1/embeddings", s.handleEmbeddings)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/messages", s.handleMessages)
	mux.HandleFunc("/health", s.handleHealthGlobal)

	mux.HandleFunc("/admin/providers", s.handleProviders)
	mux.HandleFunc("/admin/providers/probe", s.handleProbe)
	mux.HandleFunc("/admin/providers/", s.handleProviderItem)
	mux.HandleFunc("/admin/aliases", s.handleAliases)
	mux.HandleFunc("/admin/aliases/", s.handleAliasItem)
	mux.HandleFunc("/admin/audit", s.handleAudit)
	mux.HandleFunc("/admin/reliability", s.handleReliability)
	mux.HandleFunc("/admin/health", s.handleHealth)
	mux.HandleFunc("/admin/health/clear", s.handleHealthClear)
	mux.HandleFunc("/admin/auth/chatgpt/start", s.handleChatGPTAuthStart)
	mux.HandleFunc("/admin/auth/chatgpt/status", s.handleChatGPTAuthStatus)
	mux.HandleFunc("/admin/localauth", s.handleLocalAuth)
	mux.HandleFunc("/admin/update/status", s.handleUpdateStatus)
	mux.HandleFunc("/admin/update/check", s.handleUpdateCheck)

	sub, err := fs.Sub(webFS, "web")
	if err == nil {
		mux.Handle("/", http.FileServer(http.FS(sub)))
	}

	corsMW := cors.New(cors.Options{
		AllowedOrigins:   s.cfg.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: false,
	})

	inner := s.localAuthMW(logMW(mux))
	v1Handler := corsMW.Handler(inner)

	// Top-level dispatch:
	//  - /health  : Public health check, no CORS or local auth.
	//  - /v1/*    : CORS applied (legitimate cross-origin clients like editors/IDEs).
	//  - /admin/* : NO CORS, plus Host-header validation against DNS-rebinding.
	//  - other    : static admin UI; no CORS, but X-Frame-Options to block iframes.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/health":
			mux.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/v1/"):
			v1Handler.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/admin/"):
			if !isLocalHost(r.Host) {
				http.Error(w, "forbidden: admin endpoints require a localhost Host header", http.StatusForbidden)
				return
			}
			inner.ServeHTTP(w, r)
		default:
			if !isLocalHost(r.Host) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
			inner.ServeHTTP(w, r)
		}
	})
}

// isLocalHost reports whether the request's Host header is a loopback address.
// Mitigates DNS-rebinding attacks against /admin/*: even if a malicious page
// rebinds attacker.example to 127.0.0.1, the browser keeps sending Host:
// attacker.example and we reject it.
func isLocalHost(host string) bool {
	h := host
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.HasSuffix(host, "]") {
		// Strip :port (but not from bare IPv6 like [::1]:80 — handle below).
		if strings.HasPrefix(host, "[") {
			if end := strings.IndexByte(host, ']'); end >= 0 {
				h = host[1:end]
			}
		} else {
			h = host[:i]
		}
	}
	switch h {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return false
}

// localAuthMW enforces a client-supplied Bearer token on /v1/* when enabled.
// /admin/* and static assets are not affected — they still rely on localhost-only binding.
func (s *Server) localAuthMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		la := s.cfg.GetLocalAuth()
		if !la.Enabled || la.KeyRef == "" {
			next.ServeHTTP(w, r)
			return
		}
		expected, err := config.GetSecret(la.KeyRef)
		if err != nil || expected == "" {
			http.Error(w, `{"error":"local_auth_misconfigured"}`, http.StatusInternalServerError)
			return
		}
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, prefix) ||
			subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(expected)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="operatorlm"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key","type":"invalid_request_error"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

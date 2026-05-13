package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/getlantern/systray"

	"github.com/aralde/operatorlm/internal/audit"
	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/providers"
	"github.com/aralde/operatorlm/internal/server"
	"github.com/aralde/operatorlm/internal/tray"
	"github.com/aralde/operatorlm/internal/update"
	"github.com/aralde/operatorlm/internal/version"
)

// setupLogging mirrors log output to ~/.operatorlm/operatorlm.log so that
// builds with -H=windowsgui (no console) still produce a readable trace.
func setupLogging() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".operatorlm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "operatorlm.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
}

func main() {
	setupLogging()
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	auditLogger := audit.New()
	if a := cfg.GetAudit(); a.Enabled {
		opts := auditOptsFromConfig(a)
		if err := auditLogger.Reconfigure(true, opts); err != nil {
			log.Printf("audit: failed to enable (%v); continuing disabled", err)
		} else {
			log.Printf("audit: enabled, writing to %s", opts.Path)
		}
	}

	reg := providers.NewRegistry(cfg)
	updMgr := update.NewManager(version.Version)
	srv := server.New(cfg, reg, auditLogger, updMgr)
	log.Printf("operatorlm version=%q", version.Version)

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr(),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("operatorlm listening on http://%s", cfg.ListenAddr())
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http server error: %v", err)
		}
	}()

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
		_ = auditLogger.Close()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Headless mode: skip the system tray entirely. Useful on Linux servers
	// without an AppIndicator-capable desktop session, or for running under
	// systemd/Docker.
	if os.Getenv("OPERATORLM_NO_TRAY") != "" {
		log.Printf("OPERATORLM_NO_TRAY set; running without system tray")
		<-sigCh
		shutdown()
		return
	}

	go func() {
		<-sigCh
		shutdown()
		systray.Quit()
	}()

	systray.Run(
		func() { tray.OnReady(cfg, updMgr) },
		func() { shutdown() },
	)
}

func auditOptsFromConfig(a config.AuditConfig) audit.Options {
	path := a.Path
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".operatorlm", "audit.log")
	}
	return audit.Options{
		Path:                 path,
		BufferSize:           a.BufferSize,
		MaxRequestBodyBytes:  a.MaxRequestBodyBytes,
		MaxResponseBodyBytes: a.MaxResponseBodyBytes,
		Redact:               a.Redact,
	}
}

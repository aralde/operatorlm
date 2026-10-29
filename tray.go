//go:build !notray

package main

import (
	"os"

	"github.com/getlantern/systray"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/tray"
	"github.com/aralde/operatorlm/internal/update"
)

const trayAvailable = true

func runTray(cfg *config.Config, updMgr *update.Manager, sigCh <-chan os.Signal, shutdown func()) {
	update.OnQuit = systray.Quit

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

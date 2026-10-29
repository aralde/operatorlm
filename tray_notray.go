//go:build notray

package main

import (
	"os"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/update"
)

const trayAvailable = false

func runTray(*config.Config, *update.Manager, <-chan os.Signal, func()) {}

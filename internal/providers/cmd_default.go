//go:build !windows

package providers

import (
	"context"
	"os"
	"os/exec"
)

func prepareCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

func startCommand(cmd *exec.Cmd) error {
	return cmd.Start()
}

func getActiveLSInfo() (string, string, string) {
	addr := os.Getenv("ANTIGRAVITY_LS_ADDRESS")
	if addr == "" {
		addr = "localhost:53235"
	}
	token := os.Getenv("ANTIGRAVITY_CSRF_TOKEN")
	projectID := getProjectIDFromPB()
	return addr, token, projectID
}

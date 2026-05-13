package version

// Version is injected at build time via:
//   -ldflags "-X github.com/aralde/operatorlm/internal/version.Version=v1.2.3"
// An empty string means "dev build" — the updater refuses to self-update in that case.
var Version = ""

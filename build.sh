#!/usr/bin/env bash
# Cross-platform build script for operatorlm.
#
# Detects the target OS/arch (or honours GOOS/GOARCH from the environment) and
# builds with the right linker flags. On Windows it forces -H=windowsgui so no
# console window appears at launch (logs go to ~/.operatorlm/operatorlm.log via
# setupLogging in main.go). On Linux/macOS it produces a normal binary.
#
# Usage:
#   ./build.sh                              # build for the host
#   GOOS=linux GOARCH=arm64 ./build.sh      # cross-compile
#   GOOS=windows ./build.sh                 # produces operatorlm.exe
set -euo pipefail

cd "$(dirname "$0")"

target_os="${GOOS:-$(go env GOOS)}"
target_arch="${GOARCH:-$(go env GOARCH)}"

ldflags=""
output="operatorlm"

case "$target_os" in
  windows)
    ldflags="-H=windowsgui"
    output="OperatorLM.exe"
    ;;
  linux|darwin)
    : # CGO is required by getlantern/systray; rely on the toolchain default.
    ;;
  *)
    echo "warning: untested GOOS=$target_os; proceeding with defaults" >&2
    ;;
esac

# Inject the build-time version so the OTA updater can compare against the
# latest GitHub release. Honour $VERSION from the environment, otherwise fall
# back to `git describe`. An empty result means "dev build" and the updater
# will refuse to self-update.
version="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || true)}"
if [ -n "$version" ]; then
  ver_flag="-X github.com/aralde/operatorlm/internal/version.Version=$version"
  if [ -n "$ldflags" ]; then
    ldflags="$ldflags $ver_flag"
  else
    ldflags="$ver_flag"
  fi
fi

# getlantern/systray requires CGO on every platform.
export CGO_ENABLED="${CGO_ENABLED:-1}"

echo "Building $output for $target_os/$target_arch (CGO_ENABLED=$CGO_ENABLED, version=${version:-dev})"

if [ -n "$ldflags" ]; then
  GOOS="$target_os" GOARCH="$target_arch" go build -ldflags="$ldflags" -o "$output" .
else
  GOOS="$target_os" GOARCH="$target_arch" go build -o "$output" .
fi

echo "Built $output"

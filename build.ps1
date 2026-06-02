# Build the Windows release binary as a GUI app so no console window appears
# when launched. Logs still go to ~/.operatorlm/operatorlm.log (see setupLogging
# in main.go).
#
# This is the Windows-native build path. For Linux/macOS (or cross-compiling
# from any host), use ./build.sh instead — it picks the correct linker flags
# per GOOS and skips -H=windowsgui where it does not apply.
#
# Usage:
#   .\build.ps1                  # auto-detect version from git tag
#   .\build.ps1 -Version v0.2.0  # pin an explicit version string
param(
  [string]$Version = ""
)
$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
  if (-not $Version) {
    try {
      $Version = (git describe --tags --always --dirty 2>$null).Trim()
    } catch {
      $Version = ""
    }
  }
  $verFlag = ""
  if ($Version) {
    $verFlag = " -X github.com/aralde/operatorlm/internal/version.Version=$Version"
  }
  # -s -w: strip symbol/debug info (smaller binary, fewer AV false positives).
  # -H=windowsgui: GUI subsystem, no console window on launch.
  # -trimpath / -buildvcs=false: strip local paths and VCS stamps so the binary
  #   is reproducible and doesn't leak C:\Users\... - also reduces SmartScreen
  #   / Defender false positives on fresh Go binaries.
  $ldflags = "-H=windowsgui -s -w$verFlag"
  go build -trimpath -buildvcs=false -ldflags="$ldflags" -o OperatorLM.exe .
  if ($Version) {
    Write-Host "Built OperatorLM.exe (windowsgui mode, version=$Version)" -ForegroundColor Green
  } else {
    Write-Host "Built OperatorLM.exe (windowsgui mode, dev build - no version)" -ForegroundColor Yellow
  }
} finally {
  Pop-Location
}

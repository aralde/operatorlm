@echo off
REM Build the Windows release binary as a GUI app so no console window appears
REM when launched. Logs still go to %USERPROFILE%\.operatorlm\operatorlm.log
REM (see setupLogging in main.go).
REM
REM The build-time version is read from the VERSION env var; if unset we try
REM `git describe`. An empty version means "dev build" and the OTA updater
REM will refuse to self-update.
setlocal
pushd "%~dp0"

set "VER=%VERSION%"
if "%VER%"=="" (
  for /f "delims=" %%v in ('git describe --tags --always --dirty 2^>nul') do set "VER=%%v"
)

set "LDFLAGS=-H=windowsgui"
if not "%VER%"=="" (
  set "LDFLAGS=%LDFLAGS% -X github.com/aralde/operatorlm/internal/version.Version=%VER%"
)

go build -ldflags="%LDFLAGS%" -o OperatorLM.exe . || exit /b 1
if "%VER%"=="" (
  echo Built OperatorLM.exe windowsgui mode, dev build
) else (
  echo Built OperatorLM.exe windowsgui mode, version=%VER%
)
popd
endlocal

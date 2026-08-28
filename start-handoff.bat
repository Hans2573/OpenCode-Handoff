@echo off
setlocal

cd /d "%~dp0"
set "APP=%~dp0opencode-handoff.exe"
set "CONFIG=%~dp0config.yaml"
set "TEMPLATE=%~dp0config.example.yaml"

if not exist "%APP%" (
  echo opencode-handoff.exe was not found in this folder.
  pause
  exit /b 1
)

if not exist "%CONFIG%" (
  if not exist "%TEMPLATE%" (
    echo config.yaml and config.example.yaml were not found.
    pause
    exit /b 1
  )
  copy /Y "%TEMPLATE%" "%CONFIG%" >nul
  echo A config.yaml file was created from the example template.
  echo Fill in your Feishu and OpenCode settings, save the file, then run this script again.
  start "" notepad.exe "%CONFIG%"
  pause
  exit /b 2
)

echo Starting OpenCode Handoff...
"%APP%" -config "%CONFIG%"
if errorlevel 1 (
  echo.
  echo OpenCode Handoff exited with an error. The window is kept open for review.
  pause
)

endlocal

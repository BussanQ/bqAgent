@echo off
setlocal

cd /d "%~dp0"

echo [1/2] Building bqagent.exe...
go build -trimpath -o bqagent.exe .\cmd\agent
if errorlevel 1 (
    echo Build failed.
    exit /b 1
)

echo [2/2] Copying bqagent.exe to run0808...
copy /Y "bqagent.exe" "D:\Dev\ai\code\run\run0808\bqagent.exe" >nul
if errorlevel 1 (
    echo Copy failed.
    exit /b 1
)

echo Build and copy completed successfully.
endlocal

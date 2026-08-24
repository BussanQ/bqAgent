@echo off
setlocal

cd /d "%~dp0"

echo [1/3] Building bqagent.exe...
go build -trimpath -o bqagent.exe .\cmd\agent
if errorlevel 1 (
    echo Build failed.
    exit /b 1
)

echo [2/3] Killing bqagent.exe if running...
taskkill /F /IM bqagent.exe
echo [3/3] Running bqagent.exe
.\bqagent.exe -d
if errorlevel 1 (
    echo Failed to run bqagent.exe.
    exit /b 1
)

echo Build and run completed successfully.
endlocal
 
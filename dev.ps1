# DFMT Development Script
# Build, install to PATH, and re-init DFMT from source
# Run this after `git pull` to rebuild and redeploy

$ErrorActionPreference = 'Stop'

$RepoRoot = $PSScriptRoot
$BinaryName = "dfmt.exe"
$NewBinary = Join-Path $RepoRoot $BinaryName

# Stop ALL running dfmt processes (daemon, MCP servers, hook invocations)
# so the installed binary file is not locked.
Write-Host "==> Stopping all dfmt processes..." -ForegroundColor Cyan
$running = Get-Process dfmt -ErrorAction SilentlyContinue
if ($running) {
    Write-Host "    stopping $($running.Count) running dfmt process(es)..." -ForegroundColor DarkGray
    $running | Stop-Process -Force
    Start-Sleep -Milliseconds 800
}

# Also kill any dfmt in Task Manager (robust cleanup)
$dfmtTasks = Get-CimInstance Win32_Process -Filter "Name='dfmt.exe'" -ErrorAction SilentlyContinue
if ($dfmtTasks) {
    foreach ($task in $dfmtTasks) {
        Write-Host "    killing PID $($task.ProcessId): $($task.CommandLine)" -ForegroundColor DarkGray
        Stop-Process -Id $task.ProcessId -Force -ErrorAction SilentlyContinue
    }
    Start-Sleep -Milliseconds 400
}

Write-Host "==> Building dfmt from source..." -ForegroundColor Cyan
Set-Location $RepoRoot
go build -ldflags "-X internal/cli.Version=0.1.0-dev" -o $NewBinary ./cmd/dfmt

if (-not (Test-Path $NewBinary)) {
    Write-Host "error: build failed, dfmt.exe not found" -ForegroundColor Red
    exit 1
}

Write-Host "==> Installing to LOCALAPPDATA\Programs\dfmt..." -ForegroundColor Cyan
$TargetDir = "$env:LOCALAPPDATA\Programs\dfmt"
$TargetPath = Join-Path $TargetDir $BinaryName

New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null
Copy-Item -Force $NewBinary -Destination $TargetPath

# Ensure TARGETDIR is in PATH (user scope — persists across restarts)
$UserPath = [Environment]::GetEnvironmentVariable("Path", [EnvironmentVariableTarget]::User)
$TargetDirEscaped = $TargetDir -replace '\\', '\\'
if ($UserPath -notlike "*$TargetDir*") {
    Write-Host "    adding $TargetDir to user PATH..." -ForegroundColor DarkGray
    $newPath = "$UserPath;$TargetDir"
    [Environment]::SetEnvironmentVariable("Path", $newPath, [EnvironmentVariableTarget]::User)
    $env:Path = "$env:Path;$TargetDir"  # Update current session immediately
} else {
    Write-Host "    $TargetDir already in PATH" -ForegroundColor DarkGray
}

Write-Host "==> Verifying install..." -ForegroundColor Cyan
& $TargetPath --version

Write-Host "==> Re-initializing current project..." -ForegroundColor Cyan
Set-Location $RepoRoot
& $TargetPath init

Write-Host ""
Write-Host "==> New settings.json hooks:" -ForegroundColor Green
Get-Content ".\.claude\settings.json" | Select-String -Pattern "command"

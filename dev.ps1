# DFMT Development Script
# Build, install, and re-init DFMT from source

$ErrorActionPreference = 'Stop'

$RepoRoot = $PSScriptRoot
$NewBinary = Join-Path $RepoRoot "dfmt-new.exe"

Write-Host "==> Building dfmt from source..." -ForegroundColor Cyan
Set-Location $RepoRoot
go build -ldflags "-X internal/cli.Version=0.1.0-dev" -o dfmt-new.exe ./cmd/dfmt

if (-not (Test-Path $NewBinary)) {
    Write-Host "error: build failed, dfmt-new.exe not found" -ForegroundColor Red
    exit 1
}

Write-Host "==> Installing to LOCALAPPDATA..." -ForegroundColor Cyan
$TargetDir = "$env:LOCALAPPDATA\Programs\dfmt"
$TargetPath = Join-Path $TargetDir "dfmt.exe"

New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null

# Stop any running dfmt processes (daemon, MCP servers, hook invocations)
# so the installed binary file is no longer locked.
$running = Get-Process dfmt -ErrorAction SilentlyContinue
if ($running) {
    Write-Host "    stopping $($running.Count) running dfmt process(es)..." -ForegroundColor DarkGray
    $running | Stop-Process -Force
    Start-Sleep -Milliseconds 800
}

Copy-Item -Force $NewBinary -Destination $TargetPath

Write-Host "==> Verifying install..." -ForegroundColor Cyan
& $TargetPath --version

Write-Host "==> Re-initializing current project..." -ForegroundColor Cyan
Set-Location $RepoRoot
& $TargetPath init

Write-Host ""
Write-Host "==> New settings.json hooks:" -ForegroundColor Green
Get-Content .\.claude\settings.json | Select-String -Pattern "command"
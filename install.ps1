# DFMT one-command installer (Windows PowerShell 5.1+ / 7+).
# Usage:
#   iwr https://raw.githubusercontent.com/ersinkoc/dfmt/main/install.ps1 | iex
#
# Environment:
#   $env:DFMT_DEBUG = "1"   # verbose output

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Repo = 'github.com/ersinkoc/dfmt/cmd/dfmt@latest'

function Write-Info($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Warn($msg) { Write-Host "warn: $msg" -ForegroundColor Yellow }
function Write-Err ($msg) { Write-Host "error: $msg" -ForegroundColor Red }

if ($env:DFMT_DEBUG -eq '1') { $VerbosePreference = 'Continue' }

# --- step 1: go available? -------------------------------------------------
$goCmd = Get-Command go -ErrorAction SilentlyContinue
if (-not $goCmd) {
    Write-Err "Go toolchain not found on PATH."
    Write-Host ""
    Write-Host "DFMT installs via `go install`. Install Go first:"
    Write-Host "  winget install GoLang.Go"
    Write-Host "  # or download from https://go.dev/dl/"
    Write-Host ""
    Write-Host "Then re-run this installer."
    exit 1
}

try {
    $goVersion = (& go version) 2>$null
    Write-Info "using $goVersion"
} catch {
    Write-Info "using go (version probe failed, continuing)"
}

# --- step 2: go install ----------------------------------------------------
Write-Info "installing dfmt from $Repo"
try {
    & go install $Repo
    if ($LASTEXITCODE -ne 0) {
        throw "go install exited with code $LASTEXITCODE"
    }
} catch {
    Write-Err "go install failed: $_"
    Write-Host "Re-run with `$env:DFMT_DEBUG = '1'` for details."
    exit 1
}

# --- step 3: locate binary -------------------------------------------------
$goBin = (& go env GOBIN) 2>$null
if (-not $goBin) {
    $goPath = (& go env GOPATH) 2>$null
    if ($goPath) { $goBin = Join-Path $goPath 'bin' }
}

$dfmtCmd = Get-Command dfmt -ErrorAction SilentlyContinue
if (-not $dfmtCmd) {
    $exe = Join-Path $goBin 'dfmt.exe'
    if ($goBin -and (Test-Path $exe)) {
        Write-Warn "dfmt installed to $goBin but that directory is not on your PATH."
        Write-Warn "Add it to your user PATH, e.g.:"
        Write-Warn "    [Environment]::SetEnvironmentVariable('Path', `"$goBin;`" + [Environment]::GetEnvironmentVariable('Path','User'), 'User')"
        $dfmtExe = $exe
    } else {
        Write-Err "dfmt binary not found after install. Check 'go env GOBIN / GOPATH'."
        exit 1
    }
} else {
    Write-Info "dfmt binary: $($dfmtCmd.Source)"
    $dfmtExe = $dfmtCmd.Source
}

# --- step 4: dfmt setup ----------------------------------------------------
Write-Info "running 'dfmt setup --force' to wire up detected agents"
try {
    & $dfmtExe setup --force
    if ($LASTEXITCODE -ne 0) {
        Write-Warn "'dfmt setup' returned $LASTEXITCODE. You can re-run it later."
    }
} catch {
    Write-Warn "'dfmt setup' failed: $_"
}

# --- step 5: success banner ------------------------------------------------
Write-Host ""
Write-Host "DFMT installed." -ForegroundColor Green
Write-Host ""
Write-Host "Next steps:"
Write-Host "  1.  cd C:\path\to\your\project"
Write-Host "  2.  dfmt init"
Write-Host "  3.  restart Claude Code (or your agent) and you're done"
Write-Host ""
Write-Host "Docs:  https://github.com/ersinkoc/dfmt"

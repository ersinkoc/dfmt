# DFMT one-command installer (Windows PowerShell 5.1+ / 7+).
# Usage:
#   iwr https://raw.githubusercontent.com/ersinkoc/dfmt/main/install.ps1 | iex
#
# Environment:
#   $env:DFMT_VERSION      pin release tag (default: latest)
#   $env:DFMT_INSTALL_DIR  override install dir (default: %LOCALAPPDATA%\Programs\dfmt)
#   $env:DFMT_FROM_SOURCE  = "1" to skip binary download, go install from source
#   $env:DFMT_DEBUG        = "1" for verbose output

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$RepoOwner = 'ersinkoc'
$RepoName  = 'dfmt'
$Tag       = if ($env:DFMT_VERSION) { $env:DFMT_VERSION } else { 'latest' }

function Write-Info($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Warn($msg) { Write-Host "warn: $msg" -ForegroundColor Yellow }
function Write-Err ($msg) { Write-Host "error: $msg" -ForegroundColor Red }

if ($env:DFMT_DEBUG -eq '1') { $VerbosePreference = 'Continue' }

# --- detect arch -----------------------------------------------------------
$procArch = $env:PROCESSOR_ARCHITECTURE
switch ($procArch) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    default { Write-Err "unsupported arch: $procArch"; exit 1 }
}
$asset = "dfmt-windows-$arch.exe"

# --- install dir -----------------------------------------------------------
if ($env:DFMT_INSTALL_DIR) {
    $installDir = $env:DFMT_INSTALL_DIR
} else {
    $installDir = Join-Path $env:LOCALAPPDATA 'Programs\dfmt'
}
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
$target = Join-Path $installDir 'dfmt.exe'

# --- base URL --------------------------------------------------------------
if ($Tag -eq 'latest') {
    $base = "https://github.com/$RepoOwner/$RepoName/releases/latest/download"
} else {
    $base = "https://github.com/$RepoOwner/$RepoName/releases/download/$Tag"
}

function Download-Binary {
    Write-Info "fetching $asset from release $Tag"
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("dfmt-" + [Guid]::NewGuid())
    New-Item -ItemType Directory -Force -Path $tmp | Out-Null
    try {
        $tmpExe  = Join-Path $tmp $asset
        $tmpSums = Join-Path $tmp 'sha256sums.txt'

        Invoke-WebRequest -Uri "$base/$asset" -OutFile $tmpExe -UseBasicParsing

        # optional checksum verify
        try {
            Invoke-WebRequest -Uri "$base/sha256sums.txt" -OutFile $tmpSums -UseBasicParsing -ErrorAction Stop
            $line = Get-Content $tmpSums | Where-Object { $_ -match ("\s" + [regex]::Escape($asset) + "$") } | Select-Object -First 1
            if ($line) {
                $expected = ($line -split '\s+')[0].ToLower()
                $actual = (Get-FileHash -Algorithm SHA256 $tmpExe).Hash.ToLower()
                if ($expected -ne $actual) {
                    throw "checksum mismatch (expected $expected, got $actual)"
                }
                Write-Info "sha256 verified"
            }
        } catch {
            Write-Warn "could not verify checksum: $_"
        }

        Move-Item -Force -Path $tmpExe -Destination $target -ErrorAction SilentlyContinue
        if (-not (Test-Path $target)) {
            # target might be locked; remove and retry
            try {
                Remove-Item -Force $target -ErrorAction Stop
                Start-Sleep -Milliseconds 100
                Move-Item -Path $tmpExe -Destination $target
            } catch {
                Write-Warn "could not replace locked file at $target"
                return $false
            }
        }
        Write-Info "installed to $target"
        return $true
    } catch {
        Write-Warn "binary download failed: $_"
        return $false
    } finally {
        Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $tmp
    }
}

function Install-FromSource {
    $goCmd = Get-Command go -ErrorAction SilentlyContinue
    if (-not $goCmd) {
        Write-Err "source fallback needs Go toolchain."
        Write-Host "Install Go first:"
        Write-Host "  winget install GoLang.Go"
        Write-Host "  # or download from https://go.dev/dl/"
        Write-Host "Then re-run this installer."
        exit 1
    }
    Write-Info "building from source via 'go install'"
    & go install "github.com/$RepoOwner/$RepoName/cmd/dfmt@latest"
    if ($LASTEXITCODE -ne 0) {
        Write-Err "go install failed"
        exit 1
    }
    $goBin = (& go env GOBIN) 2>$null
    if (-not $goBin) {
        $goPath = (& go env GOPATH) 2>$null
        if ($goPath) { $goBin = Join-Path $goPath 'bin' }
    }
    $src = Join-Path $goBin 'dfmt.exe'
    if ((Test-Path $src) -and ($src -ne $target)) {
        # Remove target if locked, then copy
        try {
            Remove-Item -Force $target -ErrorAction Stop
        } catch {
            Write-Warn "could not remove locked file at $target"
        }
        Copy-Item -Force -Path $src -Destination $target -ErrorAction SilentlyContinue
        if (Test-Path $target) {
            Write-Info "copied $src -> $target"
        } else {
            Write-Warn "could not copy to locked target"
        }
    }
}

# --- main ------------------------------------------------------------------
if ($env:DFMT_FROM_SOURCE -eq '1') {
    Install-FromSource
} else {
    if (-not (Download-Binary)) {
        Write-Warn "falling back to source build"
        Install-FromSource
    }
}

if (-not (Test-Path $target)) {
    Write-Err "dfmt.exe not found at $target after install"
    exit 1
}

# --- also install to ~/.dfmt/ so ResolveDFMTCommand finds it ----------
# This mirrors the Unix behaviour where ~/.dfmt/dfmt is the canonical
# path ResolveDFMTCommand resolves to. Without this the doctor check
# shows "stale" when the binary was installed elsewhere.
$userDfmtDir = Join-Path $env:USERPROFILE '.dfmt'
$userDfmtExe = Join-Path $userDfmtDir 'dfmt.exe'
try {
    New-Item -ItemType Directory -Force -Path $userDfmtDir | Out-Null
    Copy-Item -Force -Path $target -Destination $userDfmtExe -ErrorAction Stop
    Write-Info "also installed to $userDfmtExe"
} catch {
    Write-Warn "could not copy to $userDfmtExe (non-fatal): $_"
}

# --- ensure installDir is on user PATH -------------------------------------
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $userPath) { $userPath = '' }
$onPath = $false
foreach ($p in ($userPath -split ';')) {
    if ($p -and ($p.TrimEnd('\') -ieq $installDir.TrimEnd('\'))) { $onPath = $true; break }
}
if (-not $onPath) {
    $newUserPath = if ($userPath) { "$installDir;$userPath" } else { $installDir }
    [Environment]::SetEnvironmentVariable('Path', $newUserPath, 'User')
    $env:PATH = "$installDir;$env:PATH"
    Write-Info "added $installDir to user PATH (open a new shell for it to take effect everywhere)"
}

# --- dfmt setup ------------------------------------------------------------
Write-Info "running 'dfmt setup --force' to wire up detected agents"
try {
    & $target setup --force
    if ($LASTEXITCODE -ne 0) {
        Write-Warn "'dfmt setup' returned $LASTEXITCODE. You can re-run it later."
    }
} catch {
    Write-Warn "'dfmt setup' failed: $_"
}

# --- success banner --------------------------------------------------------
Write-Host ""
Write-Host "DFMT installed." -ForegroundColor Green
Write-Host ""
Write-Host "Next steps:"
Write-Host "  1.  cd C:\path\to\your\project"
Write-Host "  2.  dfmt quickstart      # init + per-agent setup + verify"
Write-Host "  3.  restart your AI agent (Claude Code, Cursor, VS Code, Codex,"
Write-Host "      Gemini, Windsurf, Zed, Continue, OpenCode -- whichever you use)"
Write-Host ""
Write-Host "Docs:  https://github.com/$RepoOwner/$RepoName"

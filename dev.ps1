# DFMT Development Script (Windows)
#
# Tertemiz reset + build + install + setup + smoke test.
# Default: nukes every dfmt artifact on the system (state, binaries, manifest,
# project state, git hooks, stale Claude Code Temp project entries) and
# reinstalls from source. This is what `.\dev.ps1` does by itself.
#
# Usage:
#   .\dev.ps1                  # nuke + build + install + setup + init + doctor
#   .\dev.ps1 -NoClean         # skip the wipe (just build/install/setup)
#   .\dev.ps1 -SkipSetup       # build/install only, skip agent registration
#   .\dev.ps1 -SkipInit        # skip `dfmt init` for this project
#   .\dev.ps1 -InstallHooks    # also install git hooks for this project
#   .\dev.ps1 -KeepClaude      # do NOT auto-kill running Claude Code processes
#   .\dev.ps1 -SkipMCPSmoke    # skip the post-build MCP initialize handshake
#   .\dev.ps1 -SkipDoctor      # skip the post-install `dfmt doctor` health check
#   .\dev.ps1 -SkipTests       # skip the `go test ./internal/transport/...` gate

[CmdletBinding()]
param(
    [switch]$NoClean,
    [switch]$SkipSetup,
    [switch]$SkipInit,
    [switch]$InstallHooks,
    [switch]$KeepClaude,
    [switch]$SkipMCPSmoke,
    [switch]$SkipDoctor,
    [switch]$SkipTests
)

$ErrorActionPreference = 'Stop'
$RepoRoot      = $PSScriptRoot
$BinaryName    = "dfmt.exe"
$TargetDir     = Join-Path $env:USERPROFILE ".dfmt"
$TargetPath    = Join-Path $TargetDir $BinaryName
$LegacyDir     = Join-Path $env:LOCALAPPDATA "Programs\dfmt"
$ManifestDir   = Join-Path $env:USERPROFILE ".local\share\dfmt"
$ProjectDfmt   = Join-Path $RepoRoot ".dfmt"
$DistDir       = Join-Path $RepoRoot "dist"
$ClaudeJson    = Join-Path $env:USERPROFILE ".claude.json"

function Write-Step($msg)  { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Info($msg)  { Write-Host "    $msg" -ForegroundColor DarkGray }
function Write-Ok($msg)    { Write-Host "    $msg" -ForegroundColor Green }
function Write-Warn($msg)  { Write-Host "    $msg" -ForegroundColor Yellow }
function Write-Err($msg)   { Write-Host "error: $msg" -ForegroundColor Red }

function Remove-IfExists($path) {
    if (-not $path) { return }
    if (-not (Test-Path -LiteralPath $path)) { return }
    try {
        Remove-Item -LiteralPath $path -Recurse -Force -ErrorAction Stop
        Write-Info "removed $path"
    } catch {
        Write-Warn "could not remove $path ($_)"
    }
}

# ---- 0. Preflight: Go must be available --------------------------------------
# Minimum patched version: go1.26.2. Earlier 1.26.x patches ship vulnerable
# crypto/x509 + crypto/tls (GO-2026-4866 / 4870 / 4946 / 4947). The build
# is allowed to proceed on older patches — we don't have a clean way to
# auto-upgrade — but the warning is loud enough that operators notice
# before shipping a binary embedding the vulnerable stdlib.
Write-Step "Checking Go toolchain..."
$go = Get-Command go -ErrorAction SilentlyContinue
if (-not $go) {
    Write-Err "Go not found in PATH. Install from https://go.dev/dl/ then re-run."
    exit 1
}
$goVersionLine = & go version
Write-Info "$($go.Source) ($goVersionLine)"

if ($goVersionLine -match 'go(\d+)\.(\d+)(?:\.(\d+))?') {
    $major = [int]$Matches[1]
    $minor = [int]$Matches[2]
    $patch = if ($Matches[3]) { [int]$Matches[3] } else { 0 }
    $tooOld = ($major -lt 1) -or
              ($major -eq 1 -and $minor -lt 26) -or
              ($major -eq 1 -and $minor -eq 26 -and $patch -lt 2)
    if ($tooOld) {
        Write-Warn "Go $major.$minor.$patch is older than 1.26.2 - stdlib CVEs"
        Write-Warn "  GO-2026-4866 / 4870 / 4946 / 4947 (crypto/x509, crypto/tls)"
        Write-Warn "  remain unpatched in this build. Upgrade: https://go.dev/dl/"
    }
} else {
    Write-Warn "could not parse Go version from: $goVersionLine"
}

# ---- 1. Stop running dfmt processes (and optionally Claude Code) -------------
# Claude Code spawns `dfmt mcp` as a stdio subprocess and will respawn it
# almost immediately if we kill only the child. Stopping the parent first
# breaks that respawn loop and ensures the next Claude launch picks up the
# freshly-written MCP config and freshly-built binary.
$claudeWasKilled = $false
if (-not $KeepClaude) {
    Write-Step "Stopping running Claude Code processes..."
    $claudeProcs = @(Get-Process -Name "Claude","claude","Claude Code" -ErrorAction SilentlyContinue)
    if ($claudeProcs) {
        $interactive = [Environment]::GetEnvironmentVariable("CI") -ne "true" -and
                       [Console]::IsInputRedirected -eq $false
        if ($interactive) {
            Write-Host "    WARNING: $($claudeProcs.Count) Claude Code process(es) will be terminated." -ForegroundColor Yellow
            $confirm = Read-Host "    Kill Claude Code processes? [y/N]"
            if ($confirm -ne 'y' -and $confirm -ne 'Y') {
                Write-Info "aborted -- relaunch dev.ps1 with -KeepClaude to skip this step"
                exit 0
            }
        } else {
            Write-Warn "interactive input not available; sending termination signal to $($claudeProcs.Count) Claude Code process(es)"
        }
        Write-Info "stopping $($claudeProcs.Count) Claude process(es)"
        $claudeProcs | Stop-Process -Force -ErrorAction SilentlyContinue
        $claudeWasKilled = $true
        Start-Sleep -Milliseconds 600
    } else {
        Write-Info "no Claude Code processes running"
    }
} else {
    Write-Info "skipping Claude Code stop (-KeepClaude)"
}

Write-Step "Stopping running dfmt processes..."
$procs = @(Get-Process dfmt -ErrorAction SilentlyContinue)
if ($procs) {
    Write-Info "stopping $($procs.Count) process(es)"
    $procs | Stop-Process -Force
    Start-Sleep -Milliseconds 800
}
$lingering = Get-CimInstance Win32_Process -Filter "Name='dfmt.exe'" -ErrorAction SilentlyContinue
if ($lingering) {
    foreach ($p in $lingering) {
        Write-Info "killing PID $($p.ProcessId): $($p.CommandLine)"
        Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue
    }
    Start-Sleep -Milliseconds 400
}

# ---- 1.5. NUKE every prior dfmt artifact (default; -NoClean opts out) --------
# Order: kill processes first (already done), then wipe state. We deliberately
# delete the binary too — the build step in §3 lays down a fresh one.
if (-not $NoClean) {
    Write-Step "Wiping prior dfmt state..."

    # Project-level state.
    Remove-IfExists $ProjectDfmt
    Remove-IfExists $DistDir

    # User-level state at ~/.dfmt/. Going forward this dir holds ONLY
    # `dfmt.exe` and `daemons.json` (the global daemon registry) — every
    # other artefact (journal, index, config, port, lock, pid) is project-
    # local under <project>/.dfmt/. The remaining Remove-IfExists calls
    # below clean up legacy pollution from pre-fix installs where
    # `dfmt mcp` fell back to cwd-as-project (typically ~) and scattered
    # project-local files into the user home. They can be deleted once
    # all developer machines have cycled through this script at least
    # once on the post-fix build.
    Remove-IfExists $TargetPath
    Remove-IfExists (Join-Path $TargetDir 'daemons.json')
    # Legacy: pre-fix runMCP wrote these into ~/.dfmt/ when launched
    # outside any project. Post-fix runMCP runs in degraded mode instead.
    Remove-IfExists (Join-Path $TargetDir 'config.yaml')
    Remove-IfExists (Join-Path $TargetDir 'journal.jsonl')
    Remove-IfExists (Join-Path $TargetDir 'index.gob')
    Remove-IfExists (Join-Path $TargetDir 'index.cursor')
    Remove-IfExists (Join-Path $TargetDir 'port')
    Remove-IfExists (Join-Path $TargetDir 'lock')
    Remove-IfExists (Join-Path $TargetDir 'daemon.pid')

    # Setup manifest (records every agent file dfmt setup wrote).
    Remove-IfExists $ManifestDir

    # Legacy LOCALAPPDATA installer dir.
    Remove-IfExists $LegacyDir

    # GOPATH/bin/dfmt.exe — `go install` may have left one shadowing $TargetDir.
    try {
        $goPath = (& go env GOPATH 2>$null)
        if ($LASTEXITCODE -eq 0 -and $goPath) {
            Remove-IfExists (Join-Path $goPath.Trim() 'bin\dfmt.exe')
        }
    } catch { }

    # Anywhere else PATH still resolves dfmt.exe.
    try {
        $whereOutput = & where.exe dfmt.exe 2>$null
        if ($LASTEXITCODE -eq 0 -and $whereOutput) {
            foreach ($hit in $whereOutput) {
                if ($hit) { Remove-IfExists $hit.Trim() }
            }
        }
    } catch { }

    # Project git hooks installed by `dfmt install-hooks`.
    foreach ($hook in @('post-commit','post-checkout','pre-push')) {
        $hookPath = Join-Path $RepoRoot ".git\hooks\$hook"
        if (Test-Path -LiteralPath $hookPath) {
            $contents = Get-Content -LiteralPath $hookPath -Raw -ErrorAction SilentlyContinue
            if ($contents -match 'dfmt') {
                Remove-IfExists $hookPath
            }
        }
    }

    # Stale Claude Code project entries pointing at scratch dirs (dfmt-test*,
    # dfmt-final, etc) — these accumulate over repeated dev cycles and clutter
    # /mcp output.  The raw JSON may contain D:/CODEBOX and D:/Codebox variants
    # of the same logical path as separate keys; PowerShell's JSON deserializer
    # throws on duplicate keys (last-write-wins semantics don't apply during
    # deserialization).  We scrub duplicates at the raw-text level before
    # passing to ConvertFrom-Json.
    if (Test-Path -LiteralPath $ClaudeJson) {
        try {
            $raw = [System.IO.File]::ReadAllText($ClaudeJson)
            # Normalize any path that differs only by Windows case to lowercase
            # "codebox".  This merges D:/CODEBOX and D:/Codebox into one entry.
            # Regex: match "D:/CODEBOX/..." or "D:/Codebox/..." and collapse
            # the variant to the lowercase form, keeping only the first match.
            $normalized = [regex]::Replace($raw,
                '(?="D:/)(D:/)[Cc][Oo][Dd][Ee][Bb][Oo][Xx](/PROJECTS/DFMT[^"]*")',
                '$1codebox$2',
                'Compiled')
            $cj = $normalized | ConvertFrom-Json -ErrorAction Stop
            if ($cj.PSObject.Properties['projects']) {
                $removed = 0
                $projNames = @($cj.projects.PSObject.Properties.Name)
                foreach ($name in $projNames) {
                    if ($name -match '[\\/]Temp[\\/]dfmt[-_]') {
                        $cj.projects.PSObject.Properties.Remove($name) | Out-Null
                        $removed++
                    }
                }
                if ($removed -gt 0) {
                    $backup = "$ClaudeJson.dfmt.preclean.bak"
                    if (-not (Test-Path -LiteralPath $backup)) {
                        Copy-Item -LiteralPath $ClaudeJson -Destination $backup -Force
                        Write-Info "backed up ~/.claude.json -> $backup"
                    }
                    $newJson = $cj | ConvertTo-Json -Depth 100
                    [System.IO.File]::WriteAllText($ClaudeJson, ($newJson -replace "`r`n","`n"))
                    Write-Info "removed $removed stale dfmt-* Temp project entries from ~/.claude.json"
                }
            }
        } catch {
            Write-Warn "could not clean ~/.claude.json projects ($_)"
        }
    }

    Write-Ok "wipe complete"
} else {
    Write-Info "skipping wipe (-NoClean)"
}

# ---- 2. Run the gate tests before anyone gets a binary -----------------------
# Four packages, picked because every regression class they catch surfaces
# only at runtime (not at build time):
#   transport — MCP initialize handshake + CallToolResult envelope shape
#   sandbox   — default-Lang fallback, runtime probing, exec env (PATH
#               prepend) so a broken path_prepend doesn't ship silently
#   config    — yaml round-trip of new fields (e.g. exec.path_prepend)
#               and the Validate() rejection list
#   cli       — dispatch wiring + doctor checks (sandbox toolchain probe,
#               instruction-block staleness) so an agent UX regression
#               trips the build, not the user
if (-not $SkipTests) {
    Write-Step "Running transport + sandbox + config + cli tests (gate)..."
    Set-Location $RepoRoot
    & go test ./internal/transport/... ./internal/sandbox/... ./internal/config/... ./internal/cli/...
    if ($LASTEXITCODE -ne 0) {
        Write-Err "tests failed -- aborting before install"
        exit 1
    }
    Write-Ok "transport + sandbox + config + cli tests pass"
} else {
    Write-Info "skipping tests (-SkipTests)"
}

# ---- 3. Build directly to install target -------------------------------------
Write-Step "Building dfmt to $TargetPath..."
Set-Location $RepoRoot
New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null
$gitRev = "dev"
try {
    $gitRev = (& git rev-parse --short HEAD 2>$null).Trim()
    if (-not $gitRev) { $gitRev = "dev" }
} catch { $gitRev = "dev" }
$ldflags = "-X github.com/ersinkoc/dfmt/internal/version.Current=v0.3.1"
& go build -ldflags $ldflags -o $TargetPath ./cmd/dfmt
if ($LASTEXITCODE -ne 0 -or -not (Test-Path $TargetPath)) {
    Write-Err "build failed"
    exit 1
}
Write-Ok "built $TargetPath"

# ---- 4. PATH (user scope): drop legacy entry, ensure $TargetDir present ------
Write-Step "Updating user PATH..."
$userPath = [Environment]::GetEnvironmentVariable("Path", [EnvironmentVariableTarget]::User)
$pathParts = @()
if ($userPath) {
    $pathParts = $userPath -split ';' | Where-Object { $_ -ne '' }
}
$pathParts = @($pathParts | Where-Object {
    -not [string]::Equals($_.TrimEnd('\'), $LegacyDir.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)
})
$alreadyInPath = $false
foreach ($p in $pathParts) {
    if ([string]::Equals($p.TrimEnd('\'), $TargetDir.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)) {
        $alreadyInPath = $true
        break
    }
}
if (-not $alreadyInPath) {
    $pathParts += $TargetDir
}
$newUserPath = ($pathParts -join ';')
if ($newUserPath -ne $userPath) {
    [Environment]::SetEnvironmentVariable("Path", $newUserPath, [EnvironmentVariableTarget]::User)
    Write-Ok "user PATH updated"
} else {
    Write-Info "user PATH already correct"
}
# Refresh PATH in the current PowerShell session so subsequent calls in this
# script (and any shell launched from it) can resolve `dfmt` immediately,
# and so the legacy dir is gone from this session too.
$sessionParts = @(($env:Path -split ';') | Where-Object { $_ -ne '' } | Where-Object {
    -not [string]::Equals($_.TrimEnd('\'), $LegacyDir.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)
})
$inSession = $false
foreach ($p in $sessionParts) {
    if ([string]::Equals($p.TrimEnd('\'), $TargetDir.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)) {
        $inSession = $true; break
    }
}
if (-not $inSession) {
    $sessionParts += $TargetDir
}
$env:Path = ($sessionParts -join ';')

# ---- 5. Verify the installed binary actually runs ----------------------------
Write-Step "Verifying installed binary..."
& $TargetPath --version
if ($LASTEXITCODE -ne 0) {
    Write-Err "installed dfmt failed to run (exit $LASTEXITCODE)"
    exit 1
}

# ---- 6. Configure detected agents (MCP server registration) ------------------
if (-not $SkipSetup) {
    Write-Step "Registering dfmt MCP server with detected agents..."
    & $TargetPath setup --force
    if ($LASTEXITCODE -ne 0) {
        Write-Err "dfmt setup failed (exit $LASTEXITCODE)"
        exit 1
    }

    Write-Step "Verifying setup wrote expected files..."
    & $TargetPath setup --verify
    if ($LASTEXITCODE -ne 0) {
        Write-Err "setup verification failed -- some agent config files are missing"
        exit 1
    }

    # ---- 6b. Verify ~/.claude.json mcpServers.dfmt.command is the absolute -----
    # path we just installed. `dfmt setup` *should* write the absolute path via
    # ResolveDFMTCommand(), but older installs may have left a literal "dfmt"
    # there which silently fails to launch when PATH doesn't have $TargetDir
    # at Claude Code startup time. Patch defensively.
    Write-Step "Checking ~/.claude.json mcpServers.dfmt entry..."
    if (-not (Test-Path $ClaudeJson)) {
        Write-Warn "~/.claude.json not found -- Claude Code may not be installed; skipping patch"
    } else {
        try {
            $raw = [System.IO.File]::ReadAllText($ClaudeJson) -replace 'D:/CODEBOX/PROJECTS/DFMT', 'D:/codebox/PROJECTS/DFMT'
            $claudeJson = $raw | ConvertFrom-Json -ErrorAction Stop
        } catch {
            Write-Err "could not parse ~/.claude.json: $_"
            exit 1
        }

        if (-not $claudeJson.PSObject.Properties['mcpServers']) {
            $claudeJson | Add-Member -NotePropertyName mcpServers -NotePropertyValue ([pscustomobject]@{}) -Force
        }
        $existingDfmt = $null
        if ($claudeJson.mcpServers.PSObject.Properties['dfmt']) {
            $existingDfmt = $claudeJson.mcpServers.dfmt
        }

        $needsPatch = $false
        if (-not $existingDfmt) {
            $needsPatch = $true
            Write-Info "no mcpServers.dfmt entry -- will create"
        } else {
            $currentCmd = [string]$existingDfmt.command
            if (-not [string]::Equals($currentCmd, $TargetPath, [StringComparison]::OrdinalIgnoreCase)) {
                $needsPatch = $true
                Write-Info "command was '$currentCmd' -- repointing to '$TargetPath'"
            }
            if (($existingDfmt.args -join ',') -ne 'mcp') {
                $needsPatch = $true
                Write-Info "args were '$($existingDfmt.args -join ',')' -- resetting to 'mcp'"
            }
            if (-not $existingDfmt.PSObject.Properties['type'] -or $existingDfmt.type -ne 'stdio') {
                $needsPatch = $true
            }
        }

        if ($needsPatch) {
            $backupPath = "$ClaudeJson.dfmt.bak"
            if (-not (Test-Path $backupPath)) {
                Copy-Item -LiteralPath $ClaudeJson -Destination $backupPath -Force
                Write-Info "backup written to $backupPath"
            }
            $newDfmtEntry = [pscustomobject]@{
                type    = 'stdio'
                command = $TargetPath
                args    = @('mcp')
                env     = [pscustomobject]@{}
            }
            if ($claudeJson.mcpServers.PSObject.Properties['dfmt']) {
                $claudeJson.mcpServers.dfmt = $newDfmtEntry
            } else {
                $claudeJson.mcpServers | Add-Member -NotePropertyName dfmt -NotePropertyValue $newDfmtEntry -Force
            }
            # Depth must be high -- ~/.claude.json has deeply nested project blocks.
            $newJson = $claudeJson | ConvertTo-Json -Depth 100
            # Preserve LF newlines on disk (Claude Code writes LF on all platforms).
            [System.IO.File]::WriteAllText($ClaudeJson, ($newJson -replace "`r`n","`n"))
            Write-Ok "patched mcpServers.dfmt.command to $TargetPath"
        } else {
            Write-Ok "mcpServers.dfmt.command already pinned to $TargetPath"
        }
    }

    # ---- 6c. MCP smoke test: handshake + tools/call envelope check -----------
    # Two regression gates here:
    #   (1) initialize MUST advertise capabilities.tools, else Claude Code
    #       skips tools/list and every dfmt_* tool silently disappears.
    #   (2) tools/call MUST return a CallToolResult envelope (result.content
    #       is a content-block array). Returning a bare handler struct made
    #       Claude Code reject responses with "expected array, received string"
    #       because ReadResponse.Content (a string) collided with the
    #       CallToolResult.content slot.
    if (-not $SkipMCPSmoke) {
        Write-Step "Running MCP smoke test (initialize + tools/call)..."
        # Three messages on stdin: initialize (id=1), the initialized
        # notification (no id, no response), then a tools/call (id=2) that
        # exercises the CallToolResult wrapper. dfmt_stats is the cheapest
        # tool — no required args, no project state needed.
        $smokeReq = @(
            '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"dev.ps1-smoke","version":"0"}}}',
            '{"jsonrpc":"2.0","method":"notifications/initialized"}',
            '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dfmt_stats","arguments":{}}}'
        ) -join "`n"
        # Run in a throwaway temp dir so dfmt mcp's auto-init drops its
        # .dfmt/ scratch state into a place we delete afterwards — keeps
        # the smoke test from touching $RepoRoot or $env:USERPROFILE.
        $smokeWd = Join-Path $env:TEMP ("dfmt-smoke-" + [guid]::NewGuid().ToString("N"))
        New-Item -ItemType Directory -Force -Path $smokeWd | Out-Null
        $smokeJob = Start-Job -ScriptBlock {
            param($exe, $payload, $wd)
            Set-Location -LiteralPath $wd
            $payload | & $exe mcp 2>&1
        } -ArgumentList $TargetPath, $smokeReq, $smokeWd
        $finished = Wait-Job -Job $smokeJob -Timeout 15
        if (-not $finished) {
            Stop-Job -Job $smokeJob -ErrorAction SilentlyContinue
            Remove-Job -Job $smokeJob -Force -ErrorAction SilentlyContinue
            Write-Err "MCP smoke test timed out after 15s -- dfmt mcp did not respond"
            exit 1
        }
        $smokeOut = Receive-Job -Job $smokeJob
        Remove-Job -Job $smokeJob -ErrorAction SilentlyContinue
        # Each line beginning with '{' is a JSON-RPC reply. Parse them all and
        # match on id so we can assert against the right response.
        $jsonLines = $smokeOut | Where-Object { $_ -match '^\s*\{' }
        if (-not $jsonLines) {
            Write-Err "MCP smoke test produced no JSON responses. Raw output:"
            $smokeOut | ForEach-Object { Write-Host "      $_" -ForegroundColor DarkRed }
            exit 1
        }
        $initResp = $null
        $callResp = $null
        foreach ($line in $jsonLines) {
            try {
                $parsed = $line | ConvertFrom-Json -ErrorAction Stop
            } catch { continue }
            if ($parsed.id -eq 1) { $initResp = $parsed }
            elseif ($parsed.id -eq 2) { $callResp = $parsed }
        }

        # ---- gate (1): initialize handshake ----
        if (-not $initResp) {
            Write-Err "MCP smoke: no response with id=1 (initialize). Lines: $jsonLines"
            exit 1
        }
        if ($initResp.error) {
            Write-Err "MCP initialize error: $($initResp.error | ConvertTo-Json -Compress)"
            exit 1
        }
        $serverName = $initResp.result.serverInfo.name
        $serverVer  = $initResp.result.serverInfo.version
        if (-not $serverName) {
            Write-Err "MCP initialize response missing result.serverInfo.name"
            exit 1
        }
        $hasToolsCap = $false
        if ($initResp.result.capabilities -and $initResp.result.capabilities.PSObject.Properties['tools']) {
            $hasToolsCap = $true
        }
        if (-not $hasToolsCap) {
            Write-Err "MCP initialize response is MISSING capabilities.tools -- Claude Code will not load any dfmt tools."
            exit 1
        }
        Write-Ok "MCP handshake OK -- serverInfo.name='$serverName' version='$serverVer', capabilities.tools present"

        # ---- gate (2): tools/call CallToolResult envelope ----
        if (-not $callResp) {
            Write-Err "MCP smoke: no response with id=2 (tools/call). Server may have crashed after initialize."
            $smokeOut | ForEach-Object { Write-Host "      $_" -ForegroundColor DarkRed }
            exit 1
        }
        if ($callResp.error) {
            Write-Err "MCP tools/call returned error: $($callResp.error | ConvertTo-Json -Compress)"
            exit 1
        }
        # result.content MUST be an array of content blocks. If the wrapper
        # regressed, Claude Code would reject this with the "expected array,
        # received string" schema error -- we mirror that check here so the
        # build fails before the user ever sees it.
        $callContent = $callResp.result.content
        if ($null -eq $callContent) {
            Write-Err "MCP tools/call result is MISSING the 'content' array. Got: $($callResp.result | ConvertTo-Json -Compress -Depth 5)"
            exit 1
        }
        # PowerShell unwraps single-element arrays from ConvertFrom-Json into
        # a scalar; force-wrap before counting so a one-block reply still
        # passes the array-shape gate.
        $callContent = @($callContent)
        if ($callContent.Count -lt 1) {
            Write-Err "MCP tools/call result.content is empty -- expected at least one block"
            exit 1
        }
        $firstBlock = $callContent[0]
        if (-not $firstBlock.type -or $firstBlock.type -ne 'text') {
            Write-Err "MCP tools/call result.content[0].type='$($firstBlock.type)' -- expected 'text'"
            exit 1
        }
        Write-Ok "MCP tools/call envelope OK -- result.content[0].type='text', $($callContent.Count) block(s)"

        # Clean up the throwaway smoke working directory.
        if (Test-Path -LiteralPath $smokeWd) {
            Remove-Item -LiteralPath $smokeWd -Recurse -Force -ErrorAction SilentlyContinue
        }
    } else {
        Write-Info 'skipping MCP smoke test (-SkipMCPSmoke)'
    }
} else {
    Write-Info 'skipping setup (-SkipSetup)'
}

# ---- 7. Initialize the current project ---------------------------------------
if (-not $SkipInit) {
    Write-Step "Initializing project at $RepoRoot..."
    Set-Location $RepoRoot
    & $TargetPath init
    if ($LASTEXITCODE -ne 0) {
        Write-Err "dfmt init failed (exit $LASTEXITCODE)"
        exit 1
    }
} else {
    Write-Info 'skipping init (-SkipInit)'
}

# ---- 8. Optional: install git hooks ------------------------------------------
if ($InstallHooks) {
    Write-Step "Installing git hooks..."
    Set-Location $RepoRoot
    & $TargetPath install-hooks
    if ($LASTEXITCODE -ne 0) {
        Write-Warn "install-hooks failed (exit $LASTEXITCODE) -- non-fatal, continuing"
    }
}

# ---- 9. Show daemon status (sanity check) ------------------------------------
Write-Step "Status:"
& $TargetPath status

# ---- 10. Doctor: per-agent wire-up + command-path verification ---------------
# This is the canonical post-install sanity check. It exercises three things
# the earlier steps cannot:
#   (a) every detected agent has its manifest-tracked file on disk;
#   (b) every agent's mcpServers.dfmt.command equals $TargetPath (catches
#       stale paths from a prior install in a different location);
#   (c) the dfmt binary at $TargetPath is itself stat-able.
# Non-fatal: doctor failures are warnings, not aborts. The earlier steps
# already gate the build; doctor's job is to surface drift, and the operator
# can choose how to react. Skip with -SkipDoctor for fast iteration.
if (-not $SkipDoctor) {
    Write-Step "Running doctor (project state + per-agent wire-up + command path)..."
    & $TargetPath doctor
    $doctorExit = $LASTEXITCODE
    if ($doctorExit -ne 0) {
        Write-Warn "doctor reported issues (exit $doctorExit) -- review the lines marked with X above"
    } else {
        Write-Ok "all health checks passed"
    }
} else {
    Write-Info 'skipping doctor (-SkipDoctor)'
}

# ---- 11. Restart-Claude advisory ---------------------------------------------
$claudeStillRunning = Get-Process -Name "Claude","claude","Claude Code" -ErrorAction SilentlyContinue
Write-Host ""
Write-Host "==> Install complete." -ForegroundColor Green
Write-Host "    dfmt.exe        : $TargetPath" -ForegroundColor White
Write-Host "    MCP command     : pinned (absolute path, PATH-independent)" -ForegroundColor White
Write-Host ""
if ($claudeStillRunning) {
    # -KeepClaude was passed, or new Claude windows started during the script.
    Write-Host "    Claude Code is still running. CLOSE IT COMPLETELY and reopen --" -ForegroundColor Yellow
    Write-Host "    MCP servers are loaded only at startup. After reopening," -ForegroundColor Yellow
    Write-Host "    dfmt_exec / dfmt_read / dfmt_fetch will be available." -ForegroundColor Yellow
} elseif ($claudeWasKilled) {
    Write-Host "    Claude Code was stopped. RELAUNCH IT now -- the freshly" -ForegroundColor Green
    Write-Host "    installed dfmt MCP server will load on startup." -ForegroundColor Green
    Write-Host "    Confirm in the new session with: /mcp" -ForegroundColor Green
} else {
    Write-Host "    Open Claude Code -- dfmt_exec / dfmt_read / dfmt_fetch will be" -ForegroundColor White
    Write-Host "    available immediately. Use /mcp to confirm the connection." -ForegroundColor White
}
Write-Host ""
Write-Host "    Health check any time:    dfmt doctor              # project + per-agent wire-up" -ForegroundColor White
Write-Host "    Setup files only:         dfmt setup --verify     # presence-only check" -ForegroundColor White
Write-Host "    Daemon/project status:    dfmt status" -ForegroundColor White

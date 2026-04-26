# Test dfmt MCP server: send initialize + initialized + tools/list, print response.
# Run: .\scripts\test-mcp.ps1

$ErrorActionPreference = 'Stop'

$msgs = @(
    '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}'
    '{"jsonrpc":"2.0","method":"notifications/initialized"}'
    '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
) -join "`n"

$tmp = New-TemporaryFile
try {
    # Write LF-only (no CRLF) so dfmt's line reader sees clean newlines.
    [IO.File]::WriteAllText($tmp.FullName, $msgs + "`n")
    Write-Host "==> sending 3 messages to dfmt mcp..." -ForegroundColor Cyan
    Get-Content -Raw $tmp.FullName | dfmt mcp
} finally {
    Remove-Item $tmp.FullName -ErrorAction SilentlyContinue
}

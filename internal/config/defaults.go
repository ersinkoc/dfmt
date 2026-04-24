package config

// DefaultConfigYAML returns the default config.yaml template used by
// both "dfmt init" and auto-initialization.
func DefaultConfigYAML() string {
	return `# DFMT Configuration
version: 1

capture:
  mcp:
    enabled: true
  fs:
    # fs capture is opt-in; flip to true to enable the filesystem watcher.
    enabled: false
    watch:
      - "**"
    ignore:
      - ".git/**"
      - "node_modules/**"

storage:
  durability: batched
  journal_max_bytes: 10485760
`
}

package config

// DefaultConfigYAML returns the default config.yaml template used by
// both "dfmt init" and auto-initialization.
//
// The fswatch ignore list MUST include ".dfmt/**". Without it, every event
// the daemon journals into .dfmt/journal.jsonl triggers a filesystem event
// the watcher feeds back into the journal — a self-amplifying loop that
// fills the journal with its own writes within seconds of enabling fs
// capture. The other entries are tuned for typical project noise: build
// artifacts, dependency caches, IDE state, and compiler outputs that
// produce thousands of events per second during a normal workday.
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
      # dfmt's own state: WITHOUT this, the watcher sees every journal
      # append it wrote and feeds another event back in — infinite loop.
      - ".dfmt/**"
      # Version control & dependencies
      - ".git/**"
      - "node_modules/**"
      - "vendor/**"
      - ".venv/**"
      - "venv/**"
      - "__pycache__/**"
      - "*.pyc"
      # Build / output directories
      - "dist/**"
      - "build/**"
      - "target/**"
      - "out/**"
      - ".next/**"
      - ".nuxt/**"
      - ".turbo/**"
      - "coverage/**"
      # Editor / IDE state
      - ".idea/**"
      - ".vscode/**"
      - "*.swp"
      - "*.swo"
      # Logs & temp
      - "*.log"
      - "tmp/**"

storage:
  durability: batched
  journal_max_bytes: 10485760

# Optional: directories to prepend to the sandbox's PATH for every exec
# call. Use this when the daemon was auto-started from a shell that does
# not see your language toolchains, so dfmt_exec returns exit 127 for go
# / node / python. Run "dfmt doctor" — it probes common locations and
# prints a ready-to-paste block here. Each entry must be an absolute
# path.
# exec:
#   path_prepend:
#     - "C:/Program Files/Go/bin"
#     - "C:/Program Files/nodejs"
`
}

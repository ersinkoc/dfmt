# Contributing to DFMT

Thank you for your interest in contributing to DFMT. This document outlines the development workflow, test requirements, and architectural decision-making process.

## Development Workflow

### Prerequisites

- Go 1.25 or later (matches `go.mod`)
- Make (for running build targets)
- Git
- `golangci-lint` if you intend to run `make lint`

### Setup

1. Clone the repository:
   ```bash
   git clone https://github.com/ersinkoc/dfmt.git
   cd dfmt
   ```

2. Install dependencies:
   ```bash
   go mod download
   ```

3. Build the project:
   ```bash
   make build
   ```

4. Run tests:
   ```bash
   make test
   ```

### Project Structure

```
dfmt/
├── cmd/
│   ├── dfmt/          # CLI binary entry point
│   └── dfmt-bench/    # Token-savings benchmark binary
├── internal/
│   ├── capture/       # FS watcher + git hooks + shell integration
│   ├── cli/           # CLI command implementations + dispatch
│   ├── client/        # CLI ↔ daemon RPC client
│   ├── config/        # YAML configuration loading
│   ├── content/       # Ephemeral content store for raw tool output
│   ├── core/          # Events, journal, BM25 index, classifier
│   ├── daemon/        # Daemon lifecycle + lockfile + idle exit
│   ├── logging/       # Internal logging
│   ├── project/       # Project discovery + per-user registry
│   ├── redact/        # Secret redaction (AWS, GitHub, Anthropic, …)
│   ├── retrieve/      # Recall-snapshot building + markdown rendering
│   ├── safefs/        # Symlink-safe atomic write helper
│   ├── sandbox/       # exec/read/fetch/glob/grep/edit/write policy gate
│   ├── setup/         # Agent auto-detection + MCP config writers
│   └── transport/     # MCP stdio + JSON-RPC HTTP + Unix socket
├── docs/
│   ├── ARCHITECTURE.md  # System architecture overview
│   └── adr/             # Architecture Decision Records
├── install.sh           # POSIX one-line installer
├── install.ps1          # Windows one-line installer
├── dev.ps1              # Developer reset + build + install + smoke test
├── AGENTS.md            # Canonical agent-onboarding doc
├── CLAUDE.md            # Pointer to AGENTS.md (Claude Code's filename)
├── CONTRIBUTING.md      # This file
└── README.md            # Project entry point
```

### Making Changes

1. **Fork** the repository and create a feature branch:
   ```bash
   git checkout -b feature/your-feature-name
   ```

2. **Make your changes** following the coding standards:
   - Run `make lint` to check code style
   - Run `make fmt` to format code

3. **Write tests** for new functionality (see Test Requirements below)

4. **Ensure all tests pass**:
   ```bash
   make test
   ```

5. **Commit your changes** using descriptive commit messages:
   ```bash
   git commit -m "description of changes"
   ```

6. **Push and create a Pull Request** on GitHub

## Test Requirements

### Policy

- All new functionality **must** include tests
- Bug fixes **must** include a regression test
- Test coverage must not decrease below the current threshold

### Running Tests

```bash
# Run all tests
make test

# Run tests with coverage (raw `go test` flags)
go test -cover ./...

# Run specific package tests
go test ./internal/core/...

# Run tests with race detector (Linux/macOS)
go test -race ./...
# On Windows the race detector needs cgo:
CGO_ENABLED=1 go test -race ./...
```

### Test Structure

Tests are co-located with the code they test:

```
internal/core/
├── events.go       # Implementation
├── events_test.go  # Tests
```

### Coverage Report

Generate an HTML coverage report:

```bash
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html
# Open coverage.html in your browser
```

Target coverage by package:
- `internal/core`: 90%+
- `internal/transport`: 85%+
- `internal/daemon`: 80%+
- `internal/cli`: 75%+

## Architecture Decision Records (ADR)

### Why ADRs?

DFMT uses ADRs to document significant architectural decisions. This helps:
- Track the reasoning behind design choices
- Onboard contributors to the project
- Avoid repeating past mistakes
- Maintain consistency across the codebase

### When to Create an ADR?

Create an ADR when your change:
- Adds a new architectural component
- Changes how components interact
- Adopts a new external library or technology
- Modifies existing behavior in a breaking way
- Affects the dependency policy

### ADR Process

1. **Propose**: Create a new ADR in `docs/adr/` with the next sequential number
2. **Template**: Use `0000-adr-process.md` as the template
3. **Review**: Open a PR for discussion
4. **Decide**: Merge when consensus is reached
5. **Implement**: Changes can proceed after ADR merge

### ADR Index

See [ADR-INDEX.md](docs/adr/ADR-INDEX.md) for a list of all ADRs and their status.

### Key ADRs

- [ADR-0001: Per-Project Daemon](docs/adr/0001-per-project-daemon.md) — Why one daemon per project
- [ADR-0004: Stdlib-Only Dependencies](docs/adr/0004-stdlib-only-deps.md) — Dependency philosophy
- [ADR-0006: Sandbox Scope](docs/adr/0006-sandbox-scope.md) — Sandboxed tool execution design
- [ADR-0007: Content Store Separation](docs/adr/0007-content-store-separation.md) — Ephemeral vs durable storage

## Dependency Policy

DFMT follows a **stdlib-first** policy. See [ADR-0004](docs/adr/0004-stdlib-only-deps.md) for the full rationale.

### Adding Dependencies

Adding a new dependency requires:
1. An ADR explaining why the stdlib cannot fulfill the requirement
2. Justification of the library's stability and maintenance status
3. Security review of the dependency

### Prohibited

- No SQLite or other database drivers
- No ORMs
- No web frameworks (Echo, Gin, Fiber, etc.)
- No CLI frameworks (Cobra, Kingpin, etc.)
- No logging frameworks (Zap, Logrus, etc.)

### Permitted

- Standard library (`net`, `http`, `os`, `io`, etc.)
- `golang.org/x/sys` — syscalls not in stdlib
- `gopkg.in/yaml.v3` — YAML configuration

That's it. The runtime tree currently has exactly two third-party
modules (`go.mod` is the source of truth). `golang.org/x/crypto`,
`golang.org/x/text`, and similar were considered but are not used —
proposing them requires an ADR.

### Bundled Libraries

Some functionality is bundled (no external import):

- HTML parser
- BM25 implementation
- Porter stemmer
- MCP protocol
- JSON-RPC 2.0

These are vendored directly to avoid version conflicts and ensure long-term stability.

## Code Style

### Go Standards

Follow [Effective Go](https://go.dev/doc/effective_go) and the [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments) style guide.

### Specific Conventions

1. **Error handling**: Return errors, don't log and return
2. **Context**: Use `context.Context` for cancellation and timeouts
3. **Interfaces**: Define interfaces where needed, not prematurely
4. **Mutex**: Use `sync.Mutex` for simple mutual exclusion
5. **Slices**: Preallocate when size is known

### Linting

DFMT uses `golangci-lint`. Run before committing:

```bash
make lint
```

The lint configuration is in `.golangci.yml`.

## Reporting Issues

### Bug Reports

Include:
- DFMT version (`dfmt --version`)
- OS and architecture
- Steps to reproduce
- Expected vs actual behavior
- Relevant log output

Useful debugging commands when filing an issue:

```bash
dfmt --version       # version + build SHA
dfmt doctor          # project state + per-agent wire-up
dfmt stats           # event counts and byte savings
dfmt config          # active configuration
```

Paste the relevant output into the issue body.

### Feature Requests

- Describe the problem you're solving
- Explain why existing functionality is insufficient
- Provide concrete use cases
- Consider backward compatibility impact

## License

By contributing to DFMT, you agree that your contributions will be licensed under the MIT License.

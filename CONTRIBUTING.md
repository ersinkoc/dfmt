# Contributing to DFMT

Thank you for your interest in contributing to DFMT. This document outlines the development workflow, test requirements, and architectural decision-making process.

## Development Workflow

### Prerequisites

- Go 1.21 or later
- Make (for running build targets)
- Git

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
├── cmd/dfmt/          # Main CLI entry point
├── internal/
│   ├── cli/           # CLI command implementations
│   ├── config/        # Configuration loading
│   ├── core/          # Core domain logic (events, journal, index)
│   ├── daemon/        # Daemon lifecycle management
│   ├── sandbox/       # Sandboxed tool execution
│   └── transport/     # Transport layer (HTTP, stdio, Unix socket)
├── docs/
│   ├── adr/           # Architecture Decision Records
│   └── hooks/         # Hook integration guides
└── README.md          # Project documentation
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
   make test-coverage
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

# Run tests with coverage
make test-coverage

# Run specific package tests
go test ./internal/core/...

# Run tests with race detector
go test -race ./...
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
make coverage-html
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
- `golang.org/x/sys` — system calls not in stdlib
- `golang.org/x/crypto` — cryptographic operations
- `gopkg.in/yaml.v3` — YAML configuration (only exception to stdlib)

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

Run `dfmt bundle` to generate a diagnostic package:

```bash
dfmt bundle
```

Attach the resulting tarball to your issue.

### Feature Requests

- Describe the problem you're solving
- Explain why existing functionality is insufficient
- Provide concrete use cases
- Consider backward compatibility impact

## License

By contributing to DFMT, you agree that your contributions will be licensed under the MIT License.

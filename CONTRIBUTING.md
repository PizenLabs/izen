# Contributing to Izen

Thank you for your interest in contributing to Izen. This project is built around human-centered coding intelligence, and every contribution should uphold that principle.

---

## Table of Contents

- [Philosophy](#philosophy)
- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [Development Guide](#development-guide)
- [Pull Request Process](#pull-request-process)
- [Coding Standards](#coding-standards)
- [Testing](#testing)
- [Architecture Decisions](#architecture-decisions)

---

## Philosophy

Before contributing, read the [project philosophy](docs/architecture/PHILOSOPHY.md). Every feature must pass these questions:

1. Does it improve human understanding?
2. Does it reduce noise?
3. Does it preserve human control?
4. Does it increase trust?
5. Does it fit local-first?

If the answer to any is **no**, the contribution will likely be rejected.

---

## Code of Conduct

### Our Pledge

We are committed to providing a welcoming, inclusive, and harassment-free experience for everyone.

### Our Standards

- **Be respectful** — Disagreement is fine, personal attacks are not.
- **Be constructive** — Critique ideas, not people. Provide reasoning.
- **Be patient** — Open source is a global, asynchronous effort.
- **Be humble** — No one knows everything.

### Enforcement

Violations can be reported by opening an issue or contacting the maintainers. All reports will be reviewed and handled appropriately.

---

## Getting Started

### Prerequisites

- Go 1.26 or later
- Rust toolchain (only required for Lynx development)
- `make` (optional, for build shortcuts)

### Setup

```bash
# Fork and clone the repository
git clone https://github.com/<your-username>/izen.git
cd izen

# Build with embedded Lynx
make build

# Build without Lynx
go build ./cmd/izen

# Run tests
go test ./...
```

---

## Development Guide

### Project Structure

```
izen/
├── cmd/izen/            # CLI entrypoint
├── internal/            # All core logic (modular monolith)
│   ├── config/          # YAML config loader
│   ├── session/         # Active state management
│   ├── modes/           # Mode state machines
│   ├── graph/           # Tree-sitter AST parsing
│   ├── retrieval/       # Multi-tier retrieval
│   ├── context/         # Prompt assembly
│   ├── execution/       # Sandboxed execution
│   ├── ai/              # Provider interface
│   ├── lynx/            # Semantic search controller
│   ├── mcp/             # External integrations
│   └── ui/              # TUI layer
├── lynx/                # Rust embedded search engine
└── docs/architecture/   # Design documentation
```

### Internal Boundaries

All subsystems live in `internal/` with strict encapsulation. Follow these rules:

- Packages import only interfaces, not concrete implementations, from sibling packages
- The `graph` package has zero knowledge of `retrieval` or `execution`
- The `ui` package delegates to engines — it does not own logic
- Cross-package communication happens through well-defined interfaces

### Adding a New Language to the Graph

1. Add a `Language` constant in `internal/graph/types.go`
2. Add a parser case in `internal/graph/parser.go`
3. Provide a Tree-sitter grammar query for symbol extraction
4. Add tests in `internal/graph/graph_test.go`

### Adding a New MCP Integration

1. Create a new file in `internal/mcp/` implementing the `MCPServer` interface from `internal/mcp/gateway.go`
2. Register it in the gateway's server registry
3. Add tests in `internal/mcp/mcp_test.go`

---

## Pull Request Process

### Before Submitting

1. Ensure all tests pass: `go test ./...`
2. Ensure the linter passes: `go vet ./...`
3. Add tests for new functionality
4. Update documentation if public APIs or behavior change

### Commit Messages

Follow conventional commits:

```
<type>(<scope>): <description>

feat(graph): add Rust language parser support
fix(retrieval): handle empty graph cache gracefully
docs(readme): add quick start section
test(investigate): add evidence store edge cases
```

Types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `chore`

### PR Checklist

- [ ] Tests pass
- [ ] `go vet` is clean
- [ ] New code has tests
- [ ] Public API changes are documented
- [ ] Commit messages follow conventional format
- [ ] PR description explains the *why* (not just the *what*)

### Review Process

1. Maintainers review within 5 business days
2. Address requested changes
3. Once approved, a maintainer merges

---

## Coding Standards

### Go

- Follow [Effective Go](https://go.dev/doc/effective_go) and [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)
- Run `gofmt` before committing
- No unused exports, no unused variables
- Error handling: always check errors, wrap with context when propagating

### Testing

- Test package behavior, not implementation
- Use table-driven tests where appropriate
- Aim for >80% coverage on new code
- Integration tests that cross package boundaries go in `*_test.go` files within the consuming package

### Documentation

- Public types and functions must have Go doc comments
- Architecture decisions go in `PROJECT_STATE.md` as ADRs
- Internal design rationale goes in `docs/architecture/`

---

## Testing

```bash
# Run all tests
go test ./...

# With embedded Lynx
go test -tags lynx_embed ./...

# Run specific package tests
go test ./internal/retrieval/...

# Run with race detector
go test -race ./...

# Run benchmarks
go test -bench=. ./...
```

### Test Coverage

| Package | Tests | Status |
|---------|-------|--------|
| internal/context | 14 | All pass |
| internal/execution | 16 | All pass |
| internal/graph | 3 | All pass |
| internal/lynx | 12 | All pass |
| internal/mcp | 28 | All pass |
| internal/modes/investigate | 40 | All pass |
| internal/modes/review | 62 | All pass |
| internal/retrieval | 8 | All pass |

---

## Architecture Decisions

Significant decisions are recorded as Architecture Decision Records (ADRs) in `PROJECT_STATE.md`. When making a change that affects the project's architecture:

1. Discuss it in an issue first
2. Add an ADR entry documenting the decision and its rationale
3. Reference the ADR in your PR description

Key ADRs to be aware of:

- **ADR-1**: Modular monolith — no microservices, no distributed protocols
- **ADR-2**: Graph-first retrieval — Tree-sitter before semantic or text
- **ADR-4**: Session is ephemeral state, not conversation history
- **ADR-6**: Hypothesis-evidence loop with bounded max iterations
- **ADR-9**: UI is a passive shell — it does not own logic
- **ADR-10**: Lynx binary embedded via `go:embed`, started lazily

---

## Getting Help

- Open an issue for bugs, feature requests, or questions
- Review `PROJECT_STATE.md` for current status and roadmap
- Read `docs/architecture/` for detailed design documentation

---

*Built by [PizenLabs](https://github.com/PizenLabs).*
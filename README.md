<div align="center">

<img src="https://raw.githubusercontent.com/PizenLabs/onpic/refs/heads/main/izen/izen-banner.png" width="96%" alt="izen" />

# Izen

**Human-centered coding intelligence.**

Izen is a local-first, modular monolith CLI/TUI for code understanding, investigation, and safe mutation. Built around one core belief: **AI should strengthen human judgment, not replace it.**

[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Build Status](https://img.shields.io/github/actions/workflow/status/PizenLabs/izen/ci.yml?style=flat-square&logo=githubactions)](https://github.com/PizenLabs/izen/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/PizenLabs/izen?style=flat-square)](https://goreportcard.com/report/github.com/PizenLabs/izen)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=flat-square)](https://opensource.org/licenses/MIT)
[![Code Coverage](https://img.shields.io/codecov/c/github/PizenLabs/izen?style=flat-square&logo=codecov)](https://codecov.io/gh/PizenLabs/izen)
[![Release](https://img.shields.io/github/v/release/PizenLabs/izen?style=flat-square&logo=github)](https://github.com/PizenLabs/izen/releases)
[![Contributors](https://img.shields.io/github/contributors/PizenLabs/izen?style=flat-square)](https://github.com/PizenLabs/izen/graphs/contributors)
[![Discord](https://img.shields.io/discord/123456789?style=flat-square&logo=discord&label=community)](https://discord.gg/pizenlabs)

[Philosophy](docs/architecture/PHILOSOPHY.md) • [Tech Stack](docs/architecture/TECHSTACK.md) • [Contributing](CONTRIBUTING.md) • [Security](SECURITY.md) • [Code of Conduct](CODE_OF_CONDUCT.md) • [Roadmap](ROADMAP.md) • [Lynx](lynx/)

</div>

---

## Why Izen?

| Problem | Izen's Approach |
|---------|-----------------|
| **Context dumping** burns tokens and obscures signal | Graph-first retrieval: Tree-sitter AST symbol index before any file read |
| **Black-box autonomy** removes human control | Mode-driven: `/ask`, `/plan`, `/build`, `/investigate`, `/review` with explicit permissions |
| **Hidden fallbacks** erode trust | Explicit retrieval pyramid: Graph → Lynx → rg → grep → read (every step visible) |
| **Irreversible mutations** risk data loss | Git checkpoints, patch storage, audit logs — everything reversible |
| **Cloud dependency** violates privacy | Local-first: everything runs on your machine, no hidden remote systems |

---

## Quick Start

### Prerequisites

- **Go 1.26+**
- **Rust toolchain** (only for building with embedded Lynx)

### Installation

```bash
# With embedded Lynx semantic search (recommended)
make build

# Without embedded Lynx (no Rust toolchain needed)
go build ./cmd/izen

# Install globally
make install
```

### Run

```bash
./izen
# or
go run ./cmd/izen
```

### Test

```bash
# With Lynx
make test

# Without Lynx
go test ./...

# With race detector
go test -race ./...

# Run benchmarks
go test -bench=. ./...
```

### Configuration

Global config at `~/.izen/izen.conf.yml`:

```yaml
models:
  default: claude-sonnet-4-20250514
  provider: anthropic
execution:
  sandbox: true
  confirm: true
lynx:
  enabled: true
  lazy_start: true
  semantic_threshold: 0.6
  max_results: 20
mcp:
  enabled: false
```

---

## Modes

Izen is **mode-driven**, not personality-driven. Each mode defines explicit permission boundaries:

| Mode | Purpose | Read | Write | Shell | Test | Patch | Checkpoint |
|------|---------|------|-------|-------|------|-------|------------|
| `/ask` | Explain, inspect, understand code | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| `/plan` | Architecture, migrations, refactors | ✅ | ❌ | ❌ | ❌ | ❌ | Optional |
| `/build` | Implement, refactor, write tests | ✅ | ✅ | ✅ | ✅ | ✅ | Required |
| `/investigate` | Debug failures, regressions, CI issues | ✅ | ❌ | ✅ | ✅ | ❌ | Optional |
| `/review` | Audit changes, detect risks, inspect regressions | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |

---

## Architecture

```
izen/
├── cmd/izen/main.go              # CLI entrypoint
├── internal/
│   ├── config/                   # YAML config loader (~/.izen/izen.conf.yml)
│   ├── session/                  # Ephemeral active state (objective, mode)
│   ├── modes/                    # Mode enum + state machines
│   │   ├── investigate/          # 9-state hypothesis-evidence debug loop
│   │   └── review/               # 6-state diff impact + risk audit
│   ├── graph/                    # Tree-sitter AST parsing + symbol graph
│   ├── retrieval/                # Multi-tier fallback: Graph → Lynx → rg → grep → read
│   ├── context/                  # Signal-dense prompt assembly
│   ├── execution/                # Sandboxed command runner, patches, checkpoints
│   ├── ai/                       # ModelProvider interface
│   ├── lynx/                     # Embedded Rust semantic search daemon controller
│   ├── mcp/                      # Gateway: GitHub Issues, Jira, Linear
│   └── ui/                       # Bubble Tea TUI (modular monolith)
│       ├── program.go            # Entrypoint: NewProgram factory
│       ├── model.go              # Model struct, message types, record
│       ├── update.go             # Init, Update, message dispatch
│       ├── view.go               # View, body/header/modebar/statusbar renderers
│       ├── keys.go               # Keyboard event routing
│       ├── commands.go           # Input dispatch, command handler
│       ├── stream.go             # AI streaming (readStream / streamCmd)
│       ├── agents.go             # Investigate & Review agent commands
│       ├── proposals.go          # Build proposal extraction, apply, checkpoint
│       ├── suggestions.go        # Command & file auto-complete, palette UI
│       ├── styles.go             # Colour palette, lipgloss styles, helpers
│       ├── highlight.go          # Code syntax highlighting
│       ├── renderers.go          # Startup banner, confirmation box
│       └── utils.go              # Path shortening, mode prefix, file refs
├── lynx/                         # Embedded Rust semantic search engine
└── docs/architecture/            # Philosophy, tech stack, architecture docs
```

### Retrieval Pyramid

```
Graph (Tree-sitter) ── <1ms
    ↓ (confidence < threshold)
Lynx (BM25 + FastEmbed) ── <50ms
    ↓ (daemon unavailable / parse failure)
rg / grep / glob / read ── text fallback
```

---

## Key Features

- **Graph-first retrieval** — Tree-sitter AST symbol index before any file read
- **Hypothesis-evidence investigation** — 9-state deterministic debug loop with automated test iteration
- **Risk audit sandbox** — Pre-flight AST validation for patch safety
- **Review mode** — Git diff impact radius analysis and import chain traversal
- **Embedded semantic search** — Rust-powered (Tantivy BM25 + FastEmbed) via `go:embed`
- **MCP gateway** — Optional GitHub Issues, Jira, and Linear integrations
- **Safe execution** — Sandboxed runner with dangerous-command detection, patch capture/rollback, git checkpoints

---

## Performance Benchmarks

> **Note:** Benchmarks run on Apple M2 Pro, 32GB RAM. Results vary by hardware and repository size.

### Graph Engine (Tree-sitter)

| Operation | Target | Typical |
|-----------|--------|---------|
| Cold index (10k files) | < 5s | ~3.2s |
| Warm index (incremental) | < 500ms | ~180ms |
| Symbol lookup | < 1ms | ~0.3ms |
| Call chain resolution (depth 5) | < 5ms | ~2.1ms |

### Retrieval Pyramid

| Tier | Operation | Target | Typical |
|------|-----------|--------|---------|
| 1 | Graph exact symbol | < 1ms | ~0.4ms |
| 2 | Lynx hybrid search | < 50ms | ~28ms |
| 3 | Ripgrep fallback | < 200ms | ~85ms |

### Lynx Semantic Search (Embedded)

| Benchmark | Result |
|-----------|--------|
| Index 50k symbols | ~1.8s |
| Query latency (p99) | 35ms |
| Memory (idle) | ~45MB |
| Memory (active query) | ~120MB |

Run benchmarks locally:

```bash
# Go benchmarks
go test -bench=. -benchmem ./internal/...

# Lynx (Rust) benchmarks
cd lynx && cargo bench
```

---

## Project Status

| Phase | Description | Status |
|-------|-------------|--------|
| 1 | Core Foundation — CLI, config, session, modes, TUI | ✅ Complete |
| 2 | Graph Core — Tree-sitter parsing, symbol index, caching | ✅ Complete |
| 3 | Retrieval Layer — Multi-tier fallback pyramid | ✅ Complete |
| 4 | Execution Layer — Runner, sandbox, patches, checkpoints | ✅ Complete |
| 5 | Context Engine — Context builder, signal compression | ✅ Complete |
| 6 | Investigate Mode — Hypothesis-evidence loop, proximity slicing | ✅ Complete |
| 7 | Lynx Monolith Integration & Deep Semantic | ✅ Complete |
| 8 | Review Mode, Risk Audit & MCP Ecosystem | ✅ Complete |

**Tests: ~183 across 9 packages, 100% pass rate.**

---

## What Izen Is Not

- ❌ A black-box autonomous agent
- ❌ A cloud-dependent coding system
- ❌ A prompt collection or personality wrapper
- ❌ An uncontrolled MCP hub
- ❌ A file-dumping token burner

---

## Built With

- [Go](https://go.dev/) — primary language, single binary
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) — TUI framework
- [Lip Gloss](https://github.com/charmbracelet/lipgloss) — terminal styling
- [Tree-sitter](https://tree-sitter.github.io/) — AST parsing (Go, Python, Rust)
- [Lynx](lynx/) — Rust embedded semantic search (Tantivy + FastEmbed)

---

## Documentation

| Document | Description |
|----------|-------------|
| [Philosophy](docs/architecture/PHILOSOPHY.md) | Constitutional foundation — 12 core principles |
| [Tech Stack](docs/architecture/TECHSTACK.md) | Technical implementation details |
| [Contributing](CONTRIBUTING.md) | Development guide, PR process, coding standards |
| [Security](SECURITY.md) | Vulnerability reporting, security model |
| [Code of Conduct](CODE_OF_CONDUCT.md) | Community standards and enforcement |
| [Roadmap](ROADMAP.md) | Future direction and milestones |

---

## Community

- **Issues**: [GitHub Issues](https://github.com/PizenLabs/izen/issues) — Bug reports, feature requests
- **Discussions**: [GitHub Discussions](https://github.com/PizenLabs/izen/discussions) — Questions, ideas, showcase
- **Discord**: [PizenLabs Community](https://discord.gg/pizenlabs) — Real-time chat

---

## Contributing

We welcome contributions that align with our [philosophy](docs/architecture/PHILOSOPHY.md). Please read [CONTRIBUTING.md](CONTRIBUTING.md) for:

- Development setup
- Architecture boundaries
- Pull request process
- Coding standards
- Testing guidelines

---

## Security

Izen takes security seriously. See [SECURITY.md](SECURITY.md) for:

- Vulnerability reporting process
- Threat model
- Sandbox and execution guards
- Supported versions

---

## License

This project is licensed under the **MIT License** — see the [LICENSE](LICENSE) file for details.

The embedded Lynx engine is also MIT licensed — see [lynx/LICENSE](lynx/LICENSE).

---

## Acknowledgments

Built with ❤️ by [PizenLabs](https://github.com/PizenLabs) and [contributors](https://github.com/PizenLabs/izen/graphs/contributors).

Special thanks to the open source projects that make Izen possible:
- [Charmbracelet](https://github.com/charmbracelet) for Bubble Tea and Lip Gloss
- [Tree-sitter](https://tree-sitter.github.io/) for language-agnostic parsing
- [Tantivy](https://github.com/quickwit-oss/tantivy) for full-text search
- [FastEmbed](https://github.com/qdrant/fastembed) for local embeddings

---

<div align="center">

**AI should strengthen human judgment, not replace it.**

*Star this repo if you believe in human-centered coding intelligence.*

</div>
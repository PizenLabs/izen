# Izen

**Human-centered coding intelligence.**

Izen is a local-first, modular monolith CLI/TUI for code understanding, investigation, and safe mutation. It is built around one core belief: *AI should assist human judgment, not replace it.*

[Philosophy](docs/architecture/PHILOSOPHY.md) • [Tech Stack](docs/architecture/TECHSTACK.md) • [Project State](PROJECT_STATE.md) • [Lynx](lynx/)

---

## Philosophy

| Principle | Meaning |
|-----------|---------|
| **Human-centered** | AI is an assistant, never an authority. Human judgment remains final. |
| **Clarity over speed** | `clarity > control > trust > speed`. Fast is useful; clear is required. |
| **Explicit over implicit** | Nothing important happens silently. Every fallback is visible. |
| **Minimal by default** | Complexity activates proportionally to intent. Simple inputs stay simple. |
| **Local-first** | Everything runs locally. No cloud assumptions, no hidden remote systems. |
| **Security-aware** | Nothing is trusted by default. Every capability is explicit, scoped, revocable. |
| **Reversible by design** | Every meaningful mutation is recoverable via checkpoints, patches, and audit logs. |
| **Structure before intelligence** | Graph → symbols → call chains → dependency slices before raw files. |
| **Semantic-first, text-resilient** | Graph → semantic search → text fallback. |
| **Modes over prompts** | Behavior is declarative and bounded by mode, not improvised by prompt. |

---

## Modes

| Mode | Purpose | Permissions |
|------|---------|-------------|
| `/ask` | Explain, inspect, understand code | Read-only |
| `/plan` | Architecture, migrations, refactors | No execution |
| `/build` | Implement, refactor, write tests | Controlled execution |
| `/investigate` | Debug failures, regressions, CI issues | Bounded test loops |
| `/review` | Audit changes, detect risks, inspect regressions | Read-only analysis |

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

## Quick Start

### Prerequisites

- Go 1.26+
- Rust toolchain (only for building with embedded Lynx)

### Build

```bash
# With embedded Lynx semantic search
make build

# Without embedded Lynx (no Rust toolchain needed)
go build ./cmd/izen
```

### Run

```bash
./izen

# Or directly:
go run ./cmd/izen
```

### Test

```bash
# With Lynx
make test

# Without Lynx
go test ./...
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

## Key Features

- **Graph-first retrieval**: Tree-sitter AST symbol index before any file read
- **Hypothesis-evidence investigation**: 9-state deterministic debug loop with automated test iteration
- **Risk audit sandbox**: Pre-flight AST validation for patch safety
- **Review mode**: Git diff impact radius analysis and import chain traversal
- **Embedded semantic search**: Rust-powered (Tantivy BM25 + FastEmbed) via `go:embed`
- **MCP gateway**: Optional GitHub Issues, Jira, and Linear integrations
- **Safe execution**: Sandboxed runner with dangerous-command detection, patch capture/rollback, git checkpoints

---

## Project Status

| Phase | Description | Status |
|-------|-------------|--------|
| 1 | Core Foundation — CLI, config, session, modes, TUI | Complete |
| 2 | Graph Core — Tree-sitter parsing, symbol index, caching | Complete |
| 3 | Retrieval Layer — Multi-tier fallback pyramid | Complete |
| 4 | Execution Layer — Runner, sandbox, patches, checkpoints | Complete |
| 5 | Context Engine — Context builder, signal compression | Complete |
| 6 | Investigate Mode — Hypothesis-evidence loop, proximity slicing | Complete |
| 7 | Lynx Monolith Integration & Deep Semantic | Complete |
| 8 | Review Mode, Risk Audit & MCP Ecosystem | Complete |

**Tests: ~183 across 9 packages, 100% pass rate.**

---

## What Izen Is Not

- A black-box autonomous agent
- A cloud-dependent coding system
- A prompt collection or personality wrapper
- An uncontrolled MCP hub
- A file-dumping token burner

---

## Built With

- [Go](https://go.dev/) — primary language, single binary
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) — TUI framework
- [Lip Gloss](https://github.com/charmbracelet/lipgloss) — terminal styling
- [Tree-sitter](https://tree-sitter.github.io/) — AST parsing (Go, Python, Rust)
- [Lynx](lynx/) — Rust embedded semantic search (Tantivy + FastEmbed)

---

## License

This project is licensed under the terms of the [LICENSE](lynx/LICENSE) file.

---

*Built by [PizenLabs](https://github.com/PizenLabs).*
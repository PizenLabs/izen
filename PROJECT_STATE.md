# PROJECT STATE

## Overview

Izen is a human-centered coding intelligence system built by PizenLabs.
A local-first, modular monolith CLI/TUI for code understanding, investigation, and safe mutation.

---

## Architecture

```
izen/
├── cmd/izen/main.go              # CLI entrypoint — loads config, session, launches TUI
│
├── internal/
│   ├── config/config.go          # YAML config loader (~/.izen/izen.conf.yml)
│   ├── session/session.go        # Ephemeral active state (objective, mode, investigation)
│   │
│   ├── modes/
│   │   ├── modes.go              # Mode enum + resolver (ask/plan/build/investigate/review)
│   │   └── investigate/          # Phase 6 — Deep diagnostic loop subsystem
│   │       ├── state.go          # Hypothesis-evidence state machine
│   │       ├── hypothesis.go     # Hypothesis management
│   │       ├── evidence.go       # Evidence collection & storage
│   │       ├── proximity.go      # Stack trace parsing + proximity context slicing
│   │       ├── testloop.go       # Automated test iteration loop
│   │       └── engine.go         # Main investigation orchestrator
│   │
│   ├── graph/                    # Tree-sitter AST parsing + symbol graph
│   │   ├── types.go              # Graph, FileNode, Symbol, Language types
│   │   ├── scanner.go            # File tree walk with exclusion filters
│   │   ├── parser.go             # Tree-sitter bindings (Go, Python, Rust)
│   │   ├── engine.go             # Build/LoadCache/SaveCache (graph.cache.v1)
│   │   └── cache.go              # Gob-based cache serialization
│   │
│   ├── retrieval/                # Multi-tier fallback retrieval pyramid
│   │   ├── retriever.go          # Chain-of-responsibility: Graph → Glob → rg → grep → read
│   │   ├── graph.go              # GraphLookup: symbol/file/package/import queries
│   │   ├── fallback.go           # Glob, Ripgrep, Grep, Read text-level fallbacks
│   │   ├── confidence.go         # Confidence scoring (Exact→Fuzzy→Semantic→Pattern→Text→Fallback)
│   │   └── result.go             # ResultSet, Result types with merge/best/file grouping
│   │
│   ├── context/                  # Signal-dense prompt assembly
│   │   ├── types.go              # Context, FileSlice, SymbolRef, Stats
│   │   ├── builder.go            # BuildRequest → Context builder (graph + git + session)
│   │   └── renderer.go           # Structured prompt renderer (objective/mode/diff/files/errors)
│   │
│   ├── execution/                # Safe workspace mutation
│   │   ├── execution.go          # Engine: Runner + TestRunner + PatchManager + CheckpointManager
│   │   ├── runner.go             # os/exec wrapper with sandbox + dangerous-command detection
│   │   ├── test.go               # go test runner with output parsing (PASS/FAIL/SKIP, coverage)
│   │   ├── patch.go              # File-level patch capture/apply/rollback (patches/*.json)
│   │   └── checkpoint.go         # Git-based checkpoints with session tracking
│   │
│   ├── ai/provider.go            # ModelProvider interface + Manager (Register/Get/Default)
│   ├── providers/                # (empty) — API client implementations (OpenAI, Anthropic, etc.)
│   ├── mcp/                      # (empty) — Optional MCP server integrations
│   ├── hooks/                    # (empty) — Lifecycle triggers
│   └── ui/ui.go                  # Bubble Tea TUI: mode display, input handling, investigate view
│
├── pkg/                          # (empty) — Reusable utility packages
│
├── lynx/                         # Lynx (Rust) embedded search engine submodule
│   └── ...                       # Tantivy BM25 + FastEmbed semantic search daemon
│
├── README.md                     # Project overview, philosophy, architecture, modes
├── PHILOSOPHY.md                 # Core principles (10 principles + 10 operational rules)
├── TECHSTACK.md                  # Technical stack, internal architecture, implementation phases
├── PROJECT_STATE.md              # ← This file: current state, decisions, roadmap
├── go.mod / go.sum               # Go module dependencies
└── .izen/                        # Project-local state directory
    └── session.json              # Current session (mode, objective, investigation_id)
```

---

## Phase Completion Status

| Phase | Description | Status |
|-------|-------------|--------|
| 1 | Core Foundation — CLI, config, session, modes, TUI | Complete |
| 2 | Graph Core — Tree-sitter parsing, symbol index, caching | Complete |
| 3 | Retrieval Layer — Multi-tier fallback pyramid | Complete |
| 4 | Execution Layer — Runner, sandbox, patches, checkpoints | Complete |
| 5 | Context Engine — Context builder, signal compression | Complete |
| 6 | Investigate Mode — Hypothesis-evidence loop, proximity slicing, test iteration | Complete |
| **7** | **Lynx Monolith Integration & Deep Semantic** | **Complete** |
| 8 | Review Mode, Risk Audit & MCP Ecosystem | Pending |

---

## Architecture Decisions

### ADR-1: Modular Monolith
All subsystems live in `internal/` with strict encapsulated boundaries. No microservices. No distributed protocols. Single binary deployment.

### ADR-2: Graph-First Retrieval
Before semantic search or raw file reads, always query the Tree-sitter symbol graph first. Semantic (Lynx) and text (rg/grep/glob/read) are fallback tiers.

### ADR-3: Tree-sitter over Regex Parsing
AST-level symbol extraction instead of regex-based parsing. Supports Go, Python, Rust. Hot-pluggable language extensions.

### ADR-4: Session as Ephemeral State
`session.json` is a short-lived, optimized state snapshot — not a conversation log. Stores objective, mode, assumptions, investigation references.

### ADR-5: Investigation Storage
Investigations persist to `.izen/investigations/` with JSON reports. Session holds an `InvestigationID` reference.

### ADR-6: Hypothesis-Evidence Loop
The investigate mode uses a 9-state deterministic state machine (Observe → Hypothize → Search → Gather → Evaluate → Narrow → Verify → Propose → Done). Bounded by max iterations to prevent infinite loops.

### ADR-7: Proximity Context Slicing
Stack traces parsed into `(file, line, column, function)` frames. Proximity slicer reads ±N lines around each frame, annotated with `>` markers on the target line.

### ADR-8: Test Iteration as Evidence Source
Automated test runs feed directly into the evidence store. Failed tests, stack traces, and narrowed packages drive the investigation loop.

### ADR-9: UI is Passive Shell
The Bubble Tea TUI renders mode state and investigation results. It does not own investigation logic — it delegates to the investigate engine.

### ADR-10: Async-Lazy Lynx
Lynx (Rust binary) is embedded via `go:embed`. Background daemon starts on first semantic query. Not blocking startup.

### ADR-11: Lynx as Retrieval Tier 2
Lynx sits between graph (Tier 1) and text fallbacks (Tier 3+). Activated when graph confidence < exact match or for natural-language queries without symbol patterns. Implemented via `lynx.Controller` bridging to `retrieval.SetLynxController`.

### ADR-12: Conditional Embedding via Build Tag
The Lynx binary is embedded via `go:embed` behind a `lynx_embed` build tag. Without the tag, the binary is resolved via PATH lookup or auto-built from the Rust source in `lynx/`. This enables development without Rust toolchain.

### ADR-13: go/ast + go/types Mutation Tracing
`MutationTracer` uses `go/ast` for assignment tracking and `go/types` for type system analysis, combined with the existing graph's symbol and dependency indices. This provides cross-file impact analysis without requiring a running Lynx daemon.

---

## Current File Inventory

### Source Files (Go, 37 files)

```
cmd/izen/main.go                               (34 lines)  — CLI entrypoint + Lynx controller setup
internal/config/config.go                       (106 lines) — YAML config loader + Lynx config
internal/session/session.go                     (100 lines) — Session state + investigation persistence
internal/modes/modes.go                         (97 lines)  — Mode enum (5 modes) + resolver
internal/modes/investigate/state.go             (108 lines) — 9-state state machine
internal/modes/investigate/hypothesis.go        (109 lines) — Hypothesis management
internal/modes/investigate/evidence.go          (110 lines) — Evidence store
internal/modes/investigate/proximity.go         (148 lines) — Stack trace parsing + context slicing
internal/modes/investigate/testloop.go          (72 lines)  — Test iteration loop
internal/modes/investigate/engine.go            (292 lines) — Investigation orchestrator
internal/modes/investigate/investigate_test.go  (858 lines) — 40 tests
internal/graph/types.go                         (182 lines) — Graph/Symbol/FileNode types
internal/graph/scanner.go                       (104 lines) — File tree walk
internal/graph/parser.go                        (481 lines) — Tree-sitter AST parsing
internal/graph/engine.go                        (114 lines) — Build/Load/Save cache
internal/graph/cache.go                         (31 lines)  — Cache serialization
internal/retrieval/retriever.go                 (310 lines) — Chain-of-responsibility router + Lynx tier
internal/retrieval/graph.go                     (168 lines) — Graph symbol queries
internal/retrieval/fallback.go                  (213 lines) — rg/grep/glob/read fallbacks
internal/retrieval/confidence.go                (74 lines)  — Confidence scoring + Lynx strategies
internal/retrieval/result.go                    (75 lines)  — ResultSet merge/best/file
internal/retrieval/retrieval_test.go            (201 lines) — 8 tests
internal/context/types.go                       (52 lines)  — Context/FileSlice/SymbolRef
internal/context/builder.go                     (255 lines) — Context builder
internal/context/renderer.go                    (186 lines) — Prompt renderer
internal/context/context_test.go                (385 lines) — 14 tests
internal/execution/execution.go                 (31 lines)  — Execution engine
internal/execution/runner.go                    (119 lines) — os/exec runner + sandbox
internal/execution/test.go                      (171 lines) — Test runner + output parser
internal/execution/patch.go                     (146 lines) — Patch capture/apply/rollback
internal/execution/checkpoint.go                (109 lines) — Git checkpoint management
internal/execution/execution_test.go            (274 lines) — 16 tests
internal/ai/provider.go                         (71 lines)  — AI provider interface
internal/ui/ui.go                               (280 lines) — Bubble Tea TUI
internal/lynx/client.go                         (167 lines) — JSON-RPC stdio client for Lynx MCP
internal/lynx/daemon.go                         (293 lines) — Process lifecycle + Daemon controller
internal/lynx/embed_embed.go                    (98 lines)  — go:embed with lynx_embed build tag
internal/lynx/embed_none.go                     (81 lines)  — Fallback binary resolution (no embed)
internal/lynx/lynx.go                           (97 lines)  — Controller, cache queries, semantic heuristics
internal/lynx/mutation.go                       (468 lines) — go/ast + go/types mutation tracing
internal/lynx/lynx_test.go                      (195 lines) — 12 tests
```

### Test Coverage

| Package | Tests | Status |
|---------|-------|--------|
| internal/context | 14 | All pass |
| internal/execution | 16 | All pass |
| internal/git | (via execution) | All pass |
| internal/graph | 3 | All pass |
| internal/lynx | 12 | All pass |
| internal/modes/investigate | 40 | All pass |
| internal/retrieval | 8 | All pass |
| **Total** | **~93** | **100% pass** |

---

## Todo

### Phase 8 — Review Mode, Risk Audit & MCP Ecosystem
- [ ] Review Engine: Git diff impact radius analysis
- [ ] Risk Audit Sandbox: pre-flight AST validation
- [ ] MCP Gateway: GitHub Issues, Jira, Linear integrations
- [ ] `/review` mode state machine and UI

### Cross-cutting
- [ ] Provider implementations (OpenAI, Anthropic, OpenRouter clients)
- [ ] MCP server abstractions
- [ ] Hook lifecycle triggers
- [ ] Provider failover/switching
- [ ] Token cost accounting
- [ ] Streaming response rendering in TUI

### Quality
- [ ] Codebase-memory MCP indexing for the whole project
- [ ] Integration tests across engine boundaries
- [ ] Benchmark for graph build time
- [ ] Benchmark for retrieval tier latency

---

## Schedule

### Immediate (Next)
1. Phase 7 — Lynx embedding + daemon controller
2. Provider client implementations (Anthropic, OpenAI)

### Short-term (1-2 weeks)
3. Phase 8 — Review mode + risk audit
4. Token accounting and streaming

### Medium-term (3-4 weeks)
5. MCP ecosystem (GitHub, Jira, Linear)
6. Cross-file impact analysis
7. Integration benchmarks

### Long-term
8. Hot-pluggable language extension drivers
9. Semantic commit message generation
10. CI/CD failure reproduction

---

## Key Metrics

| Metric | Value |
|--------|-------|
| Go files | 37 |
| Total LOC (source) | ~5,400 |
| Total LOC (tests) | ~1,913 |
| Test count | ~93 |
| Dependencies | bubbletea, lipgloss, tree-sitter, yaml.v3 |
| External submodules | lynx (Rust) |
| Build time | < 1s (no embed), ~1s (with embed) |
| Phase completion | 7/8 (87.5%) |

---

## Quick Reference

```bash
# Build with embedded Lynx
make build
# or: go build -tags lynx_embed ./cmd/izen

# Build without embedded Lynx
go build ./cmd/izen

# Test (with embed)
make test
# or: go test -tags lynx_embed ./...

# Test (without embed)
go test ./...

# Build Lynx binary
make build-lynx
# or: cd lynx && cargo build --release && cp target/release/lx ../internal/lynx/bin/lx

# Run
go run ./cmd/izen

# Investigate mode (in TUI)
/investigate       # Switch to investigate mode
<problem desc>     # Run investigation (e.g., "nil pointer in NewEngine")
```

---

*Generated: 2026-06-26*
*Project: github.com/PizenLabs/izen*

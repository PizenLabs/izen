# Izen Roadmap

> **Vision**: A local-first industrial cognition layer for engineering work.

This roadmap outlines the strategic direction for Izen. All items are subject to the [philosophy](docs/architecture/PHILOSOPHY.md) — if a feature violates our principles, it will not be built.

---

## Current Release: v0.1.x (Foundation Complete)

All 8 foundation phases complete. Focus: stability, polish, and ecosystem.

### Near-term (v0.2 — Q3 2026)

| Area | Item | Status |
|------|------|--------|
| **Graph** | TypeScript/JavaScript Tree-sitter support | 🔄 In progress |
| **Graph** | Java/Kotlin Tree-sitter support | 📋 Planned |
| **Retrieval** | Cross-file type inference for Go | 📋 Planned |
| **Lynx** | Incremental index updates (watch mode) | 📋 Planned |
| **UI** | Split-pane diff view for review mode | 📋 Planned |
| **UI** | Command palette (fuzzy search) | 📋 Planned |
| **MCP** | GitHub PR review integration | 📋 Planned |
| **MCP** | Linear issue → code mapping | 📋 Planned |
| **Config** | Project-local `.izen/izen.conf.yml` override | 📋 Planned |
| **Testing** | Property-based tests for graph engine | 📋 Planned |

---

## Medium-term (v0.3 — Q4 2026)

### Language Intelligence

- [ ] **Go**: Full `go/types` integration for type-aware queries
- [ ] **Rust**: `rust-analyzer` LSP integration for precise symbols
- [ ] **Python**: `pyright`/`basedpyright` type stubs support
- [ ] **Generic**: Language server protocol (LSP) fallback for unsupported languages

### Investigation Mode

- [ ] Automated root-cause classification (null ptr, race, memory, logic)
- [ ] Historical regression bisect via `git bisect` automation
- [ ] CI/CD log ingestion (GitHub Actions, GitLab CI, Buildkite)
- [ ] Flame graph integration for performance investigations

### Review Mode

- [ ] Architectural rule enforcement (custom rules via CEL/ReGo)
- [ ] Dependency freshness analysis (outdated/vulnerable deps)
- [ ] Test coverage delta on PR diff
- [ ] Semantic versioning impact detection

### Context Engine

- [ ] Cross-repo symbol resolution (monorepo support)
- [ ] Dynamic token budget allocation per-mode
- [ ] Context compression via AST summarization (SLM-assisted)

---

## Long-term (v1.0 — 2027)

### Stability & Production Readiness

- [ ] **API stability guarantee** — Internal interfaces versioned
- [ ] **Plugin architecture** — Wasm-based extensions (sandboxed)
- [ ] **Distributed caching** — Optional shared cache for teams (local network)
- [ ] **Reproducible builds** — Hermetic build with Nix/Bazel
- [ ] **SBOM generation** — CycloneDX/SPDX for supply chain

### Industrial Features

- [ ] **Policy engine** — Org-wide guardrails (no secrets, no prod deploys, etc.)
- [ ] **Audit trail export** — Structured logs for compliance (JSONL, OpenTelemetry)
- [ ] **Multi-repo workspaces** — Unified graph across related repos
- [ ] **Offline model bundling** — `ollama`/`llama.cpp` embedded for air-gapped

### Ecosystem

- [ ] **VS Code extension** — Read-only graph visualization, mode status
- [ ] **JetBrains plugin** — Same as above
- [ ] **Neovim plugin** — Native integration via RPC
- [ ] **CI/CD integrations** — GitHub App, GitLab integration, generic webhook

---

## Research & Exploration

> These are **not committed**. They require philosophy review before any implementation.

| Area | Idea | Philosophy Check |
|------|------|------------------|
| **AI** | Local SLM for summarization (no API calls) | ✅ Local-first |
| **AI** | Agent-to-agent handoff (investigate → build) | ⚠️ Must preserve human control |
| **Graph** | Cross-language call graphs (FFI, RPC) | ✅ Structure before intelligence |
| **Graph** | Temporal graph (code evolution over time) | ⚠️ Complexity budget |
| **UI** | Web-based dashboard (optional) | ❌ Local-first violation unless fully local |
| **Execution** | Containerized execution (podman/docker) | ✅ If sandboxed, opt-in |

---

## Release Cadence

| Channel | Frequency | Policy |
|---------|-----------|--------|
| **Patch** | As needed | Bug fixes only, no new features |
| **Minor** | ~6 weeks | New features, backward compatible |
| **Major** | ~12 months | Breaking changes, philosophy review |

Pre-releases: `v0.x.y-rc.z` for release candidates.

---

## How to Influence the Roadmap

1. **Open an issue** — Tag with `roadmap` for discussion
2. **Join Discord** — #roadmap channel for real-time discussion
3. **Upvote** — 👍 on issues signals priority
4. **Contribute** — PRs for planned items welcome (check issue first)

---

## Philosophy Gate

Every roadmap item must pass the [Operational Rules](docs/architecture/PHILOSOPHY.md#operational-rules):

> 1. Does it improve human understanding?
> 2. Does it reduce noise?
> 3. Does it preserve human control?
> 4. Does it increase trust?
> 5. Does it fit local-first?
> 6. Can Graph solve this first?
> 7. Is this context necessary right now?
> 8. Can this mutation be reversed?
> 9. Can local solve this before external?
> 10. Does this increase autonomy beyond understanding?
> 11. Can raw diagnostics be pre-filtered locally?
> 12. Does this exceed token budget?

**If any answer is "no" → do not build it.**

---

*Roadmap last updated: 2026-07-03*
*Next review: 2026-08-15*
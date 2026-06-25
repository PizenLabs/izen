# TECHSTACK.md

## Izen Technical Stack

This document defines the technical implementation of Izen.

It exists to answer:
* What is built
* How it is built
* Why this stack is chosen
* How each subsystem works
* What developmental phases are followed

This is the implementation contract behind the Izen philosophy.

⸻

## Core Stack

### Language: Go
Primary language for Izen.

**Reason:**
* Fast startup times (<10ms)
* Single-binary distribution
* High-performance concurrency (Goroutines/Channels)
* Robust CLI and TUI ecosystem
* Efficient, low-overhead file I/O
* Low and predictable memory usage
* Zero-dependency deployment profiles

Go is chosen because Izen is fundamentally a CLI-first, local-first, workflow-heavy, and execution-heavy tool. It is not an ML-heavy system; its primary duty is systemic orchestration.

⸻

### UI Layer: Bubble Tea
Used to orchestrate the terminal application runtime loop.

**Responsibilities:**
* Contextual mode switching (`/ask`, `/investigate`, `/review`)
* Interactive multi-step prompts
* Non-blocking workflow rendering
* Real-time progress and token streaming
* Safe-guard confirmations and structural audits

**Purpose:** Provide an elite, blazing-fast, and human-friendly CLI/TUI experience.

⸻

### Styling Layer: Lip Gloss
Used for programmatic visual layout definitions.

**Responsibilities:**
* Color palette isolation (optimized for terminal standards)
* Structured layout alignment and box models
* Visual hierarchy and typography controls

**Purpose:** Drastically improve scanability and contextual readability under dense code environments.

⸻

### Configuration Layer: YAML
Used for global configuration management stored natively at `~/.izen/izen.conf.yml`.

**Responsibilities:**
* LLM model and API provider registries
* Operational execution and token-burn policies
* Graceful fallback thresholds
* Model Context Protocol (MCP) tool configurations

**Why YAML:** Human-readable, expressive, and easily maintainable by engineers.

⸻

### AI Layer: Provider Abstraction
Designed with strict architectural decoupling to prevent commercial model lock-in.

**Interface:** `ModelProvider`

**Responsibilities:**
* Prompt translation and strict execution
* Stream-based payload handling
* Real-time token and cost accounting
* Automatic provider/model failover switching

**Supported Providers:** OpenAI, Anthropic, OpenRouter, and standard local LLM engines (via OpenAI-compatible APIs).
**Future Targets:** Google Gemini, DeepSeek, and specialized private endpoints.
**Core Principle:** Models are temporary, volatile assets; the reasoning engine is permanent.

⸻

## Internal Architecture

Izen is engineered as a highly disciplined **Modular Monolith**.

```text
izen/
├── cmd/                # CLI entrypoints and subcommands
├── internal/
│   ├── config/         # Global and local YAML loaders
│   ├── modes/          # State machines for /ask, /investigate, /review
│   ├── session/        # Ephemeral active state tracking
│   ├── graph/          # Tree-sitter powered repository indexing
│   ├── retrieval/      # Hybrid multi-layer discovery pipeline
│   ├── execution/      # OS command isolation and patch runner
│   ├── ai/             # Core LLM token management layer
│   ├── providers/      # Direct API clients (Anthropic, OpenAI, etc.)
│   ├── context/        # Structural prompt assembly and compression
│   ├── git/            # Local Git interaction plumbing
│   ├── mcp/            # Stdio-based MCP server abstractions
│   ├── hooks/          # Lifecycle triggers and automation hooks
│   └── ui/             # Bubble Tea views and Lip Gloss styles
└── pkg/                # Reusable, non-domain utility packages

```

**Architectural Rule:** Internal domain boundaries are strictly encapsulated. Exposing interfaces to external tools via protocols is treated as a secondary, optional interface layer.

⸻

## Core Systems

### 1. Graph Engine

Derived from the Lea reasoning philosophy.

**Purpose:** Higher-order repository compression.
**Main Role:** Establish deep structural metadata maps *before* downstream LLMs perform expensive context reads.

**Responsibilities:**

* Comprehensive file-to-module directory map
* Granular symbol topology (functions, structs, classes, interfaces)
* Inter-file import dependency graphs
* Multi-directional call graphs
* State mutation maps (tracking where data changes)
* **Input:** Raw local repository files.
* **Output:** High-density structured graph cache.
* **Storage Path:** `.izen/graph.cache.v1`
* **Why:** Eradicate context dumping and token waste via deterministic pruning.

#### Technical Solution

Izen implements raw **Tree-sitter** bindings for direct AST (Abstract Syntax Tree) walking and multi-language symbol extraction.

* **Reason:** High performance, language-agnostic primitives.
* **Future:** Hot-pluggable language-specific extension drivers.

⸻

### 2. Retrieval Engine (Monolith & Fallback Specification)

**Purpose:** Resilient, multi-tiered codebase discovery.

To achieve a true **Zero-Dependency Monolith** without violating the `<10ms` startup goal, Izen encapsulates the Rust-powered **Lynx** discovery engine as an embedded binary using `go:embed`.

#### Non-Blocking Async Lifecycle

1. **Immediate Boot (<5ms):** The Main Goroutine initialises and renders the Bubble Tea TUI instantly.
2. **Async Background Daemon:** A concurrent background worker unpacks the embedded `lx` binary to `.izen/bin/` (if missing) and spins it up as a long-lived Model Context Protocol background daemon (`lx mcp`).
3. **Lazy Loading Strategy:** The heavy semantic model (`bge-small-en-v1.5`) is either warmed up silently in the background or loaded lazily upon the very first user query requiring semantic depth, saving critical system RAM during idle modes.

#### Resilient Fallback Middleware (The Fallback Pyramid)

When the core reasoning loop requests data coordinates, the Retrieval Engine executes a strict Chain of Responsibility pattern:

```text
[Discovery / Investigation Query]
               │
               ├──> Tier 1: Exact Symbol Resolution (Go Native AST / Raw Tree-sitter Cache) -> Latency: <1ms
               │     │ (Fallback if no direct symbol or exact match exists)
               │     ▼
               ├──> Tier 2: Lynx Hybrid Search (Tantivy BM25 + FastEmbed RAM Daemon)        -> Latency: <50ms
               │     │ (Fallback if Lynx is warming up, Out-of-Memory occurs, or parsing un-structured files)
               │     ▼
               └──> Tier 3: Textual Brute-Force (Embedded ripgrep (rg) / Native grep / glob / read)

```

* **ripgrep:** Raw fast textual fallback for unstructured formats, log files, configuration blocks, or malformed source trees.
* **glob/read:** Native fallback systems to inspect physical directory trees and read direct text blocks safely.

⸻

### 3. Context Engine

**Purpose:** Compress relevant source context down to the highest possible informational density.

**Responsibilities:**

* Isolate contextually relevant files and exact symbol code blocks
* Slice structural graph components based on topological proximity
* Inline dynamic Git diff states directly into the context window
* Merge compiler, linter, or test runtime errors with structural symbols
* **Output:** A hyper-minimal, high-signal structured prompt payload.
* **Goal:** Maximise the $\text{Signal} / \text{Token}$ ratio.

#### Technical Solution

A Go-native context builder that fuses outputs from the Graph Engine, Retrieval Engine, and local Git engine before compilation into the active prompt.

⸻

### 4. Execution Engine

Derived from the Lea reasoning philosophy.

**Purpose:** Secure, automated, and reversible workspace mutations.

**Responsibilities:**

* Isolated shell command orchestration
* Parallel test runner integration
* Automation hook triggers
* Structural patch application and complete transactional rollbacks
* Runtime sandbox constraint enforcement

#### Technical Solution

* **`os/exec`:** Core process runner configured with strict timeout context control.
* **Sandbox Policy Enforcement:** Active interceptors blocking destructive or irreversible commands (e.g., untrusted destructive `rm`, forced hard resets, unverified remote script downloads).
* **Patch Architecture:** Local state patches are serialized under `.izen/patches/`, providing native multi-step `undo` capabilities.

⸻

### 5. Session Engine

**Purpose:** Maintain structural continuity throughout multi-turn developer objectives.

**Responsibilities:**

* Active operational objective tracking
* Current sub-mode states
* Structural assumptions and working theories verified by the model
* Pending human verification checkpoints
* Automated state checkpoints
* **Storage Path:** Local `.izen/session.json`
* **Critical Guardrail:** This is not a generic conversation history log. It is a highly optimized, short-lived transactional representation of active operational states.

⸻

### 6. Git Engine

Git-native by design.

**Responsibilities:**

* Working tree status and untracked file monitoring
* Granular diff parsing
* Automated atomic checkpoints before modifications
* Safety undo rollbacks via temporary branches
* Active branch and remote structural context tracking
* High-context semantic commit message generation

#### Technical Solution

Leverages pure Go-native Git library integrations (with native CLI tool bindings as an immediate high-speed fallback).

⸻

### 7. Model Context Protocol (MCP) Layer

**Status:** Optional; Non-Core.
**Purpose:** Standardized external integration gateway.
**Storage Path:** Local configurations managed at `~/.izen/mcp/`.
**Examples:** Upstream integrations targeting GitHub issues, Jira tickets, or Linear workspaces.
**Rules:** All external tool schemas must be explicitly declared, strictly scoped, and immediately revocable by the user.

⸻

## Persistence Layer Specification

### Global State Space (`~/.izen/`)

```text
~/.izen/
├── izen.conf.yml       # Global configurations and model bindings
├── AGENTS.md           # Local agent capability documentation
├── mcp/                # External MCP integration manifests
└── cache/              # Shared multi-project system caches

```

### Project-Local State Space (`.izen/`)

```text
.izen/
├── bin/                # Unpacked embedded binaries (e.g., lx)
├── session.json        # Short-lived active mission state
├── plan.json           # Multi-step execution roadmap
├── graph.cache.v1      # High-density structural Tree-sitter graph cache
├── history.md          # Local agent audit trail and logs
├── input.history       # Local user CLI input ring buffer
├── checkpoints/        # Atomic system snapshots for easy recovery
├── investigations/     # Diagnostic readouts from /investigate runs
└── patches/            # Generated source code change blocks

```

⸻

## Token Optimization Strategy

System compute resources and model context bounds are gated using a strict tiered operational policy:

### Tier 0: Ambient Mode

* **Context Condition:** Greetings or simple general questions unrelated to the workspace (e.g., *"Hi"*, *"Explain a closure"*).
* **Machinery:** No repository indexing, no tool invocation, zero graph analysis overhead.

### Tier 1: Informational Discovery

* **Context Condition:** Simple symbol lookups or structural tracking questions.
* **Machinery:** Read-only access targeting the local Graph Engine. Immediate resolution bypassing deep LLM scans.

### Tier 2: Engineering Execution

* **Context Condition:** Feature implementations, code modifications, or code reviews.
* **Machinery:** Local Graph Engine paired with tightly targeted code block reads.

### Tier 3: Autonomous Investigation

* **Context Condition:** Debugging regressions, fixing compiler errors, diagnosing CI/CD runtime breaks.
* **Machinery:** Full Graph Engine + Lynx Hybrid Vector Search + Automated Execution Loops.

**Operational Rule:** *Simple inputs must never activate complex machinery.*

⸻

## Implementation Phases

### Phase 1 — Core Foundation (Complete)

* **Goal:** Build a stable, high-performance runtime core.
* **Deliverables:** CLI command routing bootstrap, YAML configuration loader, runtime mode resolvers, transactional Session Engine, basic provider abstractions, and core Bubble Tea TUI view loops.
* **Output:** A highly responsive, minimal runnable Izen architecture.

### Phase 2 — Graph Core (Complete)

* **Goal:** Establish concrete repository structural comprehension.
* **Deliverables:** Native Tree-sitter bindings integration, parallel symbol indexing pipelines, structural file relationship graphs, import tracking matrices, and serializable local graph caching layers.
* **Output:** A high-speed, token-saving repository structural map.

### Phase 3 — Retrieval Layer (Complete)

* **Goal:** Implement resilient multi-tier information retrieval.
* **Deliverables:** Internal graph query router, strict Fallback Pyramid middleware (Graph $\rightarrow$ Ripgrep $\rightarrow$ Glob $\rightarrow$ Read), high-speed targeted file readers, and automated confidence scoring engines.
* **Output:** A resilient codebase discovery and retrieval pipeline.

### Phase 4 — Execution Layer (Complete)

* **Goal:** Controlled workspace mutation mechanics.
* **Deliverables:** Secure `os/exec` command runner isolation, automated test suite runners, standardized patch generators, unified transactional rollbacks, and atomic state checkpointing.
* **Output:** Safe, highly audited local codebase manipulation.

### Phase 5 — Context Engine (Complete)

* **Goal:** Peak AI token efficiency.
* **Deliverables:** Intelligent context slicing logic, graph compression algorithms, live Git diff injection mechanisms, and compilation/runtime error matching pipelines.
* **Output:** Minimal, high-signal structured prompt injection.

### Phase 6 — Investigate Mode (In Progress)

* **Goal:** Deep autonomous diagnostic loops powered by the Lea core.
* **Deliverables:**
* **Hypothesis-Evidence Loop:** Automated state machine allowing Lea to state a debugging theory, execute specific targeted search tiers, read evidence, and re-evaluate loops.
* **Proximity Context Slicing:** Automated extraction of localized physical tokens surrounding stack traces or identified runtime exceptions.
* **Automated Test Iteration:** Tight loops checking runtime states via the Execution Engine, analyzing `stderr`, and modifying the active context space to narrow down faulty components.


* **Output:** A powerful, deterministic, and non-hallucinatory debugging system.

### Phase 7 — Lynx Monolith Integration & Deep Semantic

* **Goal:** Seamless embedded advanced search capabilities with zero startup overhead.
* **Deliverables:**
* **Static Binary Embedding:** Implement `go:embed bin/lx` workflows paired with localized runtime auto-unpack routines.
* **RAM-Daemon Controller:** Long-lived process life cycle management inside Go to run, query, and cleanly terminate Lynx as an background stdio MCP tool.
* **Semantic Escalation:** Programmatic triggers elevating queries to Lynx Hybrid Search (Tantivy + FastEmbed) only when Graph or textual confidence matches fall below specific metrics.
* **Advanced Type & Mutation Tracing:** Blending Lynx coordinates with Go-native `go/ast` and `go/types` libraries to evaluate structural cross-file impact zones.


* **Output:** Production-grade, single-file distribution containing deep offline vector semantic search.

### Phase 8 — Review Mode, Risk Audit & MCP Ecosystem

* **Goal:** Comprehensive code assessment and systemic ecosystem maturation.
* **Deliverables:**
* **Review Engine:** Deep Git Diff impact radius analyzers evaluating the systemic blast radius of incoming pull requests or active local patches.
* **Risk Audit Sandbox:** Pre-flight AST validation checkers evaluating agent patches against dangerous syntactic behavior or system-level access violations.
* **MCP Gateway:** Unified external integration points hooking into tracking environments (e.g., Jira, GitHub Issues) to automatically map remote issues to exact code coordinates via Lynx.


* **Output:** An enterprise-ready, completely isolated, and highly resilient AI development platform.

⸻

## Final Principle

Every subsystem added or line of code written within this engine must strictly adhere to this linear architectural progression:

$$\text{Clarity} \rightarrow \text{Structure} \rightarrow \text{Retrieval} \rightarrow \text{Execution} \rightarrow \text{Optimization} \rightarrow \text{Scale}$$

**Never reverse this order.** If optimizing performance at Tier 2 ever threatens core operational stability or UI responsiveness, the engine must immediately fall back gracefully to a simpler, deterministic text layer to defend the developer's raw terminal experience.



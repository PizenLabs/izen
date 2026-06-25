# TECHSTACK.md

## Izen Technical Stack

This document defines the technical implementation of Izen.

It exists to answer:

* what is built
* how it is built
* why this stack is chosen
* how each subsystem works
* what phases should be followed

This is the implementation contract behind the philosophy.

⸻

Core Stack

Language

Go

Primary language for Izen.

Reason:

* fast startup
* single binary
* strong concurrency
* excellent CLI ecosystem
* efficient file IO
* low memory usage
* predictable deployment

Go is chosen because Izen is:

* CLI-first
* local-first
* workflow-heavy
* execution-heavy

Not ML-heavy.

⸻

UI Layer

Bubble Tea

Used for terminal application flow.

Responsibilities:

* mode switching
* interactive prompts
* workflow rendering
* progress streaming
* confirmations
* audits

Purpose:

human-friendly CLI UX.

⸻

Lip Gloss

Used for styling.

Responsibilities:

* colors
* layouts
* hierarchy
* visual clarity

Purpose:

improve readability.

⸻

Configuration Layer

YAML

Used for:

~/.izen/izen.conf.yml

Purpose:

global configuration.

Stores:

* models
* providers
* execution policies
* fallback behavior
* MCP settings

Why YAML:

* human-readable
* flexible
* easy to maintain

⸻

AI Layer

Provider abstraction

No provider lock-in.

Interface:

ModelProvider

Responsibilities:

* prompt execution
* streaming responses
* token accounting
* fallback switching

Supported:

* OpenAI
* Anthropic
* OpenRouter
* local models

Future:

* Gemini
* DeepSeek
* custom providers

Principle:

models are replaceable.

⸻

Internal Architecture

Izen is a modular monolith.

Structure:

izen/
├── cmd/
├── internal/
│   ├── config/
│   ├── modes/
│   ├── session/
│   ├── graph/
│   ├── retrieval/
│   ├── execution/
│   ├── ai/
│   ├── providers/
│   ├── context/
│   ├── git/
│   ├── mcp/
│   ├── hooks/
│   └── ui/
└── pkg/

Rule:

internal systems are modular.

External protocol is optional later.

⸻

Core Systems

⸻

1. Graph Engine

Derived from Lea philosophy.

Purpose:

repository compression.

Main role:

build structural understanding before model reads.

Responsibilities:

* file map
* symbol map
* import graph
* call graph
* mutation map
* dependency map

Input:

repository files

Output:

structured graph cache

Storage:

.izen/graph.cache.v1

Why:

reduce token waste.

⸻

Solution

Use:

tree-sitter

For:

* parsing
* symbol extraction
* AST walking

Reason:

language-agnostic

Future:

language plugins.

⸻

2. Retrieval Engine

Purpose:

multi-layer repository retrieval.

Order:

Graph
→ Lynx
→ glob
→ rg
→ grep
→ read

Responsibilities:

* fast symbol lookup
* semantic escalation
* fallback resilience

Goal:

semantic-first, text-resilient

⸻

Solutions

Internal graph lookup

Fast path.

⸻

Lynx integration

Deep semantic path.

Responsibilities:

* advanced symbol tracing
* precise reference search
* mutation tracing

Only activated when graph confidence is low.

⸻

ripgrep

Raw fast fallback.

Used for:

* configs
* logs
* malformed files

⸻

glob

File discovery.

⸻

grep

Emergency text fallback.

⸻

3. Context Engine

Purpose:

compress useful context for AI.

Responsibilities:

* select relevant files
* select relevant symbols
* compress graph slices
* merge git diff
* merge errors

Output:

minimal structured prompt.

Goal:

maximize signal/token.

⸻

Solutions

Go-native context builder.

Uses:

* graph engine
* retrieval engine
* git engine

⸻

4. Execution Engine

Derived from Lea philosophy.

Purpose:

safe execution.

Responsibilities:

* shell commands
* test runs
* hook execution
* patch apply
* rollback
* sandbox checks

⸻

Solutions

os/exec

Core command runner.

⸻

sandbox policy

Before dangerous execution.

Checks:

* rm
* force reset
* external scripts

⸻

patch system

Stores:

.izen/patches/

Supports:

undo.

⸻

5. Session Engine

Purpose:

workflow continuity.

Responsibilities:

* active objective
* active mode
* assumptions
* pending questions
* checkpoints

Storage:

.izen/session.json

Important:

not memory.

Short-lived operational state only.

⸻

6. Git Engine

Git-native by default.

Responsibilities:

* status
* diff
* checkpoint
* undo
* branch context
* commit suggestions

Solutions:

Go Git integration.

Possible:

go-git

Fallback:

native git commands.

⸻

7. MCP Layer

Optional.

Not core.

Purpose:

external integrations.

Structure:

~/.izen/mcp/

Examples:

* GitHub
* Notion
* Jira

Rules:

* explicit
* scoped
* revocable

⸻

Persistence Layer

Global:

~/.izen/
├── izen.conf.yml
├── AGENTS.md
├── mcp/
└── cache/

⸻

Project:

.izen/
├── session.json
├── plan.json
├── graph.cache.v1
├── history.md
├── input.history
├── checkpoints/
├── investigations/
└── patches/

⸻

Token Optimization Strategy

Priority:

⸻

Tier 0

No repo.

Example:

Hi

No graph.

No tools.

⸻

Tier 1

Graph only.

Used for:

* symbol lookup
* reference lookup

⸻

Tier 2

Graph + targeted reads.

Used for:

* implementation
* review

⸻

Tier 3

Graph + Lynx + execution.

Used for:

* investigations
* regressions
* CI failures

Rule:

simple input should not activate complex machinery.

⸻

Phases

⸻

Phase 1 — Core Foundation

Goal:

build stable core.

Tasks:

* CLI bootstrap
* config loader
* mode resolver
* session engine
* provider abstraction
* Bubble Tea integration

Output:

minimal runnable Izen.

⸻

Phase 2 — Graph Core

Goal:

repository understanding.

Tasks:

* tree-sitter integration
* symbol indexing
* file graph
* import graph
* graph cache

Output:

token-efficient repository map.

⸻

Phase 3 — Retrieval Layer

Goal:

structured retrieval.

Tasks:

* graph lookup
* fallback chain
* targeted read
* confidence scoring

Output:

stable retrieval pipeline.

⸻

Phase 4 — Execution Layer

Goal:

safe mutation.

Tasks:

* shell runner
* test runner
* patch generation
* rollback
* checkpoints

Output:

controlled execution.

⸻

Phase 5 — Context Engine

Goal:

AI efficiency.

Tasks:

* context slicing
* graph compression
* git merge
* error merge

Output:

minimal prompts.

⸻

Phase 6 — Investigate Mode

Goal:

deep debugging.

Tasks:

* hypothesis loop
* repeated tests
* narrowing logic
* evidence collection

Output:

strong debugging workflow.

⸻

Phase 7 — Lynx Deep Semantic

Goal:

advanced intelligence.

Tasks:

* semantic escalation
* mutation tracing
* exact call graph resolution

Output:

precision retrieval.

⸻

Phase 8 — Review + MCP

Goal:

ecosystem maturity.

Tasks:

* review engine
* risk audit
* MCP integrations

Output:

full production system.

⸻

Final Principle

Build in this order:

clarity
→ structure
→ retrieval
→ execution
→ optimization
→ scale

Never reverse this.

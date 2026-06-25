# Izen

## Izen is a human-centered coding intelligence system built by PizenLabs.

It is designed around one core belief:

AI should assist human judgment, not replace it.

Izen is not built to maximize autonomy.

It is built to maximize:

* clarity
* control
* trust
* reversibility
* efficiency

Priority order:

clarity > control > trust > speed

⸻

Philosophy

Izen follows these principles:

Human-centered

The human stays in control.

AI helps understand, plan, inspect, and execute — but does not become the owner of decision-making.

⸻

Minimal by default

Do not activate complexity unless needed.

Simple requests should remain simple.

Example:

Hi

should not trigger:

* repo scans
* graph building
* semantic indexing
* tool loading

Intent drives activation.

⸻

Local-first

Everything runs locally by default.

No cloud assumptions.

No hidden remote systems.

Local machine is the source of execution.

⸻

Security-aware

Nothing is trusted by default.

Every external system is:

* explicit
* scoped
* revocable

MCP is optional.

Never foundational.

⸻

Explicit over implicit

Izen must explain:

* what it is doing
* why it is doing it
* what strategy it is using

Example:

Semantic search failed.
Fallback: ripgrep.

This preserves trust.

⸻

Reversible by design

 meaningful mutation should be recoverable.

Examples:

* Git checkpoints
* patches
* audits
* history

Undo is a core primitive.

⸻

Core Idea

Most coding agents waste tokens by forcing models to discover context.

Izen reverses this.

Instead of:

Model -> Search -> Read -> Infer

Izen does:

Graph -> Slice -> Structure -> Model

Meaning:

Izen prepares context before the model sees it.

This improves:

* speed
* cost
* focus
* precision

⸻

Architecture

Izen is a modular monolith.

Not a protocol-first distributed system.

Not a giant monolith.

Structure:

Izen
├── Context Engine
├── Graph Engine
├── Mode Engine
├── Retrieval Engine
├── Execution Engine
└── Session Engine

⸻

Internal Engines

Context Engine

Builds minimal useful context.

Goal:

maximize signal per token.

⸻

Graph Engine

Derived from Lea philosophy.

Responsibilities:

* symbol maps
* imports
* call chains
* dependency relationships
* mutation paths

Purpose:

compress repository understanding.

Graph-first.

Not file-first.

⸻

Retrieval Engine

Multi-layer retrieval system.

Order:

Tier 1: Graph
Tier 2: Lynx semantic
Tier 3: Raw fallback

Detailed:

Graph
→ Lynx
→ glob
→ rg
→ grep
→ read

Rule:

semantic-first, text-resilient.

⸻

Execution Engine

Derived from Lea philosophy.

Responsibilities:

* shell execution
* hooks
* sandboxing
* audit
* rollback

Purpose:

execute safely.

⸻

Session Engine

Maintains workflow continuity.

Not long-term memory.

Stores:

* active objective
* active mode
* assumptions
* checkpoints
* unresolved questions

Purpose:

resume work safely.

⸻

Modes

Izen is mode-based.

Not prompt-based.

Modes define behavior.

Not personality.

Core modes:

/ask

Purpose:

* explain
* inspect
* understand

Rules:

read-only

⸻

/plan

Purpose:

* architecture
* migrations
* refactors

Rules:

no execution

⸻

/build

Purpose:

* implement
* refactor
* write tests

Rules:

controlled execution

⸻

/investigate

Purpose:

* unclear bugs
* failures
* regressions
* CI issues

Flow:

observe
→ hypothesize
→ test
→ narrow
→ verify
→ propose

Rules:

bounded loops allowed.

Main principle:

understand before acting.

⸻

/review

Purpose:

* audit changes
* detect risks
* inspect regressions

⸻

Lazy Activation Model

Inspired by lazy-loading systems.

Only activate what is needed.

Examples:

Hi
→ minimal system
Is this function used?
→ graph only
Fix this bug
→ graph + execution
Why is CI failing?
→ graph + semantic + execution + history

Rule:

simple input must not trigger complex machinery

⸻

Token Philosophy

Token efficiency is architecture quality.

Formula:

signal / token

Higher is better.

Goal:

never send unnecessary code.

Prefer:

symbol summaries
call chains
dependency slices

Over:

full file dumps
full repo scans

⸻

Configuration

Global config:

~/.izen/
├── izen.conf.yml
├── AGENTS.md
├── mcp/
└── cache/

Purpose:

izen.conf.yml

Global behavior configuration.

Examples:

* models
* execution policy
* fallback behavior

⸻

AGENTS.md

Persistent human preferences.

Not mode logic.

Example:

Prefer Go idioms.
Avoid unnecessary abstractions.
Favor readability.

⸻

mcp/

Optional external integrations.

Always explicit.

⸻

Project State

Project-local:

.izen/
├── session.json
├── plan.json
├── graph.cache.v1
├── history.md
├── input.history
├── checkpoints/
├── investigations/
└── patches/

Purpose:

session.json

Current workflow state.

⸻

plan.json

Current plan state.

⸻

graph.cache.v1

Fast warm-start graph cache.

Inspired by aider tags cache.

⸻

history.md

Human-auditable conversation history.

Not automatic model input.

⸻

input.history

Command history.

Fast reuse.

⸻

checkpoints/

Git recovery points.

⸻

investigations/

Evidence storage.

Logs, traces, notes.

⸻

patches/

Generated patches before apply.

Supports rollback.

⸻

What Izen is NOT

Izen is not:

* a black-box autonomous agent
* a prompt collection
* a cloud-dependent coding system
* an uncontrolled MCP hub
* a file-dumping token burner

⸻

Long-term Vision

Build a complete modular human-centered coding intelligence ecosystem.

Ecosystem:

PizenLabs
├── Izen
├── Lynx
└── Optional Extensions

Goal:

stable, efficient, local-first coding intelligence.

Built for trust.

Built for clarity.

Built for humans.

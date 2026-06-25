
# Lynx: Discovery Engine for AI-Native Software Engineering

## Vision

Lynx is not a graph engine.

Lynx is not a code intelligence platform.

Lynx is not an architecture analysis tool.

Lynx exists for a single purpose:

> Convert human intent into precise repository coordinates.

Lynx is the Discovery Layer of the PizenLabs ecosystem.

Its responsibility is to answer:

```text
Where should I look?
```

Lea then answers:

```text
What happens if I change it?
```

Together they form a complete AI-native software reasoning pipeline.

---

# Ecosystem Position

```text
Human Request
      │
      ▼
    Lynx
 Discovery Layer
      │
      ▼
 Exact Symbols
      │
      ▼
     Lea
 Structural Layer
      │
      ▼
 Context Package
      │
      ▼
 AI Agent
```

---

# Design Principles

## Principle 1: Discovery Only

Lynx discovers.

Lynx does not reason.

Examples:

### Valid

```text
authentication flow
```

↓

```text
internal/auth/service.go
func:internal/auth:Login
```

---

### Invalid

```text
authentication flow
```

↓

```text
Impact Radius: 24 symbols
```

Impact analysis belongs to Lea.

---

## Principle 2: Speed First

Search latency must feel instantaneous.

Target:

```text
Cold Search    < 100 ms
Warm Search    < 10 ms
```

Lynx should remain CPU-first and local-first.

No GPU dependency.

No external services.

No cloud infrastructure requirements.

---

## Principle 3: Token Efficiency

The purpose of Lynx is not finding everything.

The purpose is finding the smallest amount of information necessary.

A successful query should return:

```text
1 file
3 functions
2 symbols
```

instead of:

```text
40 files
5000 lines
```

---

## Principle 4: Deterministic Outputs

Queries should consistently return the same results.

Avoid black-box ranking systems whenever possible.

Prefer explainable ranking mechanisms.

---

# Core Responsibilities

## Semantic Discovery

Natural language search.

Examples:

```text
authentication flow
```

```text
jwt validation
```

```text
user registration
```

---

## Symbol Discovery

Resolve exact repository symbols.

Examples:

```text
Login
```

↓

```text
func:internal/auth:Login
```

---

## Code Chunk Retrieval

Return minimal code snippets relevant to the query.

Example:

```text
How are JWTs generated?
```

↓

```text
TokenService.Generate()
```

---

## Documentation Discovery

Search:

* Markdown
* ADRs
* RFCs
* READMEs
* Design Documents

---

## Configuration Discovery

Search:

* YAML
* TOML
* JSON
* ENV files
* CI/CD configs

---

# Responsibilities Explicitly Excluded

Lynx must never implement:

* Call Graph Analysis
* Impact Analysis
* Dependency Analysis
* Architecture Validation
* Layer Detection
* Structural Traversal
* Execution Flow Analysis

These belong exclusively to Lea.

---

# Technology Stack

## Language

Rust

Reasoning:

* Memory safety
* Excellent performance
* Portable binaries
* Strong ecosystem
* Easy static distribution

---

## Storage Layer

### Tantivy

Primary search engine.

Purpose:

* Full-text indexing
* BM25 retrieval
* Fast local search

Benefits:

* Native Rust
* Lightweight
* High performance
* No external dependencies

---

## Parser Layer

### Tree-sitter

Purpose:

* Symbol extraction
* Chunk boundaries
* Language awareness

Supported Languages:

* Go
* Rust
* TypeScript
* JavaScript
* Python
* Java
* C#
* C++
* Kotlin

Future languages can be added through Tree-sitter grammars.

---

## Embedding Layer

### FastEmbed

Purpose:

Semantic retrieval.

Benefits:

* ONNX runtime
* CPU optimized
* Lightweight deployment

Candidate Models:

```text
bge-small-en-v1.5
```

or

```text
nomic-embed-code
```

---

## Hybrid Ranking

Combine:

```text
Semantic Search
+
Lexical Search
```

using:

```text
Reciprocal Rank Fusion (RRF)
```

---

# Search Pipeline

```text
User Query
      │
      ▼
 Query Analysis
      │
      ▼
 BM25 Search
      │
      ├─────────────┐
      ▼             │
 Embedding Search   │
      │             │
      └──────┬──────┘
             ▼
      Rank Fusion
             ▼
      Re-Ranking
             ▼
       Top Results
```

---

# Query Classification

Before retrieval, classify queries.

## Symbol Query

Example:

```text
LoginHandler
```

Prioritize lexical retrieval.

---

## Natural Language Query

Example:

```text
how authentication works
```

Prioritize semantic retrieval.

---

## Hybrid Query

Example:

```text
jwt token validation
```

Balance both retrievers.

---

# Ranking Signals

## Definition Boost

Prefer symbol definitions over references.

Example:

```go
func Login(...)
```

should rank higher than:

```go
Login(...)
```

---

## Identifier Matching

Boost:

```text
Login
```

against:

```text
LoginHandler
```

```text
UserLoginService
```

```text
login_user
```

---

## File Coherence

When multiple chunks match inside the same file:

boost file relevance.

---

## Noise Suppression

Downrank:

* testdata
* fixtures
* examples
* generated code
* vendor
* node_modules

---

# MCP Responsibilities

Lynx should expose a minimal MCP interface.

## search

```text
search(query)
```

Returns:

* files
* chunks
* symbols

---

## find_related

```text
find_related(file, line)
```

Returns:

similar implementations.

---

## resolve_symbol

```text
resolve_symbol(name)
```

Returns:

exact symbol identifiers.

---

# CLI Responsibilities

Examples:

```bash
lynx search "authentication flow"
```

---

```bash
lynx search "jwt validation"
```

---

```bash
lynx resolve Login
```

---

```bash
lynx related internal/auth/service.go:42
```

---

# Future Extensions

Potential future additions:

* Multi-repository search
* Monorepo indexing
* Ownership metadata
* Git-aware ranking
* Commit-history signals
* Test relevance ranking
* Documentation relationship discovery

These additions must remain within the Discovery domain.

---

# Success Metrics

Lynx succeeds when:

* Agents stop using grep as their primary discovery mechanism.
* Relevant code is found in milliseconds.
* Token consumption decreases significantly.
* Exact symbols can be resolved reliably.
* Discovery becomes predictable and repeatable.

Lynx does not need to understand architecture.

Lynx only needs to find the right place to start.

---

# Relationship With Lea

Lynx and Lea are complementary systems.

```text
Lynx
-----
Input:
    Human Intent

Output:
    Exact Symbols
```

```text
Lea
----
Input:
    Exact Symbols

Output:
    Structural Understanding
```

The boundary must remain strict.

Lynx discovers.

Lea reasons.

Agents execute.

---

# Long-Term Mission

Build the fastest and most reliable local code discovery engine for AI agents.

Lynx should become the entry point into a codebase.

Lea should become the structural memory of a codebase.

Together they provide a complete foundation for AI-native software engineering.

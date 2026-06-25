
# Lynx: Technical Architecture & Retrieval Specification

## Overview

Lynx is the Discovery Layer of the PizenLabs ecosystem.

Its mission is not to explain software systems.

Its mission is to transform vague human intent into precise software coordinates.

Lynx is responsible for answering questions such as:

* "Where is authentication implemented?"
* "How are sessions created?"
* "Which symbol handles JWT validation?"
* "Where is MFA logic located?"

The output of Lynx is consumed by Lea, the Structural Intelligence Layer.

```text
Natural Language Query
          │
          ▼
        Lynx
(Discovery & Retrieval)
          │
          ▼
     Symbol IDs
          │
          ▼
         Lea
(Structural Reasoning)
          │
          ▼
   AI Understanding
```

---

# Design Principles

## Rule 1: Discovery First

Lynx exists to discover relevant symbols.

It does not perform architectural reasoning.

It does not calculate impact analysis.

It does not generate execution flow explanations.

These responsibilities belong to Lea.

---

## Rule 2: Symbols Over Snippets

Traditional code search systems return text chunks.

Lynx returns precise symbol coordinates whenever possible.

Preferred output:

```json
{
  "symbol_id": "func:internal/auth:Login",
  "score": 0.98
}
```

Instead of:

```json
{
  "file": "auth/login.go",
  "line": 124,
  "content": "..."
}
```

This allows downstream tools to perform deterministic graph traversal.

---

## Rule 3: Local-First

All retrieval operations must execute locally.

No cloud infrastructure is required.

No external APIs are required.

No GPU is required.

---

## Rule 4: Deterministic Before Probabilistic

When exact structural information exists, Lynx must prefer deterministic retrieval over semantic approximation.

Priority order:

```text
Exact Symbol Index
       ↓
BM25 Retrieval
       ↓
Vector Search
```

---

# Core Architecture

```text
                   User Query
                        │
                        ▼
              Query Classification
                        │
        ┌───────────────┼───────────────┐
        ▼                               ▼
 Exact Symbol Lookup             Hybrid Retrieval
        │                               │
        │                     ┌─────────┴─────────┐
        │                     ▼                   ▼
        │                 BM25 Search      Vector Search
        │                     │                   │
        └─────────────┬───────┴───────┬───────────┘
                      ▼               ▼
                 Rank Fusion (RRF)
                          │
                          ▼
                Heuristic Boost Layer
                          │
                          ▼
                 Symbol Resolution
                          │
                          ▼
                   Discovery Results
```

---

# Technology Stack

| Layer           | Technology                  | Purpose                                 |
| --------------- | --------------------------- | --------------------------------------- |
| Core Language   | Rust                        | Memory safety, concurrency, portability |
| Async Runtime   | Tokio                       | Parallel retrieval execution            |
| Search Engine   | Tantivy                     | BM25 indexing and retrieval             |
| Parser Engine   | Tree-sitter                 | Language-aware syntax parsing           |
| Vector Engine   | FastEmbed (Default)         | Local semantic embeddings               |
| Storage         | Tantivy Index + Local Cache | Persistent retrieval index              |
| Hashing         | BLAKE3                      | Stable chunk and symbol identifiers     |
| Serialization   | Serde                       | Data interchange                        |
| MCP Integration | Native Rust MCP Server      | Agent interoperability                  |

---

# Embedding Abstraction Layer

Lynx must never depend on a single embedding model.

Embedding providers are interchangeable through a common interface.

```rust
pub trait Embedder {
    async fn embed(
        &self,
        text: &str,
    ) -> anyhow::Result<Vec<f32>>;
}
```

Supported implementations may include:

* FastEmbed
* BGE
* Nomic
* Ollama Embeddings
* OpenAI Embeddings
* Voyage Embeddings

The retrieval pipeline must remain unchanged regardless of the provider.

---

# Symbol Index

A dedicated symbol index exists alongside chunk indexes.

The purpose is instant exact-match retrieval.

```rust
pub struct SymbolRecord {
    pub symbol_id: String,
    pub symbol_name: String,
    pub file_path: String,
    pub start_line: usize,
    pub end_line: usize,
}
```

Examples:

```text
func:internal/auth:Login

func:internal/auth:ValidateJWT

method:internal/auth:AuthService.Login
```

Exact symbol lookup bypasses all ranking systems.

Expected complexity:

```text
O(1)
```

---

# Query Classification

Lynx uses deterministic query classification.

No machine learning classifier is required.

## Symbol Query

Examples:

```text
AuthService

Login

ValidateJWT

func:internal/auth:Login
```

Detection signals:

* `func:`
* `type:`
* `method:`
* `.`
* `::`

Execution path:

```text
Query
  ↓
Symbol Index
```

---

## Natural Language Query

Examples:

```text
how authentication works

how session management is implemented

jwt validation flow
```

Execution path:

```text
Query
  ↓
BM25 + Vector Search
```

---

## Hybrid Query

Examples:

```text
jwt middleware validation

auth service login

user repository cache
```

Execution path:

```text
Query
  ↓
BM25 + Vector Search
  ↓
Balanced Fusion
```

---

# Hybrid Retrieval Algorithms

## BM25 Retrieval

Lexical retrieval is performed using Tantivy BM25.

For a query:

```math
Q = {q_1, q_2, ..., q_n}
```

The BM25 score is:

```math
Score(D,Q)
=
\sum_{i=1}^{n}
IDF(q_i)
\cdot
\frac{
f(q_i,D)(k_1+1)
}{
f(q_i,D)
+
k_1
\left(
1-b+b\frac{|D|}{avgdl}
\right)
}
```

Parameters:

```text
k1 = 1.2
b  = 0.75
```

---

## Semantic Retrieval

Dense vector similarity uses cosine similarity.

```math
Similarity(Q,D)
=
\frac{
E_Q \cdot E_D
}{
||E_Q|| ||E_D||
}
```

Where:

* E_Q = query embedding
* E_D = document embedding

---

# Reciprocal Rank Fusion (RRF)

Lexical and semantic streams are fused using RRF.

```math
RRF(d)
=
\alpha
\frac{1}{k + Rank_{Lexical}(d)}
+
\beta
\frac{1}{k + Rank_{Semantic}(d)}
```

Constants:

```text
k = 60
```

---

## Dynamic Weighting

### Symbol Query

```text
α = 0.90
β = 0.10
```

### Hybrid Query

```text
α = 0.50
β = 0.50
```

### Semantic Query

```text
α = 0.10
β = 0.90
```

---

# Structural Chunking

Files are parsed through Tree-sitter.

Chunks are created along syntax boundaries.

Examples:

* Function definitions
* Method implementations
* Struct definitions
* Interface definitions
* Trait implementations
* Class definitions

Never:

* Fixed line windows
* Arbitrary token windows

---

```rust
pub struct CodeChunk {
    pub id: String,
    pub file_path: String,
    pub start_line: usize,
    pub end_line: usize,
    pub raw_content: String,
    pub symbols_defined: Vec<String>,
}
```

---

# Signal Boosting Layer

After RRF fusion, candidates pass through heuristic ranking.

## Definition Boost

Definition matches receive additional weight.

```rust
fn apply_definition_boost(
    score: f32,
    is_definition: bool,
) -> f32 {
    if is_definition {
        score * 1.5
    } else {
        score
    }
}
```

---

## Noise Suppression

Generated files receive severe penalties.

Examples:

```text
vendor/
node_modules/
dist/
build/
generated/
.pb.go
```

Implementation:

```rust
score *= 0.05
```

---

# Discovery Result Format

The final output should prioritize symbols.

```rust
pub struct DiscoveryResult {
    pub symbol_id: String,
    pub score: f32,
    pub file_path: String,
    pub start_line: usize,
    pub end_line: usize,
}
```

Example:

```json
[
  {
    "symbol_id": "func:internal/auth:Login",
    "score": 0.98
  },
  {
    "symbol_id": "func:internal/auth:ValidateJWT",
    "score": 0.94
  }
]
```

---

# Integration with Lea

Lynx does not perform reasoning.

Lynx discovers.

Lea understands.

Workflow:

```text
User:
"How authentication works?"
          │
          ▼
        Lynx
          │
          ▼
func:internal/auth:Login
func:internal/auth:ValidateJWT
func:internal/auth:CreateSession
          │
          ▼
         Lea
          │
          ├── structure
          ├── trace
          ├── flow
          ├── impact
          └── context
          │
          ▼
      AI Agent
```

---

# Ecosystem Boundary

## Lynx Responsibilities

✓ Semantic Retrieval

✓ Lexical Retrieval

✓ Symbol Discovery

✓ Coordinate Resolution

✓ Ranking

✓ Retrieval Optimization

✗ Graph Traversal

✗ Impact Analysis

✗ Architectural Reasoning

✗ Context Compilation

✗ Call Graph Analysis

---

## Lea Responsibilities

✓ Graph Traversal

✓ Call Graph Analysis

✓ Execution Flow

✓ Impact Analysis

✓ Architecture Validation

✓ Context Assembly

✓ Structural Reasoning

✗ Embeddings

✗ Semantic Search

✗ Retrieval Ranking

✗ Vector Search

---

# Long-Term Vision

The PizenLabs ecosystem follows a strict two-stage intelligence model:

```text
Discovery
    ↓
  Lynx
    ↓
Coordinates
    ↓
  Lea
    ↓
Understanding
```

Lynx finds where knowledge lives.

Lea explains what that knowledge means.

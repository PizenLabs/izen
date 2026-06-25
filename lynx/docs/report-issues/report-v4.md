# Search Quality Report: Architectural Query Handling in Lynx

## 1. Summary

During code exploration integration testing on the `lynx` repository, the following architectural workflow query:

```bash
lx search "authentication flow"

```

returned predominantly noise—unrelated symbols with extremely low confidence scores ($1\% - 2\%$).

### Example Diagnostic Log Output

```text
STRUCT │ test_parse_go         │ Confidence: Low (0.02) │ Why: Semantic match, File coherence boost
METHOD │ Parser.parse_go       │ Confidence: Low (0.02) │ Why: Semantic match, File coherence boost
STRUCT │ CodeChunk             │ Confidence: Low (0.02) │ Why: Semantic match, File coherence boost
STRUCT │ EmbeddingCache        │ Confidence: Low (0.02) │ Why: Semantic match, File coherence boost
FUNC   │ extract_go_symbol_info│ Confidence: Low (0.01) │ Why: Semantic match, File coherence boost

```

### High-Level Assessment

While these structures exist inside the codebase, they bear zero relevance to the user's intent. Because the `lynx` codebase does not contain an business logic for user authentication, the vector retrieval pipeline falls back to weak semantic neighbors instead of safely reporting no matching features. This introduces cognitive noise and degrades developer trust in the search engine's reliability.

---

## 2. Problem Statement

The search engine currently processes all inputs through a uniform retrieval pipeline. It treats an **architectural workflow query** (e.g., `"authentication flow"`) as a **generic token/symbol retrieval request**:

$$\text{User Query} \longrightarrow \text{Embedding Extraction} \longrightarrow \text{Vector Space Search} \longrightarrow \text{Hybrid Reciprocal Rank Fusion (RRF)}$$

Because there is no intent classification or minimum confidence cutoff, the engine forces a match with nearby vectors in the code repository, presenting random structural components as low-confidence results.

### Core Issues Identified

* **No Match Redirection**: Weak associations are surfaced instead of an elegant "No relevant matches found" stdout termination.
* **Misleading Trace Information**: Low confidence metrics accompanied by generic structural boosts (`File coherence boost`) imply a false sense of loose validity.
* **Intent Homogenization**: Symbol lookups (`lx search "AuthService"`) and open-ended design intent lookups share the exact same query lifecycle.

---

## 3. Root Cause Analysis

The internal ranking subsystem lacks optimization for workspace limits:

1. **Unbounded Nearest Neighbor Traversal**: The K-Nearest Neighbors (KNN) search finishes its retrieval loop even if the highest cosine similarity score is negligible.
2. **Missing Confidence Floors**: The fusion engine does not invalidate rows scoring under a standardized baseline normalized between $[0.0, 1.0]$.
3. **Lack of Abstract Query Filtering**: High-level semantic abstractions are forced directly onto specialized syntactic identifiers parsed via Tree-sitter AST nodes.

---

## 4. Architectural Recommendations

### Recommendation 1: Normalized Confidence Threshold with Fuzzy Fallback

Introduce a static floor threshold to filter out low-confidence semantic matches.

```rust
// crates/lynx-core/src/ranking/mod.rs
pub const MIN_CONFIDENCE_THRESHOLD: f32 = 0.15;

```

* **Behavior Change**: Any output calculating below `0.15` is dropped immediately.
* **Fuzzy Fallback Mechanism**: If the result set is completely empty after applying the threshold constraint, `lx` gracefully degrades to a fast string-distance match (e.g., Jaro-Winkler or Levenshtein Distance) over the structural token registry to output a clean `"Did you mean?"` message.

### Recommendation 2: Lightweight Query Intent Classification

Differentiate incoming strings into a typed intent structure to route queries to specialized search loops.

```rust
// crates/lynx-core/src/classifier/mod.rs
pub enum QueryIntent {
    Symbol,        // Exact matches, e.g., "AuthService"
    Definition,    // Target signatures, e.g., "ParseJWT"
    Flow,          // Control path mapping, e.g., "authentication flow"
    Architecture,  // Inter-crate relationships, e.g., "request lifecycle"
    Semantic,      // Broad natural language, e.g., "how do we cache vectors"
}

```

> **Performance Constraint**: To honor the strict $<50\text{ms}$ execution limit on local architectures, intent classification must bypass complex LLM inference. Instead, it must utilize regex constraints, symbol syntax heuristics, and deterministic keyword grouping.

### Recommendation 3: Engine Separation (Symbol vs. Discovery)

```
                       ┌──────────────────────┐
                       │   lx search Input    │
                       └──────────┬───────────┘
                                  │
                     [ Query Intent Classifier ]
                                  │
         ┌────────────────────────┴────────────────────────┐
         ▼                                                 ▼
[ Symbol Search Engine ]                         [ Discovery Engine ]
 - Exact/Prefix Identifier Matching               - Semantic Hybrid Vector Chunking
 - Token-aware Metadata Tracing                   - Cross-Crate Dependency Graphs
 - 100% Deterministic Navigation                  - AST Flow Pruning & Context Bundling

```

* **Symbol Search Engine**: Preserves the existing high-efficiency exact mapping stack. Queries like `lx search "login"` bypass loose semantic lookups entirely, maintaining a deterministic $100\%$ confidence output.
* **Discovery Engine**: Dedicated to contextual workflows. It extracts multi-token concepts, utilizes dependency graphs from `lea`, and targets code chunks rather than individual symbol markers.

### Recommendation 4: Contextual Confidence Explanations

Upgrade the standard debug output format when low-scoring elements are explicitly displayed under user flags (e.g., `--verbose` / `--debug`):

```text
// Current Format
Confidence: Low (0.02)
Why: - Semantic match

// Proposed Format
Confidence: Low (0.02)
Warning: 
  - Zero lexical overlap found in current workspace.
  - Sourced via distant semantic neighbor clustering.

```

---

## 5. System Execution Roadmap

```
PHASE 1: SYMBOL NAVIGATION (Current Milestone)
  ├── Exact Symbol Verification [DONE]
  ├── Tree-sitter Identifier Extraction [DONE]
  └── Confidence-Based Threshold Filtering [IN PROGRESS]

PHASE 2: STRUCTURAL UNDERSTANDING (Lea Integration)
  ├── AST Control Flow Mapping
  ├── Cross-Crate Type Inference Tracking
  └── Codebase Dependency Graph Generation

PHASE 3: ARCHITECTURAL DISCOVERY
  ├── Multi-Signal Intent Classification
  ├── Path Impact Blast Radius Assessment
  └── Discovery Engine Isolation

PHASE 4: AGENT INTELLIGENCE (PizenLabs Integration)
  ├── Persistent Session Memory (Local SQLite/Tantivy)
  ├── Temporal Developer Preference Reasoning
  └── Cross-Repository Agent Knowledge Graphs

```

---

## 6. Current Assessment & Conclusion

* **Target Evaluation for Exact Symbol Searches**: **Excellent.** Searching for rigid identifiers yields clean, sub-30ms, production-ready indices.
* **Target Evaluation for Architectural Concept Exploration**: **Needs Improvement.** The underlying engine works properly, but architectural intent strings are routed through a search pipeline tuned for strict symbols.

### Final Takeaway

The immediate focus for the system core must remain on **Phase 1 Optimization**. Implementing `MIN_CONFIDENCE_THRESHOLD` and separating generic token matching from abstract semantic queries will eliminate noise and finalize `lynx` as a hyper-reliable navigation utility, creating a flawless foundational layer for the `lea` architectural agent to leverage.




# Search Quality Issue: Architectural / Flow Queries Return Irrelevant Results

## Summary

The search engine currently performs reasonably well for exact symbol lookups:

```bash
lx search "login"
```

returns:

```text
METHOD
  login

  Confidence: High (100%)
```

However, natural-language architectural queries still produce highly irrelevant results.

Example:

```bash
lx search "authentication flow"
```

Expected:

* Authentication services
* Login handlers
* JWT validation
* Middleware
* Token parsing
* Authorization-related components

Actual:

```text
main
Commands
Parser.parse_go
CodeChunk
EmbeddingCache
Parser.new
...
```

None of these results are related to authentication.

---

# Root Cause Analysis

## 1. Semantic Search Dominates Too Aggressively

Current ranking appears to heavily reward embedding similarity.

For broad queries such as:

```text
authentication flow
```

the embedding model generates a generic programming-related vector.

This causes infrastructure symbols to rank highly:

* main
* Commands
* Parser
* CodeChunk
* Embedder

even though they have no authentication relevance.

---

## 2. Missing Domain-Aware Intent Expansion

The query:

```text
authentication flow
```

contains architectural intent.

A developer usually means:

```text
login process
jwt validation
token parsing
middleware chain
authorization service
request authentication
```

The current pipeline does not expand the query into related software-engineering concepts.

As a result:

```text
authentication
```

is treated as a generic semantic phrase rather than a software architecture concept.

---

## 3. No Symbol-Purpose Awareness

Currently:

```rust
main()
```

and

```rust
AuthService.Login()
```

are treated similarly by semantic ranking.

The ranker does not understand that:

* login()
* authenticate()
* verify_token()
* parse_jwt()

are likely more relevant to an authentication query than:

* main()
* new()
* parse_go()

---

## 4. File Coherence Boost Is Too Strong

Many results include:

```text
Why:
- Semantic match
- File coherence boost
```

This suggests file-level boosting is pushing unrelated symbols upward.

The ranker may be rewarding:

```text
same file
same module
same package
```

more than actual query relevance.

For broad queries, this introduces substantial noise.

---

# Recommended Fixes

## Priority 1: Intent Classification

Classify queries before retrieval.

Example:

```text
authentication flow
```

should become:

```text
QueryType::ArchitectureFlow
```

Possible categories:

```rust
enum QueryType {
    ExactSymbol,
    Definition,
    ArchitectureFlow,
    CallFlow,
    CRUDOperation,
    ErrorHandling,
    Configuration,
    Semantic
}
```

This allows specialized retrieval strategies.

---

## Priority 2: Query Expansion

Expand architectural terms before retrieval.

Example:

```text
authentication
```

expands into:

```text
authentication
login
jwt
token
middleware
authorize
verify
access token
identity
session
```

Similarly:

```text
database
```

could expand into:

```text
repository
gorm
sql
transaction
query
storage
```

This dramatically improves retrieval quality.

---

## Priority 3: Symbol Intent Boosting

Introduce a relevance bonus when symbol names contain query-related concepts.

Example:

Query:

```text
authentication flow
```

Boost:

```text
login
authenticate
authorization
jwt
token
verify
middleware
session
identity
```

Penalty:

```text
main
new
test
config
```

unless explicitly requested.

---

## Priority 4: Penalize Generic Symbols

Introduce negative weights for generic symbols.

Examples:

```text
main
new
init
test
helper
util
config
```

These symbols frequently appear but rarely answer architectural questions.

---

## Priority 5: Architectural Search Mode

Introduce a dedicated mode:

```bash
lx search "authentication flow"
```

Internally:

```rust
ArchitectureFlowSearch
```

Pipeline:

1. Query classification
2. Query expansion
3. Retrieve candidate symbols
4. Retrieve call relationships
5. Rank services/controllers/middleware higher
6. Demote infrastructure symbols

Expected output:

```text
login()
AuthService.Login()
VerifyAccessToken()
ParseAccessToken()
authenticate()
JWT middleware
```

instead of:

```text
main()
Parser.new()
CodeChunk
EmbeddingCache
```

---

# Success Criteria

For the query:

```bash
lx search "authentication flow"
```

Top 10 results should predominantly contain:

* authentication handlers
* login functions
* middleware
* token validation
* authorization services

and should contain few or no:

* main()
* Parser.new()
* generic constructors
* embedding infrastructure
* unrelated utility symbols

The ranking should prioritize architectural relevance over generic semantic similarity.

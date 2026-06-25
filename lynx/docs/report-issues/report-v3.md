
# Lynx Search & Symbol Identity Review

## Current State

The latest version of Lynx represents a significant improvement over previous iterations.

Previous output:

```text
method ── unknown
```

Current output:

```text
METHOD
  login

  Confidence: High (100%)

  Why:
  - Exact symbol match

  Symbol:
  method:api/http:login

  File:
  api/http/router.go:43-58
```

This transition moves Lynx away from being a generic semantic chunk search engine and closer to becoming a true repository discovery layer.

---

# What Improved

## 1. Real Symbol Extraction

The most important milestone is that Lynx now extracts actual symbol names instead of returning anonymous chunks.

Before:

```text
method ── unknown
```

After:

```text
METHOD
  login
```

This dramatically improves usability for both developers and AI agents.

---

## 2. Symbol-First Discovery

The output is now centered around symbols rather than files.

Before:

```text
internal/auth/service.go:11-15
```

After:

```text
METHOD
  login
```

This aligns with the core philosophy of Lynx:

```text
Files are implementation details.
Symbols are the actual navigation targets.
```

---

## 3. Confidence & Explainability

Confidence scoring provides transparency and allows downstream tools to make informed decisions.

Example:

```text
Confidence: High (100%)

Why:
- Exact symbol match
```

Benefits:

* Easier ranking validation
* Better AI decision making
* Improved debugging of retrieval quality
* Increased trust in search results

---

## 4. Improved Symbol Identity

Previous symbol format:

```text
method:api/http/router.go:login
```

Current format:

```text
method:api/http:login
```

This is a meaningful improvement because filenames are not stable identities.

For example:

```text
router.go
```

may later become:

```text
auth.go
login.go
handlers.go
```

while the logical symbol remains unchanged.

Removing filenames from symbol identities improves long-term stability.

---

# Remaining Improvements

## P0 — Receiver-Aware Symbol IDs

Current format:

```text
method:api/http:login
```

Potential issue:

```go
func (r *router) login(...)
func (h *authHandler) login(...)
```

Both would resolve to:

```text
method:api/http:login
```

which is ambiguous.

Recommended format:

### Functions

```text
func:api/http:Login
```

### Methods

```text
method:api/http:router.login
```

or

```text
method:internal/auth:AuthService.Login
```

Benefits:

* Stable identities
* Unambiguous method resolution
* Better graph traversal
* Better Lea integration

Example:

```bash
lea flow method:internal/auth:AuthService.Login
```

This is significantly more expressive than:

```bash
lea flow method:internal/auth:Login
```

---

## P1 — Definition vs Reference Detection

Lynx should distinguish between:

```go
func Login()
```

and

```go
service.Login()
```

Example output:

```text
METHOD
  Login

  Type:
  Definition
```

or

```text
REFERENCE
  Login
```

Benefits:

* Higher ranking accuracy
* Better discovery quality
* More reliable AI navigation

Definitions should receive ranking boosts.

---

## P2 — Noise Suppression

Search results still contain mock, generated, and test artifacts.

Examples:

```text
mock/
*_test.go
generated/
vendor/
```

These rarely represent the primary implementation path.

Recommended default penalties:

```text
mock/       -> 0.2x
test/       -> 0.2x
*_test.go   -> 0.1x
generated/  -> 0.05x
vendor/     -> 0.01x
```

Or hide them entirely unless explicitly requested:

```bash
lx search "login" --include-tests
```

Benefits:

* Cleaner results
* Better signal-to-noise ratio
* Faster discovery

---

## P3 — Intent-Based Search Ranking

Exact symbol search is already strong.

Example:

```bash
lx search "login"
```

Result:

```text
METHOD
  login

  Confidence: High (100%)
```

However, concept searches still need improvement.

Example:

```bash
lx search "authentication flow"
```

Expected top results:

```text
AuthService
Login
VerifyAccessToken
authenticate
RegisterAuthRoutes
```

Instead of:

```text
AuthServiceOpts
Config
DTOs
Miscellaneous types
```

Recommendation:

Maintain separate ranking pipelines for:

### Symbol Search

```text
login
ParseAccessToken
AuthService
```

### Intent Search

```text
authentication flow
user registration
password reset
jwt validation
```

Intent search should prioritize:

* Definitions
* Service entry points
* Handlers
* Public interfaces
* Core business logic

---

## P4 — Native Lea Integration

Lynx and Lea become significantly more valuable when combined.

Example workflow:

### Discovery

```bash
lx search "login"
```

Result:

```text
METHOD
  login

  Symbol:
  method:api/http:router.login
```

### Structural Reasoning

```bash
lea flow method:api/http:router.login
```

### Impact Analysis

```bash
lea impact method:api/http:router.login
```

This creates a natural workflow:

```text
Lynx discovers.
Lea reasons.
```

---

# Strategic Assessment

Current progression:

```text
grep     -> files
Semble   -> chunks
Lynx     -> symbols
Lea      -> structure
```

Lynx has successfully moved beyond simple semantic search and is beginning to function as a true repository discovery layer.

The current priority should be:

1. Receiver-aware symbol identities
2. Definition/reference detection
3. Noise suppression
4. Intent-aware ranking
5. Deeper Lea integration

Memory systems should remain a lower priority until Lynx and Lea reach full maturity.

A strong discovery layer combined with a strong structural reasoning layer will provide significantly more long-term value than adding memory prematurely.

---

# Overall Evaluation

| Area                      | Score  |
| ------------------------- | ------ |
| Symbol Extraction         | 9/10   |
| Search UX                 | 8.5/10 |
| Confidence System         | 8/10   |
| Noise Suppression         | 7/10   |
| Intent Discovery          | 6/10   |
| Lea Integration Potential | 9/10   |

### Overall

**8.8 / 10**

Lynx is no longer behaving like a generic semantic search engine. It is evolving into a dedicated symbol discovery layer that fits naturally into the PizenLabs ecosystem.

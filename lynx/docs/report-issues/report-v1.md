# Lynx Discovery Layer Assessment & Priority Roadmap

## Current Observation

The primary issue is no longer the CLI rendering layer. The real problem lies in the quality of coordinate extraction and symbol resolution.

The current output appears cleaner:

```text
internal/auth/impl/service.go:32-46 │ method ── unknown
```

However, the underlying coordinate is still:

```text
method:internal/auth/impl/service.go:unknown
```

This indicates that the parser is not successfully extracting symbol names during indexing.

As a result, Lynx is returning coordinates that are structurally incomplete and difficult for downstream tools such as Lea to consume effectively.

---

# Core Problem

For a query such as:

```bash
lx search "authentication login flow"
```

The ideal output should resemble:

```text
internal/auth/impl/service.go:32-46 │ method ── Login
internal/auth/service.go:11-15 │ interface ── AuthService
api/http/router.go:43-58 │ method ── RegisterAuthRoutes
```

This allows an AI agent to immediately identify:

* Which symbols are relevant
* Which components participate in the flow
* Which coordinates can be passed directly to Lea

Instead, the current output returns:

```text
method ── unknown
```

This makes the coordinate nearly useless for structural reasoning.

---

# Priority Roadmap

## P0 — Symbol Extraction

### Importance

This is currently the highest-priority blocker.

Instead of generating:

```text
type:internal/auth/service.go:unknown
```

Lynx should generate:

```text
type:internal/auth/service.go:AuthService
```

or preferably:

```text
interface:internal/auth:AuthService
```

### Example: Interface Definition

Given:

```go
type AuthService interface {}
```

Lynx should index:

```json
{
  "kind": "interface",
  "name": "AuthService",
  "symbol_id": "interface:internal/auth:AuthService"
}
```

### Example: Method Definition

Given:

```go
func (s *Service) Login() error
```

Lynx should index:

```json
{
  "kind": "method",
  "name": "Login",
  "receiver": "Service",
  "symbol_id": "method:internal/auth:Service.Login"
}
```

### Expected Outcome

Every indexed chunk should have a deterministic and reusable symbol identifier.

Without this capability, Lynx cannot reliably act as the Discovery Layer for Lea.

---

## P1 — Definition vs Reference Detection

### Problem

Lynx currently does not distinguish between:

```go
func Login()
```

and

```go
service.Login()
```

Although they contain the same identifier, they represent fundamentally different meanings.

### Required Classification

```text
DEFINITION
REFERENCE
```

### Example

```text
method ── Service.Login (definition)
```

Definition chunks should receive a ranking boost because they represent the canonical implementation.

### Expected Outcome

Search results prioritize ownership and implementation rather than scattered references.

---

## P2 — Noise Suppression

### Problem

Current search results are heavily dominated by:

```text
internal/auth/mock/*
```

For a query such as:

```text
authentication login flow
```

The most useful results are typically:

* Production implementations
* Interfaces
* Handlers
* Business logic

not:

```text
mock/
```

### Recommended Penalties

```rust
mock/       -> 0.20x
test/       -> 0.20x
*_test.go   -> 0.10x
generated/  -> 0.05x
vendor/     -> 0.01x
```

### Expected Outcome

Top results naturally shift toward:

```text
internal/auth/impl/service.go
internal/auth/service.go
api/http/router.go
```

without requiring any changes to retrieval quality.

---

## P3 — Symbol-First Output

### Opportunity

This is one of the largest opportunities to differentiate Lynx from traditional semantic code search systems.

Current output:

```text
internal/auth/impl/service.go:32-46
```

Proposed output:

```text
METHOD
  Login

  Symbol:
  method:internal/auth:Service.Login

  File:
  internal/auth/impl/service.go:32-46
```

Or:

```text
INTERFACE
  AuthService

  Symbol:
  interface:internal/auth:AuthService

  File:
  internal/auth/service.go:11-15
```

### Rationale

AI agents care more about:

```text
symbol_id
```

than:

```text
line numbers
```

because symbol identifiers are the bridge into Lea's structural graph.

### Expected Outcome

Lynx becomes a symbol discovery engine rather than a chunk retrieval engine.

---

## P4 — Discovery Confidence

### Problem

Current scores look like:

```text
[0.0282]
```

These values are difficult for both humans and AI agents to interpret.

### Proposed Format

```text
Confidence: High
```

or

```text
Confidence: 91%
```

Example:

```text
METHOD
  Login

  Confidence: 94%

  Symbol:
  method:internal/auth:Service.Login

  Why:
  - exact identifier match
  - auth package relevance
  - definition boost
```

### Expected Outcome

Improved explainability and easier ranking validation.

---

# Larger Architectural Concern

A query such as:

```bash
lx search "authentication login flow"
```

currently returns dozens of chunks.

However, within the PizenLabs ecosystem:

```text
Lynx discovers.
Lea reasons.
```

Lynx should not return 50 chunks.

Instead, it should return a small set of high-confidence symbols:

```text
Resolved Symbols

1. interface:internal/auth:AuthService
2. method:internal/auth:Service.Login
3. method:api/http:AuthHandler.Login
```

These coordinates can then be passed directly into Lea:

```bash
lea impact method:internal/auth:Service.Login
```

or:

```bash
lea flow method:internal/auth:Service.Login
```

---

# Ecosystem Philosophy

The long-term positioning of the PizenLabs ecosystem should be:

```text
grep     -> files
Semble   -> chunks
Lynx     -> symbols
Lea      -> structure
```

Where:

* grep discovers files.
* Semble discovers relevant chunks.
* Lynx resolves intent into symbols.
* Lea performs structural reasoning and impact analysis.

---

# Recommended Short-Term Roadmap

```text
P0  Extract real symbol names
P1  Definition vs Reference detection
P2  Noise suppression
P3  Symbol-first output
P4  Confidence scoring
P5  resolve_symbol command
```

Successfully completing these milestones will transform Lynx from a generic semantic code search engine into a true Discovery Layer capable of powering Lea and the broader PizenLabs ecosystem.


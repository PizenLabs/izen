## Description

<!-- Provide a brief summary of the change, the problem it solves, or the feature it adds. -->

## Type of Change

- [ ] Bug fix (non-breaking change fixing an issue)
- [ ] New feature (non-breaking change adding functionality)
- [ ] Refactoring / Architectural improvement
- [ ] Documentation update

---

## Architectural Compliance Checklist

Please confirm your PR adheres to Izen's **Architecture Guardrails**:

### 1. "One Question, One Owner" Check

- [ ] **Component Ownership:** Does this PR add logic to a component? If so, does it answer *only* the single question owned by that component?
- [ ] **No Policy Creep:** `Capability Graph` contains NO `Allow/Deny` logic.
- [ ] **No Capability Creep:** `Policy Engine` reads facts from `Capability Graph` rather than probing OS/tools directly.
- [ ] **Thin Decision Loop:** `DecisionEngine` delegates specific calculations (budget, retry, risk) to injected policies.

### 2. Dependency & Layering Rules

- [ ] **Unidirectional Imports:** Does dependency flow strictly DOWN? (e.g., `pkg/engine` or `internal/domain` do NOT import `internal/ui` or `internal/modes`).
- [ ] **Events Flow UP:** State changes are communicated to upper layers via `events.Envelope` on `internal/events.Bus`.
- [ ] **Single Composition Root:** New services/engines are wired inside `internal/runtime/compose/compose.go`. No direct `new Engine()` instantiations inside UI or handlers.

### 3. Implementation Integrity

- [ ] **Typed Signals:** No raw string or regex matching on terminal/log output for error routing or mode handoffs (uses `domain.Signal`).
- [ ] **Canonical Types:** Uses canonical domain types (`domain.Task`, `domain.Signal`, `events.Envelope`, `UserIntent`).

---

## Verification

Please run the following commands locally and attach test output:

```bash
# 1. Build verification
go build ./...

# 2. Race detector & Unit tests
go test ./... -race

# 3. Linter check
golangci-lint run ./...
```

- [ ] `go build ./...` passed cleanly.
- [ ] `go test ./... -race` passed cleanly.
- [ ] `golangci-lint run ./...` returned 0 issues.

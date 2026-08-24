# 06 — Execution Contracts & Strategies

| Field | Value |
|---|---|
| Status | CONFIRMED |
| Version | 0.2.0 |
| Last Reviewed | 2026-08-23 |

## Definition

**Execution** is the deterministic, side-effect-bearing phase of Izen where validated Artifacts are processed by the Runtime Host to apply workspace mutations or perform system interactions.

## Object Authority Matrix

To prevent UI or Agent state inference bugs, authority over execution objects is strictly partitioned:

| Object | Owning Authority | Authority Responsibility |
|---|---|---|
| `ExecutionRequest` | Runtime Gateway | Defines request parameters, target intent, and identity. |
| `ExecutionStrategy` | Runtime Engine | Selects execution mode contract (`full_file`, `targeted_mutation`, `command`). |
| `ModelInvocation` | Runtime / Model Boundary | Manages request transport, sampling parameters (temperature), and raw response extraction. |
| `ModelOutput` | Model Provider | Untrusted raw token stream or textual response. |
| `Artifact` | Artifact Validator | Extracted, validated, and contract-compliant data structure. |
| `ExecutionContract` | Runtime Engine | Authoritative contract defining target scope, mutation domain, capabilities, and OCC preconditions. |
| `ProviderUsage` | Runtime Host | Authoritative normalized token usage sourced directly from API headers. |
| `ExecutionResult` | Runtime Execution Host | Records exit codes, stdout/stderr, or side-effect execution outcomes. |
| `MutationEvidence` | Runtime Execution Host | Physical workspace evidence (cryptographic hash, diff, byte size, timestamp). |
| `VerificationEvidence` | Verification Engine | Output from linters, test runners, or explicit validation checks. |
| UI Projection | UI Layer | Pure projection of Runtime evidence and states. |

## Strategy vs. Artifact Distinction

- `ExecutionStrategy` defines the Runtime contract used to perform an operation (e.g., `full_file`, `targeted_mutation`, `patch`, `command`).
- `ArtifactKind` describes the validated structure extracted from Model Output (e.g., `full_file`, `search_replace`, `unified_diff`, `command`).

> Strategy and Artifact Kind must not be conflated as identical enums.

## Canonical Execution Pipeline

```
Intent ──► Context ──► Autonomy Decision ──► Capability Grant
                                                    │
                                                    ▼
                                          Execution Request
                                                    │
                                                    ▼
                                Execution Strategy & Execution Contract
                                                    │
                                                    ▼
                                Model Invocation (Invocation Retry Loop)
                                                    │
                                                    ▼
                                         Model Output (Untrusted)
                                                    │
                                                    ▼
                                  Artifact Extraction & Validation
                                     │                      │
                                 (Reject)                (Valid)
                                     │                      │
                                     ▼                      ▼
                                 Recovery      3-Phase Atomic Execution
                                                (Prepare → Stage → Commit)
                                     │                      │
                                     ▼                      ▼
                              Contract(N+1)          Mutation Evidence
                                     │                      │
                                     ▼                      ▼
                               Escalation             Verification
```

## Invariants

- **Artifact Boundary:** Model Output must not be passed directly to a mutation executor. The Runtime must first extract and validate an Artifact according to the active execution contract. Invalid, malformed, truncated, or contract-incompatible output must remain outside the mutation boundary.
- **Contract Identity & Meaningful Recovery:** Each logical execution attempt must have an observable execution contract. A recovery attempt must differ from its predecessor in at least one execution-relevant contract dimension when the previous contract cannot satisfy the failure condition:

  ```
  Contract(N+1) ≠ Contract(N)
  ```

  Changing only metadata, logging text, temperature, request identity, or recovery reason is **not** a valid recovery strategy.

- **No Implicit Strategy Fallback:** If a selected ExecutionStrategy fails validation (e.g., `search_replace` anchor not found), the Runtime must not silently execute an unvalidated `full_file` overwrite. It must enter explicit contract recovery.
- **Single Production Authority:** Exactly one Runtime Execution Host owns physical side-effects per workspace.
- **Evidence-Backed Success:** Workspace status cannot be marked success without explicit MutationEvidence recorded by the Execution Host.

# Izen Constitution — Implementation Audit

| Field         | Value        |
| ------------- | ------------ |
| Status        | ACTIVE AUDIT |
| Version       | 0.5.0        |
| Last Reviewed | 2026-08-25   |

## Purpose

Verify whether the current codebase conforms to the confirmed Izen Constitution and Canonical System Model.

This document is an **implementation and runtime conformance instrument**. It records forensic evidence, executable evidence, implementation divergences, runtime behavior, and measured operational cost.

It does not redefine, extend, or replace the Constitution or Canonical System Model.

> **The audit does not invent architecture. It proves, falsifies, or leaves unverified what the architecture already requires.**

---

# 0. Execution Pipeline — 5-Boundary Zero-Trust Architecture (v0.5.0)

## 0.1 Incident Reference ( motivating the v0.5.0 refactor )

A FULL_REWRITE request on `index.html` (7.78 KB) ran under `max_output=1024`.
The provider returned `finish_reason=length`; the truncated artifact entered
the recovery pipeline anyway, poisoning prompt context, violating contract
identity, and driving invalid retry loops. The legacy trial-and-error recovery
model (`retry_with_evidence` re-scope on any generic failure) was replaced by
the strict boundary architecture below.

## 0.2 Invariants

| ID | Invariant                    | Requirement                                                                                                                        | Enforcement surface                                                            | Status      |
| -- | ---------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ | ----------- |
| I1 | Output Boundary Integrity    | `OUTPUT_EXHAUSTED` strictly halts execution. An incomplete generation is never parsed, staged, or applied.                          | `execution.gateFor` circuit break BEFORE artifact parsing (`executor.go`)       | **VERIFIED** |
| I2 | Recovery Isolation           | Workspace State = Authoritative; Diagnostics = Advisory; Rejected Artifact = Isolated. Rejected bytes never re-enter prompt context. | `DiagnosticSignal` metadata-only channel; rejected content zeroed on result     | **VERIFIED** |
| I3 | Typed Execution Transition   | `FULL_REWRITE → BOUNDED_PATCH` requires atomic context rebuilding + new causal contract + workspace version binding.                | `autonomy.typedRepair` transition latch; `ContractRegistry.Resolve` causal edge | **VERIFIED** |
| I4 | Failure-Class Retryability   | Retryability maps to canonical failure SUBTYPES, never generic execution errors.                                                    | `RecoverySubtype` / `DecideRecovery` matrix (`runtime/autonomy/recovery.go`)    | **VERIFIED** |
| I5 | Preflight Feasibility        | Generation budget verified pre-execution: `EstimatedTokens = TargetFileTokens × Multiplier ≤ max_output`.                            | `execution.EvaluatePreflight`, wired at Boundary 2 in `Execute()`               | **VERIFIED** |

## 0.3 Boundary Matrix

| # | Boundary            | Owner (implementation)                                   | What enters                                    | What leaves                                                              | Fail-closed behavior                                                                       | Executable evidence |
| - | ------------------- | -------------------------------------------------------- | ---------------------------------------------- | ------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------ | ------------------- |
| B1 | Intent Gateway     | `execution.IntentGateway.Gate` + `strategy.Select`       | Raw user payload                               | Resolved intent + strategy profile + frozen context snapshot             | Ambiguity parks before any model call; context integrity seal verified at admission        | §1 audit below      |
| B2 | Preflight Guard    | `EvaluatePreflight` in `internal/execution/boundaries.go` | Targets + artifact contract + max_output       | Feasible verdict OR typed rejection (`preflight_infeasible`)              | Rejects with ZERO HTTP provider requests; intent never silently altered                    | Test Case A         |
| B3 | Output Gate        | `gateFor` / `NormalizeFinishReason`                      | Provider stream + authoritative finish_reason  | `CanonicalOutcome`; only COMPLETE proceeds                               | `OUTPUT_EXHAUSTED`/refusal ⇒ transport circuit break; output discarded unparsed            | Test Case B         |
| B4 | Artifact Gate      | `artifactGate` + `ExtractBoundedPatch` + V3 validators    | COMPLETE generation only                       | Validated normalized artifact OR typed schema rejection                   | Hunk-schema/syntax failures reject before approval; ONLY `DiagnosticSignal`s cross back     | `TestSearchReplaceContractRejectsFullFileOutput`, conformance suite |
| B5 | Mutation Authority | `OCCVerifier.TreeDigest` + OCC commit gate + adapter drift check | Held patches + baseline fingerprints          | Applied mutation evidence OR `ABORTED_OCC` / `workspace_drift` abort      | Digest `SHA256(Σ path(f)+hash(f))` must match between attempts; commit gate precedes write  | `TestBoundaryFive_WorkspaceDriftAbortsBetweenAttempts`, Phase 3 OCC tests |

## 0.4 Canonical Outcome Vocabulary (B3)

`COMPLETE · OUTPUT_EXHAUSTED · PROVIDER_REFUSAL · TRANSPORT_ERROR · UNKNOWN`
(`NormalizeFinishReason`). Unreported finish reasons defer to B4; KNOWN
non-complete reasons are absolute circuit breaks.

## 0.5 Failure-Subtype → Retry Matrix (I4)

| Subtype              | Trigger                                        | Automatic continuation                                                     | Bound                                |
| -------------------- | ---------------------------------------------- | -------------------------------------------------------------------------- | ------------------------------------ |
| `output_exhausted`   | finish_reason=length                           | ONE typed `FULL_REWRITE→BOUNDED_PATCH` transition, then STRICT HALT (I1)    | once per lineage (strategy latch)    |
| `schema_violation`   | B4 hunk/schema rejection                       | Typed transition if unbounded; else one bounded re-issue per remaining cycle | `MaxRecoveryCycles`                  |
| `transport_error`    | invocation-level transport failure             | Pure retry preserving contract identity (AttemptID increments)              | attempts + cycles                    |
| `provider_refusal`   | refusal/content-filter terminal reason         | none — abort                                                                | —                                    |
| `preflight_infeasible` | B2 budget refusal                            | none — explicit human re-scope required                                     | —                                    |
| `workspace_drift`    | B5 digest mismatch between attempts            | none — abort stale run                                                      | —                                    |
| `mutation_failure`   | apply/verify gates failed                      | none — human decision over rolled-back ground                               | —                                    |

The generic `retry_with_evidence` re-scope loop is REMOVED. Evidence appended
by a repair carries `[DIAGNOSTIC subtype=… boundary=…]` advisory signals only
(I2).

## 0.6 Conformance Test Outcomes

| Test Case                        | Level     | Assertion set (all passing)                                                                                                                                                 |
| -------------------------------- | --------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A — Preflight Infeasibility Trapping | executor | `TestConformanceA_PreflightInfeasibilityTrapping` (internal/execution): 7780-byte FULL_REWRITE @ max_output=1024 halts with outcome `preflight_infeasible`, **0** provider requests, no artifact surface, workspace untouched, diagnostic metadata-only. Inverse guard: feasible target executes normally. |
| A — runtime consequence          | driver    | `TestConformanceA_DriverPreflightInfeasibilityParksWithZeroRequests` (internal/runtime/autonomy): run parks at an inform boundary demanding explicit re-scope; 0 invocations.                                                               |
| B — Output Exhaustion Circuit Breaking | executor | `TestConformanceB_OutputExhaustionCircuitBreaking`: simulated `finish_reason=length` carrying a parseable hunk is caught at B3, discarded UNPARSED (no pending patch), exactly 1 invocation, billed usage preserved, advisory diagnostic only. Refusal arm covered by `TestConformanceB_RefusalCircuitBreaks`. |
| B — runtime consequence          | driver    | `TestConformanceB_DriverOutputExhaustionSingleTypedTransitionThenHalt`: exactly 2 invocations (initial + ONE typed transition); transition carries causal parent, workspace digest, diagnostic-only evidence; second exhaustion strictly halts. |
| B5 drift                         | adapter   | `TestBoundaryFive_WorkspaceDriftAbortsBetweenAttempts`: stale digest refuses submission with 0 requests; fresh digest control executes.                                                                                                      |
| Unit matrices                    | both      | `TestNormalizeFinishReasonVocabulary`, `TestEvaluatePreflightFormula`, `TestTreeDigestStabilityAndSensitivity`, `TestRecoverySubtypeMatrix`, `TestDecideRecoveryMatrix`.                                                                      |

All tests pass (`go test ./...`), race-clean (`-race`), lint-clean.

## 0.7 Implementation Notes

* Legacy loop-level truncation fixtures that depended on an infeasible full
  rewrite REACHING the provider were re-based: wire-contract/window assertions
  now drive the executor's explicit `bounded_patch` contract directly (still
  exercised against the original 7780-byte geometry); loop-level end-to-end
  recovery uses feasibility-sized fixtures.
* The historical sentinel `ErrOutputTruncated` remains discoverable through
  the new gate-error chain (`ErrOutputExhausted` wraps it).

---

# Audit Framework

Every audited boundary is evaluated against five mandatory questions:

1. **Who owns it?** — Identifies the authoritative implementation entity.
2. **What enters?** — Identifies the input contract.
3. **What leaves?** — Identifies the output contract.
4. **What invariant proves it?** — Identifies the constitutional/system-model rule under test.
5. **What executable evidence proves it?** — Names tests, runtime traces, or benchmark evidence.

Where operational behavior matters, the audit additionally records:

6. **What does it cost?** — Measures execution latency, I/O, CPU, memory, or provider overhead where applicable.

---

## Evidence Model

Architectural approval and implementation conformance are independent.

| Field                     | Meaning                                                                         |
| ------------------------- | ------------------------------------------------------------------------------- |
| **Architectural Status**  | `CONFIRMED` — canonical, ratified architectural decision.                       |
| **Implementation Status** | `VERIFIED` | `PARTIAL` | `UNKNOWN` | `VIOLATION`                                |
| **Operational Status**    | `MEASURED` | `UNMEASURED` | `WITHIN_BOUND` | `EXCEEDS_BOUND` | `NOT_APPLICABLE` |

### Implementation Status

| Status      | Definition                                                                            |
| ----------- | ------------------------------------------------------------------------------------- |
| `VERIFIED`  | Implementation evidence and named executable tests demonstrate conformance.           |
| `PARTIAL`   | Implementation exists but evidence, coverage, or execution conditions are incomplete. |
| `UNKNOWN`   | Insufficient evidence exists to determine conformance.                                |
| `VIOLATION` | Observed implementation behavior contradicts the confirmed requirement.               |

### Operational Status

Operational status is independent of architectural correctness.

| Status           | Definition                                                                                  |
| ---------------- | ------------------------------------------------------------------------------------------- |
| `MEASURED`       | Relevant operational cost has been benchmarked, but no architectural threshold is asserted. |
| `UNMEASURED`     | No representative measurement exists yet.                                                   |
| `WITHIN_BOUND`   | Measured cost satisfies an explicitly declared implementation/performance bound.            |
| `EXCEEDS_BOUND`  | Measured cost exceeds the declared bound.                                                   |
| `NOT_APPLICABLE` | No meaningful operational measurement applies to the boundary.                              |

> **Meta-rule:** `Implementation Status = VERIFIED` only when implementation evidence and named executable test evidence both exist.

> **Operational evidence does not upgrade or downgrade architectural conformance by itself.** A correct implementation may still have unacceptable operational cost; that is an operational finding, not automatically an architectural violation.

> `UNKNOWN` is a valid audit result. Unsupported certainty is an audit defect.

---

# Audit Closure Rules

A boundary may be marked `VERIFIED` only when:

* The implementation path has been directly inspected or exercised.
* Named executable tests exist and pass.
* Evidence identifies the exact implementation surface being audited.
* No open divergence contradicts the invariant for the audited path.

A boundary remains `PARTIAL` when implementation exists but evidence or coverage is incomplete.

A boundary remains `UNKNOWN` when insufficient evidence exists.

A boundary becomes `VIOLATION` when implementation behavior contradicts the confirmed architectural requirement.

Operational status must be recorded separately when the boundary introduces measurable runtime cost.

---

# Release Gates

Audit work follows this order:

```text
Gate A — Authority Integrity
        ↓
Gate B — Safety & Fidelity Integrity
        ↓
Gate C — Runtime Truth
        ↓
Gate D — Operational Viability
        ↓
Audit Closure
```

A later gate must not be used to hide a failure in an earlier gate.

In particular:

> **Performance measurements do not compensate for an unverified mutation authority boundary.**

---

# 1. Intent & Dynamic Surface Selection Boundary

**Constitution & System Model Reference:** `04_INTENT.md`, `02_SYSTEM_MODEL.md` (SM-001, SM-002)

Intent determines execution surface. Non-mutating requests must not instantiate mutation boundaries. Workspace Mode does not itself imply mutation authority.

|                          |                                                                                                                                                                                                |
| ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Owner                    | `[TO_BE_AUDITED: IntentGateway / Classifier]`                                                                                                                                                  |
| What enters              | Raw user payload + workspace context hints.                                                                                                                                                    |
| What leaves              | Resolved Intent with required surface profile (`Reduced` / `Full`).                                                                                                                            |
| What invariant proves it | Non-mutating requests do not instantiate mutation boundaries. `AMBIGUOUS` does not create or expand mutation authority.                                                                        |
| What test proves it      | `[TO_BE_AUDITED: TestAskDoesNotActivateMutationBoundary, TestModificationActivatesRequiredBoundaries, TestAmbiguousIntentCannotAcquireMutationCapability, TestAmbiguityCannotCreateRiskScope]` |
| Architectural Status     | **CONFIRMED**                                                                                                                                                                                  |
| Implementation Status    | **UNKNOWN**                                                                                                                                                                                    |
| Operational Status       | **UNMEASURED**                                                                                                                                                                                 |
| Test Evidence            | `[TO_BE_AUDITED]`                                                                                                                                                                              |
| Implementation Evidence  | `[TO_BE_AUDITED]`                                                                                                                                                                              |

### Fast Intent Path Audit

Explicit intent directives are deterministic inputs.

Examples:

```text
/ask ...
/build ...
/plan ...
/investigate ...
/review ...
```

The audit MUST determine whether explicit intent bypasses heavyweight or model-based classification.

Required property:

```text
Explicit Intent
    ↓
Deterministic Resolution
    ↓
Surface Selection
```

A model invocation MUST NOT be required merely to confirm an already explicit mode directive.

### Initial Operational Gate

For explicit, unambiguous commands:

```text
Intent Resolution Latency < 10 ms
```

This is an **implementation performance target**, not a constitutional invariant.

Required evidence:

* p50 latency
* p95 latency
* p99 latency
* number of iterations
* benchmark environment
* whether model/provider/network access occurred

Required benchmark:

```text
[TO_BE_AUDITED: BenchmarkExplicitIntentResolution]
```

---

# 2. Context Compiler & Graceful Degradation Boundary

**Constitution & System Model Reference:** `01_PHILOSOPHY.md`, `02_SYSTEM_MODEL.md` (SM-008)

Graph AST, Symbol Index, and Text/Search probes may execute in parallel. Context quality is ranked Graph AST > Symbol Index > Text/Search.

Probe failure, timeout, or resource exhaustion must degrade rather than abort the reasoning path where the architecture permits degradation.

|                          |                                                                                                             |
| ------------------------ | ----------------------------------------------------------------------------------------------------------- |
| Owner                    | `[TO_BE_AUDITED: ContextCompiler]`                                                                          |
| What enters              | Target scope + context probe configuration.                                                                 |
| What leaves              | Quality-ranked context bundle, or degraded context bundle with failed probes recorded.                      |
| What invariant proves it | Probe failure degrades available context rather than incorrectly presenting failed context as complete.     |
| What test proves it      | `[TO_BE_AUDITED: TestGraphASTFailureDegradesToSymbolIndex, TestAllIndexerFailureDoesNotAbortReasoningPath]` |
| Architectural Status     | **CONFIRMED**                                                                                               |
| Implementation Status    | **UNKNOWN**                                                                                                 |
| Operational Status       | **UNMEASURED**                                                                                              |
| Test Evidence            | `[TO_BE_AUDITED]`                                                                                           |
| Implementation Evidence  | `[TO_BE_AUDITED]`                                                                                           |

---

# 3. Autonomy & Capability Grant Boundary

**Constitution & System Model Reference:** `05_AUTONOMY.md`, `02_SYSTEM_MODEL.md` (SM-003)

Capability Grants are issued exclusively to the Runtime Host. Agent proposals remain untrusted plan objects.

|                          |                                                                                                                                                   |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| Owner                    | `[TO_BE_AUDITED: PolicyEngine / RuntimeHost]`                                                                                                     |
| What enters              | Intent, target scope, requested capabilities, active Risk Scope where applicable.                                                                 |
| What leaves              | Bounded CapabilityGrant issued to Runtime Host.                                                                                                   |
| What invariant proves it | Agent data structures contain no execution capability tokens or direct mutation authority. Grants cannot be self-issued or expanded by the Agent. |
| What test proves it      | `[TO_BE_AUDITED: TestAgentStructHasNoExecutionCapability, TestCapabilityGrantCannotBeSelfIssued, TestRiskScopeIsBoundedAndRevocable]`             |
| Architectural Status     | **CONFIRMED**                                                                                                                                     |
| Implementation Status    | **PARTIAL**                                                                                                                                       |
| Operational Status       | **NOT_APPLICABLE**                                                                                                                                |
| Test Evidence            | `[TO_BE_AUDITED]`                                                                                                                                 |
| Implementation Evidence  | Known divergence: the migrated `RuntimeExecutor` / `IntentGateway` production path is dormant because `m.autonomy` is always non-nil. See D-002.  |
| Conformance Basis        | `PARTIAL` is based on observed production-path divergence. Formal executable evidence remains incomplete.                                         |

---

# 4. Invocation Retry vs. Contract Recovery Boundary

**Constitution & System Model Reference:** `06_EXECUTION.md`, `02_SYSTEM_MODEL.md` (SM-004, SM-005)

Sampling and transport adjustments belong to ModelInvocation retries. Contract Recovery requires a material change to the ExecutionContract.

|                          |                                                                                                                                                                                                                            |
| ------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Owner                    | `RuntimeExecutor` admission (`ContractRegistry.Resolve`) + runtime recovery matrix (`internal/runtime/autonomy/recovery.go`)                                                                                                |
| What enters              | Model output failure, invocation failure, or execution error (classified into canonical failure SUBTYPES).                                                                                                                  |
| What leaves              | Pure invocation retry under the SAME ContractID, OR a typed causal recovery contract with a material operational change.                                                                                                    |
| What invariant proves it | Transport/sampling retries preserve Contract identity. Recovery changes at least one canonical operational contract dimension. Retryability is subtype-keyed (I4), never generic.                                             |
| What test proves it      | `TestRecoveryDoesNotDuplicateProviderInvocation`, `TestUsageAggregationAcrossRecovery`, `TestConformanceB_DriverOutputExhaustionSingleTypedTransitionThenHalt`, `TestDecideRecoveryMatrix`                                   |
| Architectural Status     | **CONFIRMED**                                                                                                                                                                                                              |
| Implementation Status    | **VERIFIED**                                                                                                                                                                                                               |
| Operational Status       | **UNMEASURED**                                                                                                                                                                                                             |
| Test Evidence            | Named tests above pass on every run; transport retries derive an identical contract identity so the registry increments AttemptID only.                                                                                     |
| Implementation Evidence  | `typedRepair` emits a pure retry (no prompt/evidence/strategy drift) for `transport_error`; material continuations set `ParentContractID` and resolve through `ContractRegistry.Resolve`'s causal edge under `MaxRecoveryChainDepth`. The legacy generic `retry_with_evidence` re-scope is removed (v0.5.0). |

### Recovery Parameter Audit Set

```text
Target Scope
Capabilities
Mutation Domain
Validation Rules
Artifact Representation
Resource Constraints
Escalation Behavior
```

A temperature, `top_p`, prompt phrasing, retry counter, or transport metadata change alone MUST NOT increment Contract ID.

---

# 5. Mutation Domain, OCC & Recovery Snapshot Lifecycle Boundary

**Constitution & System Model Reference:** `02_SYSTEM_MODEL.md` §4.1, §5 (SM-006, SM-007, SM-010), `06_EXECUTION.md`

State-bearing mutations require ExecutionSnapshot and atomic staging/commit semantics. Recovery contracts require fresh snapshot baselines after terminal completion of the previous contract.

External side-effects are explicitly non-transactional.

|                          |                                                                                                                                                                                                                              |
| ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Owner                    | `[TO_BE_AUDITED: RuntimeHost / StagingManager / SnapshotEngine]`                                                                                                                                                             |
| What enters              | Validated Artifact + ExecutionContract + active CapabilityGrant.                                                                                                                                                             |
| What leaves              | ExecutionResult + MutationEvidence + fresh Snapshot handle for downstream contracts.                                                                                                                                         |
| What invariant proves it | External modification between snapshot and commit produces `WorkspaceStateConflict`. Recovery re-baselining captures current observed state without erasing concurrency conflicts.                                           |
| What test proves it      | `[TO_BE_AUDITED: TestConcurrentExternalWriteTriggersWorkspaceStateConflict, TestStagingAbortLeavesNoPartialWrites, TestRecoveryRequiresFreshSnapshotAfterTerminalContract, TestRebaseliningDoesNotEraseConcurrencyConflict]` |
| Architectural Status     | **CONFIRMED**                                                                                                                                                                                                                |
| Implementation Status    | **UNKNOWN**                                                                                                                                                                                                                  |
| Operational Status       | **UNMEASURED**                                                                                                                                                                                                               |
| Test Evidence            | `[TO_BE_AUDITED]`                                                                                                                                                                                                            |
| Implementation Evidence  | `[TO_BE_AUDITED]`                                                                                                                                                                                                            |

### OCC Precision Audit

The audit MUST determine whether fingerprint granularity matches mutation strategy.

| Mutation Strategy   | Required Audit Question                                                  |
| ------------------- | ------------------------------------------------------------------------ |
| Full-file rewrite   | Is whole-file identity validated before commit?                          |
| Targeted mutation   | Does fingerprint scope cover the complete targeted semantic/byte region? |
| Multi-file mutation | Does the snapshot cover the complete declared mutation domain?           |

### Operational Measurement

Measure:

* snapshot creation latency
* fingerprinting latency
* staging I/O
* commit I/O
* conflict frequency
* conflict frequency under concurrent tooling

Representative workloads MUST include:

```text
LSP
Formatter
File Watcher
Concurrent Tooling
```

Required benchmark:

```text
[TO_BE_AUDITED: BenchmarkOCCAndStagingCost]
```

---

# 6. Runtime Evidence & UI Projection Boundary

**Constitution & System Model Reference:** `00_IZEN.md`, `02_SYSTEM_MODEL.md` (SM-009)

UI renders authoritative runtime evidence. UI does not generate or predict domain state.

|                          |                                                                                                                                                            |
| ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Owner                    | `[TO_BE_AUDITED: EventBridge / UI Projection]`                                                                                                             |
| What enters              | MutationEvidence, ProviderUsage, execution-state events.                                                                                                   |
| What leaves              | UI Render State.                                                                                                                                           |
| What invariant proves it | UI does not mark mutation success before authoritative evidence exists. Provider usage is derived from authoritative provider/runtime data when available. |
| What test proves it      | `[TO_BE_AUDITED: TestUIDoesNotRenderSuccessBeforeMutationEvidence, TestProviderUsageProjectionUsesAuthoritativeUsage]`                                     |
| Architectural Status     | **CONFIRMED**                                                                                                                                              |
| Implementation Status    | **UNKNOWN**                                                                                                                                                |
| Operational Status       | **NOT_APPLICABLE**                                                                                                                                         |
| Test Evidence            | `[TO_BE_AUDITED]`                                                                                                                                          |
| Implementation Evidence  | `[TO_BE_AUDITED]`                                                                                                                                          |

---

# 7. Mutation Domain Isolation Boundary

**Constitution & System Model Reference:** `02_SYSTEM_MODEL.md` §4.3–§4.4 (SM-011)

An ExecutionContract must not combine State-bearing mutation and External Side-effect as one atomic unit.

Where a logical operation requires both, the operation must be decomposed into independently evidenced contracts.

|                          |                                                                                                                                                        |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Owner                    | `[TO_BE_AUDITED: RuntimeEngine / ExecutionContract construction]`                                                                                      |
| What enters              | Proposed operation requiring filesystem/config mutation and/or external side-effect.                                                                   |
| What leaves              | Single-domain ExecutionContract, or ordered contracts with independent evidence.                                                                       |
| What invariant proves it | Mixed-domain atomic contracts are rejected or decomposed before execution. External side-effect does not execute before verified State-bearing commit. |
| What test proves it      | `[TO_BE_AUDITED: TestExecutionContractRejectsMixedDomains, TestExternalSideEffectOnlyRunsAfterVerifiedStateCommit]`                                    |
| Architectural Status     | **CONFIRMED**                                                                                                                                          |
| Implementation Status    | **UNKNOWN**                                                                                                                                            |
| Operational Status       | **NOT_APPLICABLE**                                                                                                                                     |
| Test Evidence            | `[TO_BE_AUDITED]`                                                                                                                                      |
| Implementation Evidence  | `[TO_BE_AUDITED]` — see D-003.                                                                                                                         |

### Partial Application Audit

The audit MUST distinguish domain outcomes:

```text
State-bearing Commit      ✓ / ✗
External Side-effect      ✓ / ✗
Compensation              ✓ / ✗ / N/A
Recovery Required         YES / NO
```

A partial outcome MUST NOT be rendered as generic success.

Required evidence fields:

```text
state_bearing_status
external_effect_status
compensation_status
recovery_requirement
```

A Compensation Contract MAY be used when a safe compensating action is explicitly defined.

The audit MUST NOT assume that external side-effects are universally reversible.

---

# 8. Context Probe Budget Enforcement Boundary

**Constitution & System Model Reference:** `02_SYSTEM_MODEL.md` §3.2, §6 (SM-008)

Each context probe must be bounded by wall-clock, CPU-worker, and memory constraints. The request as a whole is subject to an aggregate context budget.

|                          |                                                                                                                                                                      |
| ------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Owner                    | `[TO_BE_AUDITED: ContextCompiler / ProbeScheduler]`                                                                                                                  |
| What enters              | Target scope, repository characteristics, per-probe budget, aggregate request budget.                                                                                |
| What leaves              | Context within budget, or degradation event with exceeded probes marked failed.                                                                                      |
| What invariant proves it | Exceeding a probe budget cancels/degrades the affected probe without allowing unbounded fan-out.                                                                     |
| What test proves it      | `[TO_BE_AUDITED: TestProbeExceedsWallClockCeilingDegradesGracefully, TestProbeMemoryCeilingCancelsWithoutOOM, TestAggregateContextBudgetPreventsProbeFanoutOverrun]` |
| Architectural Status     | **CONFIRMED**                                                                                                                                                        |
| Implementation Status    | **UNKNOWN**                                                                                                                                                          |
| Operational Status       | **UNMEASURED**                                                                                                                                                       |
| Test Evidence            | `[TO_BE_AUDITED]`                                                                                                                                                    |
| Implementation Evidence  | `[TO_BE_AUDITED]`                                                                                                                                                    |

---

# 9. Risk Scope Pre-Authorization Boundary

**Constitution & System Model Reference:** `02_SYSTEM_MODEL.md` §2.2, `05_AUTONOMY.md`

Risk Scope exists to prevent fail-closed ambiguity from degenerating into confirmation fatigue without creating implicit authority.

|                          |                                                                                                                                                                                                                                        |
| ------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Owner                    | `[TO_BE_AUDITED: PolicyEngine / CapabilityGrant Manager]`                                                                                                                                                                              |
| What enters              | Explicit human policy grant with operation class, target scope, capability, lifespan, provenance, and quantitative blast radius.                                                                                                       |
| What leaves              | Inspectable, bounded, revocable Risk Scope.                                                                                                                                                                                            |
| What invariant proves it | `AMBIGUOUS` may use only a pre-existing Risk Scope whose semantic and quantitative limits cover the operation. Ambiguity cannot create, expand, or persistently broaden scope.                                                         |
| What test proves it      | `[TO_BE_AUDITED: TestAmbiguousIntentCannotCreateRiskScope, TestRiskScopeCannotExpandFromAmbiguity, TestRiskScopeRequiresQuantitativeBlastRadius, TestRiskScopeEnforcesPhysicalMutationLimits, TestRiskScopeIsInspectableAndRevocable]` |
| Architectural Status     | **CONFIRMED**                                                                                                                                                                                                                          |
| Implementation Status    | **UNKNOWN**                                                                                                                                                                                                                            |
| Operational Status       | **NOT_APPLICABLE**                                                                                                                                                                                                                     |
| Test Evidence            | `[TO_BE_AUDITED]`                                                                                                                                                                                                                      |
| Implementation Evidence  | `[TO_BE_AUDITED]`                                                                                                                                                                                                                      |

---

# 10. Context Fidelity & Mutation Eligibility Boundary

**Constitution & System Model Reference:** `02_SYSTEM_MODEL.md` §6, §7 (SM-012)

Context degradation permits information flow for read operations, but mutation requires context fidelity appropriate to mutation strategy and blast radius.

|                          |                                                                                                                                                                                    |
| ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Owner                    | `[TO_BE_AUDITED: ContractEngine / IntentGateway / ContextCompiler]`                                                                                                                |
| What enters              | Context compilation report + proposed mutation strategy + blast radius.                                                                                                            |
| What leaves              | Contract Eligibility Determination.                                                                                                                                                |
| What invariant proves it | Mutation contract issuance is denied when context fidelity falls below the required threshold unless the architectural exception explicitly applies.                               |
| What test proves it      | `[TO_BE_AUDITED: TestDegradedContextCannotIssueMutationContract, TestHighFidelityMutationRequiresStructuralContext, TestTargetedMutationMayUseLowerContextTierWhenContractAllows]` |
| Architectural Status     | **CONFIRMED**                                                                                                                                                                      |
| Implementation Status    | **UNKNOWN**                                                                                                                                                                        |
| Operational Status       | **NOT_APPLICABLE**                                                                                                                                                                 |
| Test Evidence            | `[TO_BE_AUDITED]`                                                                                                                                                                  |
| Implementation Evidence  | `[TO_BE_AUDITED]`                                                                                                                                                                  |

### SM-012 Compound-Risk Invariant

The following condition is independently mandatory:

```text
AMBIGUOUS
    ∧
DEGRADED_CONTEXT
    ↓
FAIL_CLOSED
```

A pre-existing Risk Scope MUST NOT override this compound condition unless the confirmed System Model explicitly defines such an exception.

The audit MUST NOT infer compound-condition safety from independent tests covering ambiguity and degraded context separately.

Required test:

```text
TestAmbiguousIntentCannotUseRiskScopeUnderDegradedContext
```

This test must exercise both conditions simultaneously.

---

# 11. Partial Application & Session Taint Boundary

**Constitution & System Model Reference:** `02_SYSTEM_MODEL.md` §4.3–§4.4, `06_EXECUTION.md`

`PARTIALLY_APPLIED` is an execution truth, not merely an error classification.

When a logical operation produces an irreversible or incompletely compensated external outcome, the session/workspace state must retain that fact until recovery or explicit human intervention establishes a new trusted baseline.

|                          |                                                                                                                                                                                                                          |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Owner                    | `[TO_BE_AUDITED: RuntimeHost / SessionEngine / RecoveryManager]`                                                                                                                                                         |
| What enters              | Partial execution result + domain-specific outcome evidence.                                                                                                                                                             |
| What leaves              | `PARTIALLY_APPLIED` result plus persistent session/workspace taint state.                                                                                                                                                |
| What invariant proves it | A session affected by unresolved partial application cannot silently resume as a clean execution context.                                                                                                                |
| What test proves it      | `[TO_BE_AUDITED: TestPartialApplicationProducesExplicitOutcome, TestPartialApplicationTaintsSession, TestTaintedSessionCannotSilentlyIssueNormalMutationContract, TestCompensationClearsTaintOnlyAfterVerifiedRecovery]` |
| Architectural Status     | **CONFIRMED**                                                                                                                                                                                                            |
| Implementation Status    | **UNKNOWN**                                                                                                                                                                                                              |
| Operational Status       | **NOT_APPLICABLE**                                                                                                                                                                                                       |
| Test Evidence            | `[TO_BE_AUDITED]`                                                                                                                                                                                                        |
| Implementation Evidence  | `[TO_BE_AUDITED]`                                                                                                                                                                                                        |

### Taint Lifecycle

```text
NORMAL
  ↓
execution
  ↓
PARTIALLY_APPLIED
  ↓
TAINTED
  ├── Compensation Contract
  │       ↓
  │   verified recovery
  │       ↓
  │    NORMAL
  │
  └── Human Intervention
          ↓
      verified baseline
          ↓
        NORMAL
```

The audit MUST verify that:

* taint is attached to the relevant session/workspace context;
* subsequent mutation decisions observe the taint;
* taint cannot be cleared merely by starting a new prompt;
* compensation does not clear taint until recovery is evidenced;
* external side-effects are not falsely represented as transactionally rolled back.

> The audit does not require universal Saga-style compensation. It requires truthful state retention and explicit recovery semantics.

---

# Architectural Divergence Log

| Divergence ID | Boundary                    | Constitutional Requirement                                                                    | Implementation Reality                                                                                                                   | Risk Level                      | Action Required                                                                                                                                                                                           |
| ------------- | --------------------------- | --------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D-001         | Mutation Atomicity          | SM-006 — State-bearing multi-target mutation must be all-or-nothing.                          | `[TO_BE_AUDITED]`                                                                                                                        | CRITICAL if violated            | Verify staging/rollback semantics and land conformance tests.                                                                                                                                             |
| D-002         | Autonomy & Capability Grant | SM-003 / I-005 / I-010 — Runtime Host is the sole production mutation authority.              | Migrated `RuntimeExecutor` / `IntentGateway` path is dormant because `m.autonomy` is always non-nil; a legacy path is exercised instead. | **CRITICAL / RELEASE BLOCKING** | Fix the production routing condition and prove the migrated path is exercised under default runtime construction. If equivalence is claimed instead, produce explicit executable and structural evidence. |
| D-003         | Mutation Domain Isolation   | SM-011 — State-bearing and External Side-effect operations must not form one atomic contract. | `[TO_BE_AUDITED]`                                                                                                                        | UNKNOWN                         | Audit contract construction and land mixed-domain rejection/decomposition tests.                                                                                                                          |

### D-002 Release Rule

D-002 is an **authority-integrity failure**, not ordinary backlog.

Until resolved:

```text
D-002 unresolved
      ↓
Gate A = BLOCKED
      ↓
Release = BLOCKED
```

A contract-equivalence argument may only clear the gate if it demonstrates:

* identical authorization semantics;
* identical mutation authority;
* identical capability enforcement;
* identical evidence generation;
* identical failure/recovery semantics;
* identical workspace mutation boundary.

“Both paths eventually modify the same files” is not sufficient evidence of equivalence.

---

# Forensic Verification Backlog

## Gate A — Authority & Intent

* [ ] Audit IntentGateway for Reduced/Full surface activation by semantic Intent rather than Workspace Mode.
* [ ] Verify explicit `/ask`, `/build`, `/plan`, `/investigate`, `/review` directives use deterministic resolution.
* [ ] Benchmark explicit intent resolution.
* [ ] Verify no model/provider call is required for explicit intent resolution.
* [ ] Land `TestAskDoesNotActivateMutationBoundary`.
* [ ] Land `TestModificationActivatesRequiredBoundaries`.
* [ ] Land `TestAmbiguousIntentCannotAcquireMutationCapability`.
* [ ] Land `TestAmbiguousIntentCannotCreateRiskScope`.
* [ ] Confirm D-002 root cause and remove dormant production routing.
* [ ] Land regression coverage proving the migrated mutation path is exercised under default runtime construction.
* [ ] Verify no legacy or secondary production path can mutate outside RuntimeHost.

## Gate B — Context, Fidelity & Safety

* [ ] Audit ContextCompiler for parallel Graph/Symbol/Text probes.
* [ ] Verify per-probe wall-clock/CPU/memory enforcement.
* [ ] Verify aggregate request-level context budget.
* [ ] Verify graceful degradation after syntax failure, timeout, cancellation, or budget exhaustion.
* [ ] Land `TestDegradedContextCannotIssueMutationContract`.
* [ ] Land `TestHighFidelityMutationRequiresStructuralContext`.
* [ ] Land `TestTargetedMutationMayUseLowerContextTierWhenContractAllows`.
* [ ] Land `TestHumanAuthorizationCanExplicitlyPermitDegradedMutation` only if this exception is explicitly defined by the confirmed model.
* [ ] Land `TestAmbiguousIntentCannotUseRiskScopeUnderDegradedContext`.
* [ ] Verify Risk Scope quantitative blast-radius enforcement.
* [ ] Verify Risk Scope lifespan and revocation.

## Gate C — Runtime Truth & Recovery

* [ ] Verify RuntimeHost is the sole production mutation authority.
* [x] Verify provider usage comes from authoritative provider/runtime evidence.
* [x] Verify UI cannot render mutation success before MutationEvidence.
* [x] Verify invocation retries preserve Contract ID. (v0.5.0: pure transport retries derive identical contract identity; `TestUsageAggregationAcrossRecovery`.)
* [x] Verify invocation retries preserve scope/capability/domain. (v0.5.0: pure retries change no operational parameter.)
* [x] Verify Contract Recovery changes a canonical operational parameter. (v0.5.0: typed transitions change artifact protocol + causal lineage; `TestConformanceB_DriverOutputExhaustionSingleTypedTransitionThenHalt`.)
* [ ] Verify fresh snapshot creation after terminal contract completion.
* [ ] Verify re-baselining does not erase concurrency conflicts.
* [ ] Verify `PARTIALLY_APPLIED` produces explicit domain outcomes.
* [ ] Land `TestPartialApplicationTaintsSession`.
* [ ] Land `TestTaintedSessionCannotSilentlyIssueNormalMutationContract`.
* [ ] Verify taint clears only after verified recovery.

## Gate D — Mutation, OCC & Operational Cost

* [ ] Inspect RuntimeHost staging before physical file commit.
* [ ] Verify `WorkspaceStateConflict` under simulated concurrent writes.
* [ ] Verify fingerprint granularity matches mutation strategy.
* [ ] Measure staging I/O cost.
* [ ] Measure OCC fingerprinting cost.
* [ ] Measure snapshot creation cost.
* [ ] Measure conflict rates under LSP, formatter, watcher, and concurrent tooling.
* [ ] Verify mixed-domain contract rejection/decomposition.
* [ ] Verify external side-effects occur only after verified State-bearing commit.
* [ ] Verify independent evidence for State-bearing and External Side-effect outcomes.

---

# Operational Measurement Model

Performance claims MUST be based on measurements from the implementation.

The audit SHOULD decompose representative execution latency as:

```text
T_total =
    T_intent
  + T_context
  + T_staging
  + T_occ
  + T_provider
  + T_verification
  + T_commit
```

The decomposition is analytical rather than architectural. Individual terms may be absent for Reduced Surface execution.

### Required Measurements

| Component    | Measurement                               |
| ------------ | ----------------------------------------- |
| Intent       | p50 / p95 / p99 resolution latency        |
| Context      | compilation latency and resource usage    |
| Staging      | snapshot/staging I/O latency              |
| OCC          | fingerprint computation latency           |
| Provider     | provider/model latency and retry overhead |
| Verification | verification latency                      |
| Commit       | physical mutation/commit latency          |

### Required Comparison

The audit MUST compare measured infrastructure overhead against provider/model execution time.

Example:

```text
Infrastructure Overhead
        vs
Provider / Model Latency
```

The purpose is not to prove that the architecture has zero overhead.

The purpose is to determine whether architectural safeguards introduce a material operational bottleneck.

> **Architectural Cost ≠ Execution Cost ≠ Implementation Cost.**

A boundary may be architecturally necessary while its current implementation is operationally inefficient.

---

# Audit Closure Matrix

At closure, every major boundary MUST have:

| Boundary                    | Architectural Status | Implementation Status | Operational Status | Evidence              |
| --------------------------- | -------------------- | --------------------- | ------------------ | --------------------- |
| Intent & Dynamic Surface    | CONFIRMED            | `[STATUS]`            | `[STATUS]`         | `[TESTS / BENCHMARK]` |
| Context Compiler            | CONFIRMED            | `[STATUS]`            | `[STATUS]`         | `[TESTS]`             |
| Capability Grant            | CONFIRMED            | `[STATUS]`            | N/A                | `[TESTS]`             |
| Invocation / Recovery       | CONFIRMED            | VERIFIED              | UNMEASURED         | §0.6 test matrix      |
| Mutation / OCC              | CONFIRMED            | `[STATUS]`            | `[STATUS]`         | `[TESTS / BENCHMARK]` |
| Runtime Evidence / UI       | CONFIRMED            | `[STATUS]`            | N/A                | `[TESTS]`             |
| Domain Isolation            | CONFIRMED            | `[STATUS]`            | N/A                | `[TESTS]`             |
| Context Budget              | CONFIRMED            | `[STATUS]`            | `[STATUS]`         | `[TESTS / BENCHMARK]` |
| Risk Scope                  | CONFIRMED            | `[STATUS]`            | N/A                | `[TESTS]`             |
| Context Fidelity            | CONFIRMED            | `[STATUS]`            | N/A                | `[TESTS]`             |
| Partial Application / Taint | CONFIRMED            | `[STATUS]`            | N/A                | `[TESTS]`             |
| Execution Pipeline (B1–B5, I1–I5) | CONFIRMED      | VERIFIED              | UNMEASURED         | §0.6 conformance suite |

---

# Release Gate

The following are release-blocking until resolved or explicitly accepted with sufficient evidence:

* Any `VIOLATION` on Runtime Host mutation authority.
* D-002 while the intended production mutation path remains dormant without an accepted, fully evidenced equivalence argument.
* Any confirmed bypass of Capability Grant or Runtime Host authority.
* Any confirmed State-bearing mutation reported as successful when the mutation was only partial.
* Any confirmed mixed-domain execution violating SM-011.
* Any confirmed mutation contract issued under degraded context contrary to SM-012.
* Any confirmed `AMBIGUOUS ∧ DEGRADED_CONTEXT` execution that bypasses fail-closed policy.
* Any session that silently continues normal mutation execution after unresolved `PARTIALLY_APPLIED` state.

Performance regressions alone do not constitute architectural violations unless an explicit architectural or implementation performance bound has been ratified.

---

# Audit Principle

```text
Constitution
    ↓
Canonical System Model
    ↓
Implementation
    ↓
Executable Evidence
    ↓
Runtime Evidence
    ↓
Operational Measurement
    ↓
Conformance Status
```

The audit must never manufacture certainty.

If the implementation is unknown, record `UNKNOWN`.

If it is partial, record `PARTIAL`.

If it violates the model, record `VIOLATION`.

Only evidence earns `VERIFIED`.

The audit therefore preserves three distinct truths:

```text
Architectural Truth
        ≠
Implementation Truth
        ≠
Runtime Truth
```

and additionally distinguishes:

```text
Correctness
    ≠
Operational Efficiency
```

The Constitution defines what Izen must be.

The Canonical System Model defines how those constitutional boundaries compose.

The Implementation Audit determines whether the code actually conforms.

Executable tests establish correctness claims.

Runtime evidence establishes observed execution truth.

Benchmarks establish operational cost.

No layer may substitute for another.

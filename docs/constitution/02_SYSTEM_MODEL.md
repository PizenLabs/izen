# Izen Constitution — System Model

| Field | Value |
|---|---|
| Status | CONFIRMED |
| Version | 0.3.0 |
| Role | Canonical System Model & Semantic Boundaries |

## 1. System Philosophy & Core Motto

Izen is governed by a unified architectural principle:

> **Dynamic Path, Static Authority, Truthful State Transition.**

- **Dynamic Path:** The execution pipeline instantiates only the semantic boundaries required by the parsed Intent. Unnecessary runtime boundaries remain non-activated regardless of user interaction modes.
- **Static Authority:** Optimization of execution paths shall never weaken authority guarantees, capability boundaries, or evidence enforcement.
- **Truthful State Transition:** No system mutation is recognized as successful without verifiable runtime evidence proving that the declared mutation domain achieved its intended baseline.

## 2. Dynamic Execution & Surface Determination

Execution surface is determined strictly by the semantic requirements of the Intent, not by workspace interaction modes (`/ask`, `/build`).

**Intent Parsing Matrix:**

```
Input Payload ──► Intent Classifier ──► Classification Confidence ──┬─► READ_ONLY  ──► Reduced Surface
                                                                     ├─► MUTATION   ──► Full Surface
                                                                     └─► AMBIGUOUS  ──► No Mutation Authority
```

### 2.1 Ambiguity Resolution — Fail-Closed

Classification confidence is not binary. When the Intent Classifier cannot resolve input to `READ_ONLY` or `MUTATION` with sufficient confidence, the system MUST NOT default to the higher-privilege surface.

```
Intent Classification
        │
        ├── READ_ONLY ──────────► Reduced Surface
        │
        ├── MUTATION ───────────► Strict Surface
        │
        └── AMBIGUOUS ──────────► No Mutation Authority
                                       │
                                       ▼
                                Clarification / Confirmation
```

> Classifier output is evidence for an authorization decision. It is not authorization itself.

**Bound:** Ambiguous classification resolves to Reduced Surface with no mutation capability available, regardless of Workspace Mode. The system must seek clarification or an explicit human confirmation before a Full Surface may be instantiated. This is a direct extension of **I-002 — No Implicit Authority**: uncertainty in classification is context, not authority.

> **Invariant — Fail-Closed Ambiguity:** `Ambiguous Intent → Reduced Surface`, never `Ambiguous Intent → Full Surface`. Escalation to Full Surface requires a positively resolved `MUTATION` classification or explicit human confirmation — never the absence of a `READ_ONLY` classification.

### 2.2 Risk-Scope Pre-Authorization & Quantitative Blast Radius

Fail-closed does not require that every `AMBIGUOUS` classification interrupt the human. A human MAY, in advance and as explicit policy, grant a bounded, narrowly-scoped Risk Scope covering a specific low-risk capability class — for example, formatting-only changes to already-open files.

When an `AMBIGUOUS` classification's resolved operation falls entirely within an active Risk Scope, Runtime may proceed without an interactive prompt.

To prevent broad semantic permissions from inflicting large physical collateral damage, a Risk Scope MUST define both semantic and physical limits:

```
RiskScope
 ├── Capability & Operation Class
 ├── Target Scope Set
 ├── Lifespan / Expiry
 ├── Provenance
 └── Quantitative Blast Radius
      └── Measurable Physical Limit (e.g., max_files, max_lines, max_bytes, max_target_regions)
```

**Blast-Radius Bound:** Authorization bounded only by semantic class (e.g., "formatting") is insufficient. A Risk Scope MUST define a measurable bound on physical mutation impact appropriate to its operation class and mutation strategy. An operation exceeding the physical blast-radius bound invalidates Risk Scope coverage and triggers fail-closed clarification.

This does **not** weaken I-002:

- The authorization was granted explicitly, by a human, ahead of time.
- Ambiguity never creates a Risk Scope; it can only fall through to one that already exists.
- Absent a matching Risk Scope, `AMBIGUOUS` still resolves to Reduced Surface and clarification.
- A Risk Scope is bounded by operation class, target set, and physical blast radius; has an explicit lifespan; remains inspectable; and is individually revocable.
- A Risk Scope MUST NOT silently expand because an Intent is ambiguous.
- Authorization provenance matters: a capability is valid because an explicit grant already exists, not because the classifier failed to resolve the request.

### Execution Surface Profiles

**A. Reduced Surface (Non-Mutating Execution)**

Applies when Intent requires information retrieval, reasoning, or response generation.

```
Intent → Context Compiler → Model Invocation → Response
```

Non-activated boundaries: Capability Grant, Artifact Validation, Execution Contract, Runtime Mutation, Mutation Evidence.

**B. Full Surface (Mutating Execution)**

Applies when Intent explicitly requires workspace modification or side-effect execution.

```
Intent → Context → Eligibility → Decision → Proposal → Grant → Artifact → Contract → Runtime Host → Evidence → Verification
```

> **Invariant — Boundary Non-Activation:** Only semantically required boundaries are instantiated on an execution path. Non-activation eliminates runtime overhead without bypassing or diluting security boundaries.

## 3. Principal Entities & Semantic Boundaries

### 3.1 Intent

The formal interpretation of Human input declaring objectives, target scope, and constraints.

> **Bound:** Intent classification assigns target scope but **never** grants capability or execution authority.

### 3.2 Context Compiler

The component responsible for aggregating workspace information for reasoning.

- **Latency Policy:** Executes context probes (Graph AST, Symbol Index, Text Search) in parallel under both per-probe and aggregate request-level resource budgets.
- **Quality Policy:** Selects context using deterministic quality ranking:

```
Graph AST > Symbol Index > Text / Search
```

**Probe Budget.** Each probe MUST be constrained by an explicit resource triple:

```
ProbeBudget
 ├── Wall-Clock Ceiling
 ├── CPU/Worker Ceiling
 └── Memory Ceiling
```

The Context Compiler also operates under an **aggregate request-level budget** across all probes combined.

A probe that exceeds any leg of its budget is treated as a probe failure, not a hang, and falls through to Graceful Context Degradation (§6, SM-008). Aggregate budget exhaustion likewise degrades the context path rather than silently stalling the request.

> Specific numeric thresholds (ms, worker count, MB) are operational parameters and belong in `IMPLEMENTATION_AUDIT.md`.

### 3.3 Agent

An untrusted reasoning participant that interprets context to produce proposals or artifacts.

> **Bound:** An Agent possesses zero capabilities and zero execution authority.

### 3.4 Invocation Scope vs. Contract Scope

- **Invocation Parameters:** Ephemeral parameters governing transport and model sampling (`model`, `temperature`, `top_p`, `max_tokens`, retry metadata).
- **Contract Semantics:** Structural and operational requirements defining the execution boundary. Canonical contract dimensions include:

```
target_scope
mutation_domain
required_capabilities
preconditions
validation_rules
artifact_representation
resource_constraints
escalation_behavior
```

> **Bound:** Prompt content is a representation payload. Ephemeral adjustments to prompt phrasing, sampling, or transport metadata do **not** alter the ExecutionContract unless a canonical operational contract dimension changes.

### 3.5 Runtime Host

The trusted execution boundary that validates preconditions against ExecutionSnapshot, enforces capabilities, executes mutations, and records MutationEvidence.

### 3.6 Canonical Authority Table

This table is the **single source of truth** for what each entity owns and does not own. Domain documents (`03_WORKSPACES.md`, `04_INTENT.md`, `05_AUTONOMY.md`, `06_EXECUTION.md`) must reference this table rather than independently redefining ownership semantics.

| Concept | Owns | Does Not Own |
|---|---|---|
| Intent | Request interpretation, target scope declaration | Authorization, capability |
| Workspace | Contextual/physical boundary, operational scoping | Capability, execution strategy |
| Agent | Reasoning, proposal generation | Execution, capability, mutation authority |
| Capability Grant | Authorization of an operation class, within scope | Mutation approval for arbitrary output; self-elevation |
| Execution Contract | Execution conditions and operational constraints | Physical mutation |
| Runtime Host | Mutation authority, physical execution, evidence recording | Human intent, capability self-issuance |
| Evidence | Execution truth (what actually happened) | Authorization, verification outcome |
| Verification | Post-mutation correctness assessment | Mutation result itself |

> Canonical semantics live once, here. Contextual explanation may repeat across domain documents; ownership definitions must not.

## 4. Mutation Domains & Atomicity Model

Side-effect execution is explicitly partitioned into distinct domains based on rollback physics.

```
ExecutionContract
 └── Mutation Domain Classification
      ├── State-bearing Domain (Filesystem, Config) ──► Hard Atomicity
      └── External Side-effect Domain (Shell, Network) ──► Non-Transactional
```

### 4.1 State-bearing Domain

State-bearing mutations are bound to an `ExecutionSnapshot`:

```
ExecutionSnapshot
 ├── Target Set
 ├── Precondition Fingerprints
 ├── Baseline State Identity
 └── Isolated Staging Buffer
```

**Protocol: 3-Phase Atomic Commit**

1. **Prepare & Validate:** Record target fingerprints; compare against ExecutionSnapshot baseline. Abort on mismatch (`WorkspaceStateConflict`).
2. **Stage & Commit:** Apply mutations inside an isolated staging area and commit all target changes to physical state.
3. **Verify & Evidence:** Validate post-commit state and record cryptographic hashes into MutationEvidence.

Within a declared State-bearing Mutation Domain, execution is all-or-nothing. If a multi-target commit cannot complete coherently, the Runtime MUST NOT report success and MUST restore the baseline state where rollback is physically possible.

**Fingerprint Scope.** Fingerprint granularity MUST correspond to the semantic blast radius of the declared mutation strategy.

- Whole-file identity is appropriate for full-file rewrites.
- Targeted mutation MAY use a byte-range or AST-node identity when that identity safely covers the intended mutation region.
- A bounded settle/debounce window MAY be used before snapshot capture to reduce races with known background tooling.

Such optimizations do not weaken OCC. A conflict within the declared mutation target remains a conflict.

### 4.2 External Side-effect Domain

Commands causing non-transactional external state transitions (for example package managers, remote API calls, or other external processes) cannot offer physical filesystem atomicity.

They SHOULD execute within an isolated environment where feasible. Failures in this domain MUST be represented explicitly and MAY trigger cleanup or compensation when such actions are known to be safe and effective.

`UnrollbackableSideEffect` is evidence of an external effect that cannot be guaranteed to be physically reversed; it is **not** itself a claim that the entire logical operation failed to mutate.

### 4.3 Mutation Domain Isolation

A single `ExecutionContract` MUST NOT combine a State-bearing mutation with an External Side-effect as one atomic unit.

Where a logical operation genuinely requires both, Runtime MUST decompose it into ordered, independently-evidenced contracts:

1. The State-bearing operation commits first.
2. The State-bearing commit is verified.
3. The External Side-effect executes only after that verification.
4. The outcome of each domain is recorded independently.

If the External Side-effect fails, the system MUST NOT falsely report the combined operation as fully successful.

The preferred recovery disposition is re-execution of the failed external operation. Where a safe compensating action is explicitly defined, a separate **Compensation Contract** MAY be issued. Compensation is not assumed to be universally possible and MUST NOT be treated as guaranteed rollback.

### 4.4 Partial Application Semantics

Cross-domain operations can produce a legitimate partial state:

```
State-bearing Commit       ✓
External Side-effect       ✗
--------------------------------
Logical Outcome        PARTIALLY_APPLIED
```

When this occurs, Runtime MUST preserve truthful outcome state and independent evidence:

```
State-bearing mutation: COMMITTED / FAILED
External side-effect:    COMMITTED / FAILED / UNKNOWN
Compensation:            NOT_REQUIRED / AVAILABLE / ATTEMPTED / UNAVAILABLE
Recovery requirement:    NONE / RETRY / HUMAN_ACTION
```

A partial outcome is not converted into generic success or hidden behind a generic failure label.

> **Truth rule:** Runtime Truth requires accurate representation of each mutation domain's physical outcome; it does not require that every external side-effect be reversible.

## 5. Recovery vs. Retry Semantics & Snapshot Lifecycle

The system decouples model transport retries from architectural state recovery.

```
ExecutionContract (N)
 │
 ├── Invocation Attempt N.1 ──► Parse Error
 ├── Invocation Attempt N.2 ──► Success
 │
 └── Execution Failure / Partial Application
      │
      ▼
 Contract Recovery Transition
      │
      ▼
 ExecutionContract (N+1)
```

> **Invariant — Contract Identity:** Re-issuing invocations with modified sampling parameters, prompt phrasing, or transport metadata does **not** alter the active ExecutionContract.

A transition to `Contract(N+1)` occurs if and only if at least one canonical operational contract dimension materially changes:

```
Contract(N+1) ≠ Contract(N)
    ⟺
Δ(
  Target Scope,
  Capabilities,
  Mutation Domain,
  Validation Rules,
  Artifact Representation,
  Resource Constraints,
  Escalation Behavior
) ≠ ∅
```

### Snapshot Lifecycle & Re-baselining

When an `ExecutionContract(N)` reaches a terminal state (COMMITTED and VERIFIED), its physical modifications become part of the workspace state. Any subsequent contract (`Contract(N+1)` or a Compensation Contract) MUST capture a **fresh** ExecutionSnapshot.

```
Contract N Baseline
       │
       ▼
Contract N Commit & Verification (Terminal)
       │
       ▼
   Fresh Snapshot
       │
       ▼
Contract N+1 Baseline
```

**Re-baselining Rules:**

- **Terminal Precondition:** A fresh snapshot MAY be captured only after Contract(N) has terminated and its evidence has been recorded.
- **No Concurrency Erasure:** Re-baselining MUST NOT retroactively erase concurrency conflicts or override OCC.
- **Observed Workspace State:** Any uncommitted external changes that occurred between Contract(N) termination and Contract(N+1) snapshot capture become part of the new observed baseline state.
- **Target Scoping:** The fresh snapshot is evaluated strictly against the target scope declared for Contract(N+1).

> **Recovery Re-baselining Principle:** Recovery contracts are permitted to establish a fresh snapshot baseline; recovery contracts are **never** permitted to erase concurrency history or bypass baseline validation.

## 6. Context Degradation Architecture & Mutation Eligibility

Izen treats codebase structural intelligence as a quality spectrum.

```
                 Target Intent Scope
                         │
                         ▼
                 ┌──────────────────┐
                 │ Context Compiler │
                 └────────┬─────────┘
                          │
              Parallel / Bounded Probes
             ┌────────────┼────────────┐
             ▼             ▼             ▼
        Graph AST    Symbol Index   Text/Search
             │            │            │
             └────────────┼────────────┘
                          ▼
                 Quality Priority Engine
                          │
                          ▼
                   Resolved Context
                          │
             ┌────────────┴────────────┐
             ▼                         ▼
      Information Flow        Mutation Eligibility
      (Degrades gracefully)   (Gated by Context Fidelity)
```

> **Invariant — Graceful Context Degradation:** Failure, timeout, budget exhaustion, or unavailability of a structural indexer must not cause failure of the reasoning or information-retrieval path. The Context Compiler must degrade to lower-tier information sources without aborting execution.

### Information Flow vs. Mutation Eligibility

Graceful Context Degradation governs information availability for reasoning and read-only queries (`/ask`, `/investigate`). It does **not** grant execution authority for workspace mutations (`MUTATION`).

```
Context Quality  ≠  Capability  ≠  Execution Authority
```

Context Fidelity is a mandatory precondition for ExecutionContract issuance on mutating paths. An ExecutionContract requiring `MUTATION` MUST declare a minimum context-fidelity requirement appropriate to its mutation strategy and physical blast radius.

If compiled context falls below the declared fidelity threshold, an ExecutionContract for mutation MUST NOT be issued prior to Runtime execution, regardless of workspace mode or user prompt phrasing.

## 7. Canonical System Invariants

The following invariants are absolute and testable:

| ID | Name | Statement |
|---|---|---|
| SM-001 | Authority Invariance | Optimization of execution surfaces shall not decrease authority guarantees. |
| SM-002 | Boundary Non-Activation | Only semantically necessary boundaries shall be instantiated for a request path, determined by resolved Intent rather than workspace mode. |
| SM-003 | Agent Authority Isolation | Agents generate untrusted proposals. Execution capability is granted exclusively to the Runtime Host. |
| SM-004 | Invocation Decoupling | Sampling parameters and transport metadata belong to Invocation scope, not Execution Contract scope. |
| SM-005 | Contract Recovery Integrity | Contract recovery requires a material change in at least one canonical operational contract dimension; invocation-only changes do not create a new contract. |
| SM-006 | Bounded Mutation Atomicity | Within a declared State-bearing Mutation Domain, execution is all-or-nothing. Partial filesystem mutations are not successful and must be rolled back where physically possible. |
| SM-007 | Side-effect Domain Boundary | Operations involving external non-transactional processes must not claim filesystem rollback guarantees and must be explicitly bounded as External Side-effects. |
| SM-008 | Graceful Context Degradation | Indexer failure, timeout, or probe-budget exhaustion must degrade context retrieval to fallback probes without aborting the reasoning path. |
| SM-009 | Runtime Evidence Primacy | Workspace mutations are recognized as execution truth solely through authoritative MutationEvidence recorded by the Runtime Host. |
| SM-010 | Optimistic Concurrency Control | Mutations must validate target workspace identity against the ExecutionSnapshot baseline before physical commit. Fingerprint scope must cover the declared mutation blast radius. |
| SM-011 | Mutation Domain Isolation | An ExecutionContract MUST NOT combine State-bearing and External Side-effect operations as one atomic unit. Where both are required, State-bearing commit MUST occur and be verified before the External Side-effect executes, with independent outcomes and evidence. |
| SM-012 | Mutation Context Fidelity | An ExecutionContract requiring mutation MUST declare a minimum context-fidelity requirement appropriate to its mutation strategy and semantic blast radius. If compiled context falls below that requirement, the system MUST NOT issue the mutation contract unless explicitly permitted by human confirmation or an active matching Risk Scope. |

> **Note on SM-002:** An unresolved Intent is not a positive determination. SM-002 and I-002 jointly require that ambiguous classification remain at Reduced Surface unless an already-existing, explicitly granted Risk Scope applies or the human explicitly confirms escalation. Ambiguity itself never creates authority.

## 8. Architectural Layering & Boundary

System documentation and operational boundaries remain strictly segregated:

```
00_IZEN.md (Constitution)
  └── Immutable Core, Authority Axioms, Philosophical Bounds (WHY)

02_SYSTEM_MODEL.md (System Model)
  └── Semantic Entities, Boundary Activation, Mutation Domains, Invariants (WHAT)

IMPLEMENTATION_AUDIT.md (Audit & Verification)
  └── Forensic Code Conformance, Performance Thresholds, Latency Budgets,
      Race Conditions, Evidence, and Release Gates (DOES IT CONFORM?)
```

Operational threshold numbers (latency budgets, memory allocations, staging algorithms, concrete provider behavior) belong exclusively in `IMPLEMENTATION_AUDIT.md` and engineering benchmarks.

The System Model defines what must be true. It does not prescribe a specific storage backend, LSP implementation, staging mechanism, hashing algorithm, lock strategy, or compensation implementation.

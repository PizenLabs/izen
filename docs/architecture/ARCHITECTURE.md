# Izen Architecture

Izen is a human-centered **Agent Runtime & Smart Harness Engine** that separates reasoning from authority, intent from execution, workflow state from persistent artifacts, and resource efficiency from correctness.

> **LLMs reason. The Engine decides. Capabilities execute. Humans remain in control.**

Izen is designed to operate across models, providers, programming languages, repositories, workspaces, and execution environments.

A model may be powerful or constrained. A provider may be local, remote, paid, free-tier, cached, uncached, fast, slow, reliable, or rate-limited. A workspace may be a mature repository with compilers and tests or a cold-start directory containing a single file.

Izen must remain useful and correct across all of these environments without encoding provider-specific behavior, language-specific correctness, or model-specific assumptions into the Runtime Core.

Izen therefore does not attempt to make every task look like a single model invocation.

It does not optimize for the smallest prompt, the largest prompt, the smallest output, the largest output, or the fewest requests independently.

It optimizes for:

> **the smallest sufficient execution that can reliably advance and complete the user's objective.**

Simplicity is therefore not the absence of intelligence.

> **Izen spends complexity, context, authority, and computation only where they provide useful execution value.**

---

# 0. Architectural Foundation

```text
HUMAN
  │
  │ intent · approval · correction · final control
  ▼
CONTROL PLANE
  │
  │ validates · plans · authorizes · budgets · compiles
  │ observes · verifies · evaluates · adapts
  ▼
EXECUTION PLANE
  │
  │ read · write · execute · test · patch
  │ checkpoint · rollback
  ▼
WORKSPACE
```

The **Control Plane** owns meaning, policy, workflow, authority, and execution decisions.

The **Execution Plane** performs operations that have already been authorized.

The LLM is a reasoning component.

Capabilities are execution and evidence providers.

Neither the LLM nor an individual capability provider owns system authority.

---

# 1. Architectural Philosophy

## 1.1 Reasoning Is Not Authority

The LLM may:

* interpret user intent
* analyze evidence
* identify relationships
* generate hypotheses
* generate plans
* propose mutations
* classify failures
* suggest recovery strategies
* generate explanations

The LLM may not:

* grant itself capabilities
* expand scope
* bypass approval
* bypass checkpoints
* authorize mutations
* execute arbitrary commands outside its granted execution contract
* mutate outside authorized scope
* consume stale artifacts
* redefine workflow state
* redefine budgets
* approve its own proposal
* silently recover from unknown failure
* declare objective completion merely because output was generated or a file changed

The fundamental flow is:

```text
LLM
  ↓
Proposal
  ↓
Engine Validation
  ↓
Capability Authorization
  ↓
Approval Policy
  ↓
Execution
  ↓
Evidence
  ↓
Engine Decision
```

---

## 1.2 Authority Topology Is Singular

Izen must maintain one authoritative Control Plane mutation path.

There must not be multiple independent authorities hidden behind:

* commands
* modes
* TUI handlers
* provider adapters
* model clients
* subagents
* retry handlers
* recovery handlers
* verification tools

A refactor is not complete merely because directory structure changed.

A refactor is architecturally meaningful only when **authority ownership** becomes clearer.

> **One substrate. One authoritative execution decision path.**

---

## 1.3 Workflow, Artifacts, Capabilities, and Runtime State Are Independent

Izen maintains several orthogonal state dimensions.

| Dimension           | Question                                        | Examples                                          |
| ------------------- | ----------------------------------------------- | ------------------------------------------------- |
| **Workflow State**  | Where is the workflow?                          | `IDLE`, `PLANNING`, `BUILDING`, `REVIEWING`       |
| **Artifacts**       | What has been reasoned about or decided?        | `Intent`, `Evidence`, `Plan`, `Patch`, `Review`   |
| **Capabilities**    | What may the system do?                         | `READ`, `WRITE`, `TEST`, `PATCH`                  |
| **Execution State** | What is happening inside the current execution? | unit, attempt, budget, fingerprints, observations |

These dimensions must never collapse into one another.

```text
CHECKPOINT EXISTS
≠
APPROVAL GRANTED

CAPABILITY GRANTED
≠
ACTION AUTHORIZED

PATCH CREATED
≠
PATCH APPLIED

PATCH APPLIED
≠
OBJECTIVE COMPLETED

ARTIFACT CHANGED
≠
PROGRESS

OUTPUT EXHAUSTED
≠
TASK FAILED

OUTPUT EXHAUSTED
≠
OBJECTIVE COMPLETED

NO VERIFIER
≠
VERIFIED
```

---

# 2. The Izen Execution Model

Izen operates as three nested control loops rather than one undifferentiated agent loop.

```text
┌─────────────────────────────────────────────────────────────┐
│                     WORKFLOW LOOP                           │
│                                                             │
│ Intent → Evidence → Plan → Build → Review                   │
│                                                             │
│   ┌─────────────────────────────────────────────────────┐   │
│   │                  EXECUTION LOOP                     │   │
│   │                                                     │   │
│   │ Unit → Proposal → Mutation/Response → Observe       │   │
│   │             ↓                                       │   │
│   │       Continue / Replan / Stop                      │   │
│   │                                                     │   │
│   │   ┌─────────────────────────────────────────────┐   │   │
│   │   │              MODEL INTERACTION              │   │   │
│   │   │ Context → Model → Output                   │   │   │
│   │   └─────────────────────────────────────────────┘   │   │
│   └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

### Workflow Loop

Answers:

> What phase is the user in?

### Execution Loop

Answers:

> What bounded work should happen next?

### Model Interaction

Answers:

> What proposal or reasoning did the model produce for this execution unit?

This separation prevents:

* state explosion
* transcript-driven execution
* multiple mutation authorities
* model-specific workflow semantics
* coupling between UI and execution

---

# 3. Objectives

The **Objective** is the authoritative representation of what the user wants the system to accomplish.

An objective may contain:

```text
intent
constraints
target scope
negative scope
risk class
mutation requirements
evidence requirements
```

Objectives are independent of implementation language.

The runtime must optimize execution around the objective, not around the shape of the artifact.

Example:

```text
objective
  "Improve authentication reliability"
```

does not imply:

```text
Go
```

or:

```text
Rust
```

or:

```text
single file
```

or:

```text
one model request
```

The execution strategy derives from the objective and environment.

---

# 4. Execution Classes

Commands are interfaces to execution classes, not independent runtimes.

```text
READ_ONLY
ANALYSIS
PLANNING
MICRO_MUTATION
CONTROLLED_MUTATION
REVIEW
```

Examples:

| Interface      | Execution Class          |
| -------------- | ------------------------ |
| `$prompt`      | `READ_ONLY` / `ANALYSIS` |
| `/ask`         | `READ_ONLY`              |
| `/investigate` | `ANALYSIS`               |
| `/plan`        | `PLANNING`               |
| `/build $hot`  | `MICRO_MUTATION`         |
| `/build`       | `CONTROLLED_MUTATION`    |
| `/review`      | `REVIEW`                 |

All classes operate on the same substrate.

They differ by:

```text
capabilities
authority
budget
execution strategy
verification requirements
approval requirements
```

---

# 5. `$prompt` — First-Class Read/Reasoning Execution

`$prompt` is a first-class Izen execution path.

It is not a degraded `/build`.

It is the canonical primitive for asking Izen to reason without mutating the workspace.

```text
USER OBJECTIVE
  ↓
INTENT CLASSIFICATION
  ↓
COST / COMPLEXITY DECISION
  ↓
CAPABILITY-AWARE RETRIEVAL
  ↓
CONTEXT COMPILATION
  ↓
LLM REASONING
  ↓
RESPONSE
```

No mutation authority is introduced.

No checkpoint is created for mutation.

No write capability is silently granted.

No mutation completion state is created.

### Trivial Prompt

```text
$prompt hi
```

must not trigger:

```text
graph indexing
semantic search
repository scan
large context compilation
```

unless the execution environment explicitly requires it.

### Repository Reasoning

```text
$prompt explain how authentication works across this repository
```

may trigger:

```text
structure
→ symbols
→ dependencies
→ targeted retrieval
→ context compilation
→ model
```

The cost of the execution is proportional to the intent.

### 5.1 Prompt Ingestion and Untrusted Boundary

Raw user input strings (e.g., `$prompt explain @internal/auth/token.go`) undergo deterministic ingestion before becoming an immutable `Intent` artifact:

1. **Directive & Reference Parsing:** Extract execution directives (`$prompt`, `$hot`), scope symbols (`@path`), and raw intent text into a structured payload.
2. **Untrusted Context Isolation:** Any workspace content retrieved during `$prompt` execution MUST be wrapped inside explicit data boundaries (e.g., `<untrusted_context>`). 
3. **Data/Instruction Separation:** The LLM MUST evaluate referenced file contents strictly as input DATA to analyze, never as SYSTEM instructions to execute. Prompt directives cannot override Control Plane Invariants.

---

# 6. `/ask` — Information Retrieval

`/ask` provides read-only information retrieval.

It prefers deterministic retrieval before expensive reasoning.

Conceptual ordering:

```text
Structure / Symbol Information
        ↓
Semantic Retrieval
        ↓
Text / Filesystem Fallback
```

A whole-repository scan is not the default.

The engine should retrieve only information relevant to the objective.

---

# 7. `/investigate` — Evidence Discovery

`/investigate` discovers facts, diagnostics, traces, and root causes.

Evidence must distinguish:

```text
CONFIRMED
INFERRED
UNKNOWN
```

An inference may never silently become a confirmed fact.

Where deterministic discovery exists, it must be preferred over model guessing.

---

# 8. `/plan` — Decision Compiler

`/plan` compiles:

```text
Intent
+
Evidence
+
Constraints
+
Scope
```

into:

```text
Target Scope
Impact Graph
Execution Steps
Negative Scope
Dependencies
Validation Strategy
```

The Plan is an artifact.

It is not execution authority.

A normal plan enters:

```text
AWAITING_APPROVAL
```

before consumption by `/build`.

A plan is an executable decision blueprint, not merely a formatted model response.

---

# 9. `/build` — Controlled Mutation

`/build` consumes an authorized Plan Artifact or valid Micro-Plan.

It operates inside:

```text
scope
capability
budget
dependency
checkpoint
approval
verification
```

A user request that changes scope invalidates affected artifacts.

Example:

```text
Plan:
  Modify auth.go

New request:
  Also refactor persistence.
```

Required behavior:

```text
scope change detected
        ↓
invalidate affected plan
        ↓
return to planning
        ↓
re-approve
```

No silent scope expansion.

---

# 10. `/build $hot` — Bounded Micro-Plan

`$hot` is not an unsafe bypass.

It is a deliberately bounded Micro-Plan in which the invocation itself can constitute pre-approval for a small, explicitly defined blast radius.

Example:

```text
max_files: 2
max_diff_lines: 50
max_context: 2000
max_attempts: 1
checkpoint: required
scope_expansion: forbidden
```

Flow:

```text
Intent
  ↓
Micro-Plan
  ↓
Budget Validation
  ↓
Strategy Selection
  ↓
Checkpoint
  ↓
Execution Unit
  ↓
Mutation
  ↓
Review
  ↓
Execution Observation
  ↓
Continue / Replan / Stop
```

The important distinction is:

> `$hot` pre-approves a bounded execution shape, not an arbitrary future mutation.

---

# 11. `$hot` Boundary Breach

A Micro-Plan breach does not silently expand authority.

There are two possible situations.

### Before Mutation

If the proposal itself exceeds the Micro-Plan budget:

```text
Proposal
  ↓
Budget Check
  ↓
Breach
```

the proposal may be frozen as a draft artifact:

```text
PATCH DRAFT
```

and presented to the human for an explicit new decision.

No workspace mutation has occurred.

### After Mutation

If an unexpected mutation has already crossed an authorization boundary:

```text
MUTATION DETECTED OUTSIDE AUTHORIZED BOUNDARY
```

the normal safety path applies:

```text
STOP
→ ROLLBACK where safe
→ HUMAN_CONTROL / REPLAN
```

Izen must never pretend an already-applied mutation is still a draft.

---

# 12. Artifact Model

Every major phase may produce an immutable artifact:

```text
Intent
  ↓
Evidence
  ↓
Plan
  ↓
Patch
  ↓
Review
```

Each artifact contains:

```text
Identity
Lifecycle
Lineage
Dependencies
Source Snapshot
Creation Metadata
Storage Scope
```

---

## 12.1 Identity

Artifact identities must be globally unique.

They must not rely on sequential counters.

Examples:

```text
intent_<id>
evidence_<id>
plan_<id>
patch_<id>
review_<id>
```

---

## 12.2 Lineage

Artifacts record deterministic provenance.

```text
plan_002
  derived_from:
    intent_001
    evidence_004

plan_003
  supersedes:
    plan_002
```

History is immutable.

A new decision creates a new artifact.

---

## 12.3 Lifecycle

```text
DRAFT
  ↓
VALIDATED
  ↓
AWAITING_APPROVAL
  ↓
AUTHORIZED
  ↓
CONSUMED
  ↓
ARCHIVED
```

Exceptional states:

```text
STALE
INVALIDATED
REJECTED
```

`AWAITING_APPROVAL` is mandatory whenever explicit approval is required.

It is bypassed only when the artifact originates from a valid pre-approved Micro-Plan.

---

# 13. Artifact Dependencies and Source Freshness

Artifacts record the exact objects they depend on.

```json
{
  "depends_on": [
    {
      "kind": "file",
      "id": "internal/auth/token.go",
      "hash": "sha256:..."
    },
    {
      "kind": "symbol",
      "id": "AuthMiddleware.Validate",
      "hash": "sha256:..."
    }
  ]
}
```

Before an artifact is consumed:

```text
Expected Source State
        ↓
Current Workspace State
        │
        ├── MATCH → proceed
        │
        └── MISMATCH → STALE
```

This protects against:

* user edits
* branch switches
* merges
* rebases
* external formatters
* external tooling
* generated files
* parallel processes

Izen must never execute an artifact against an unknown source state.

---

# 14. Mutation Context Fidelity

Source-state fidelity and execution-cycle detection are separate concerns.

### Mutation Context Fidelity

Answers:

> Is the artifact/proposal still valid for the source state that exists at authorization and mutation time?

Owned by:

```text
artifact dependencies
source hashes
authorization
staleness validation
```

### State Fingerprinting

Answers:

> Has the execution controller returned to a previously observed execution state?

Owned by:

```text
execution state
progress control
cycle detection
```

These mechanisms must not be merged.

---

# 15. Capability Model

Capabilities are explicit and granular:

```text
READ
WRITE
EXECUTE
TEST
PATCH
CHECKPOINT
ROLLBACK
```

Capabilities can be scoped.

Examples:

```text
EXECUTE:
  test commands only

WRITE:
  internal/auth/*

PATCH:
  declared Micro-Plan scope
```

Example mode boundaries:

```text
/investigate:
  READ = granted
  TEST = granted
  EXECUTE = diagnostic-only
  WRITE = denied
  PATCH = denied

/build:
  READ = granted
  WRITE = granted
  TEST = granted
  PATCH = granted
  EXECUTE = restricted
```

The Capability Guard is the final authority before execution.

---

# 16. Execution Environment

Execution Strategy is derived from the current execution environment.

Conceptually:

```text
ExecutionEnvironment
├── ModelEnvironment
├── ProviderEnvironment
├── WorkspaceCapabilities
├── ResourceBudget
├── SourceState
└── CurrentEvidence
```

The Model Environment may include:

```text
context window
maximum output
reasoning capability
tool support
```

The Provider Environment may include:

```text
rate limits
request limits
cache capability
latency characteristics
cost characteristics
```

The Workspace Capabilities may include:

```text
parser
diagnostics
formatter
compiler
type checker
build
test
runtime validation
```

The Resource Budget may include:

```text
tokens
attempts
execution time
shell commands
files
diff size
```

---

# 17. Execution Strategy

`ExecutionStrategy` is a policy description for how the next execution unit should be performed.

It is not an authority.

It does not mutate.

It does not approve.

It does not bypass the Capability Guard.

Conceptually:

```text
Objective
+
Execution Environment
+
Current Plan
+
Current Evidence
+
Execution History
        ↓
Execution Strategy
```

The strategy consists of:

```text
Context Policy
Output Policy
Verification Policy
Continuation Policy
Progress Policy
```

---

# 18. Context Policy

Context is compiled according to the needs of the current execution unit.

Possible representations include:

```text
FULL
STRUCTURAL
REGIONAL
SYMBOL
DIFF
DELTA
SUMMARY
VERIFICATION
CONTINUATION
```

No representation is universally preferred.

The runtime asks:

> What is the minimum context that preserves the information required for this execution unit?

This requires proportionality rather than arbitrary truncation.

---

## 18.1 Context Must Preserve Required Information

The runtime must not destroy:

* required types
* function signatures
* interfaces
* dependency relationships
* configuration
* design tokens
* surrounding state
* constraints
* previous relevant evidence

merely to hit a token target.

The rule is:

> **Context reduction is valid only when the removed information is unnecessary for the current execution unit.**

---

# 19. Stable Prompt Regions and Provider Caching

Where provider caching is useful, Izen should prefer:

```text
STABLE PREFIX
  system contract
  tool contract
  execution protocol
  stable task specification

DYNAMIC SUFFIX
  current objective state
  current target
  current evidence
  current delta
  continuation information
```

The preferred layout is:

```text
┌─────────────────────────────┐
│ STABLE PROMPT CONTRACT      │
│ exact when reuse is useful  │
├─────────────────────────────┤
│ DYNAMIC EXECUTION CONTEXT   │
│ current unit/state/evidence │
└─────────────────────────────┘
```

However:

> **Cache preservation must never override context correctness.**

Changing context representation is acceptable when required for better execution.

Intentional cache invalidation should be observable as a telemetry event.

Examples:

```text
strategy change
target scope change
context recompilation
intent change
provider behavior change
```

Caching is an economic optimization.

It is not a correctness authority.

---

# 20. Output Policy and Progressive Budgeting

Izen must not attempt to predict the exact output length of an arbitrary task before execution.

Instead it allocates budget progressively.

```text
OBJECTIVE
  ↓
EXECUTION UNIT
  ↓
CHECK CURRENT OUTPUT CAPACITY
  ↓
ONE-SHOT or ITERATIVE
  ↓
OBSERVE
  ↓
REALLOCATE / REPLAN
```

A constrained model can therefore complete a complex objective through multiple bounded units.

A stronger model may complete the same objective in one unit.

The objective remains identical.

The strategy changes.

---

# 21. Execution Unit

An **Execution Unit** is the smallest bounded piece of work that the current model/capability environment can safely and usefully execute.

A unit has:

```text
identity
target scope
objective slice
input context
output budget
capability boundary
verification policy
local checkpoint
parent frame
```

The runtime may create units sequentially or as a dependency-aware structure.

It must not assume every task is linear.

Execution may be:

```text
single
linear
tree-like
DAG-like
iterative
hybrid
```

---

# 22. Execution Frames and Isolation

Each execution unit belongs to an `ExecutionFrame`.

Conceptually:

```text
ROOT OBJECTIVE
  │
  ├── FRAME / UNIT 1
  │      ↓
  │   completed
  │
  ├── FRAME / UNIT 2
  │      ↓
  │   failed
  │
  └── FRAME / UNIT 3
         ↓
      pending
```

Each frame may hold:

```text
parent frame
execution unit
scope
checkpoint
strategy
attempt count
dependencies
observations
status
```

This creates an explicit rollback boundary.

Rollback granularity is determined from:

```text
checkpoint
+
dependency graph
+
artifact lineage
+
failure scope
```

The runtime may perform:

```text
LOCAL_ROLLBACK
ANCESTRAL_ROLLBACK
TASK_ROLLBACK
```

depending on dependency structure and policy.

---

# 23. Execution Observation

The runtime requires an explicit observation boundary after an execution unit.

An observation may include:

```text
execution unit identity
proposal outcome
mutation result
output status
budget usage
dependency freshness
verification evidence
failure signals
state fingerprint
```

Observation describes what happened.

It does not itself authorize the next action.

---

# 24. Feedback Controller

The Execution Controller consumes observation and determines:

```text
CONTINUE
REPLAN
STOP
```

Conceptually:

```text
Execution Unit
    ↓
Proposal
    ↓
Execution
    ↓
Observation
    ↓
Controller
    │
    ├── CONTINUE
    ├── REPLAN
    └── STOP
```

The controller is part of the Control Plane.

It is not a second mutation authority.

Iteration changes execution strategy, not authority.

---

# 25. Progress Semantics

Progress is not a single scalar.

Izen should track a **Progress Vector**.

Possible signals include:

```text
state_changed
constraint_satisfied
evidence_improved
dependency_resolved
verification_strength_increased
failure_removed
cycle_detected
```

Each signal may be:

```text
TRUE
FALSE
UNKNOWN
```

No signal alone proves completion.

In particular:

```text
diff size
≠
progress

bytes changed
≠
progress

AST valid
≠
progress

model produced output
≠
progress
```

Progress exists to help the controller decide whether another execution unit is justified.

---

# 26. State Fingerprinting

The runtime maintains a bounded history of canonical execution states.

Conceptually:

```text
S0 → S1 → S2 → S3
```

If:

```text
S3 == S1
```

a cycle has been detected.

State fingerprints should represent semantic or canonical execution state as far as workspace capabilities permit.

Preferred mechanisms may include:

```text
language-aware canonical representation
normalized tokens
normalized source
structural representation
raw artifact hash as final fallback
```

The Core must not require AST support.

The fingerprinting implementation may be language-aware when a provider can supply the necessary normalization.

State fingerprints are used for:

```text
cycle detection
oscillation detection
stagnation protection
safe replanning
```

They are not proof of semantic correctness.

---

# 27. Evidence Model

Capabilities provide evidence.

The Core consumes evidence without depending on the mechanism that generated it.

Possible evidence sources:

```text
artifact integrity
parser
AST
LSP
linter
compiler
type checker
build
tests
runtime validation
diff analysis
workspace commands
```

Evidence describes what the system can substantiate.

---

# 28. Verification Capability Model

Capabilities are the canonical model.

A verification level is a derived classification for policy, UI, and telemetry.

Conceptually:

```text
LEVEL 0
No verifier

LEVEL 1
Artifact integrity

LEVEL 2
Structural integrity

LEVEL 3
Diagnostics / static analysis

LEVEL 4
Build / type checking

LEVEL 5
Tests / runtime validation
```

The runtime must not assume that every language or workspace supports every level.

A workspace may provide arbitrary combinations of capabilities.

---

# 29. Verification Policy

Verification policy determines what evidence is required for the current execution class and objective.

Absence of a verifier does not automatically make execution impossible.

It does mean that evidence strength is constrained.

Example:

```text
Mutation applied
+
artifact integrity pass
+
no compiler
+
no tests
```

may produce:

```text
COMPLETED · PARTIALLY_VERIFIED
```

or:

```text
COMPLETED · UNVERIFIED
```

depending on available evidence and policy.

It must never produce:

```text
COMPLETED · VERIFIED
```

without sufficient evidence.

---

# 30. Completion Semantics

Execution Outcome and Evidence State are orthogonal.

## Execution Outcome

```text
COMPLETED
INCOMPLETE
FAILED
ABORTED
```

## Evidence State

```text
VERIFIED
PARTIALLY_VERIFIED
UNVERIFIED
```

A terminal state is therefore:

```text
ExecutionOutcome × EvidenceState
```

Examples:

```text
COMPLETED · VERIFIED
COMPLETED · PARTIALLY_VERIFIED
COMPLETED · UNVERIFIED

INCOMPLETE · UNVERIFIED

FAILED · VERIFIED
FAILED · UNVERIFIED

ABORTED · UNVERIFIED
```

### Meaning of COMPLETED

`COMPLETED` means:

> execution terminated normally according to the active execution policy.

It does not automatically mean:

> every semantic property of the artifact has been proven.

---

# 31. The Three Semantic Separations

These must remain explicit:

```text
Mutation
   ↓
Artifact changed

Verification
   ↓
Evidence collected

Completion
   ↓
Execution policy reached a terminal completion condition
```

Therefore:

```text
Mutation.Success
≠
Execution.Completed
```

```text
Execution.Completed
≠
Evidence.Verified
```

```text
State.Changed
≠
Objective.Progress
```

---

# 32. Failure Classification

Failure is classified before recovery.

The LLM may propose a failure class.

The Engine decides.

If classification is uncertain:

```text
UNKNOWN
```

is the safe result.

The system must never:

```text
UNKNOWN
  ↓
LLM guess
  ↓
automatic mutation
```

---

## 32.1 Failure Classes

```text
CODE_FAILURE
ENVIRONMENT_FAILURE
TEST_FAILURE
SCOPE_FAILURE
UNKNOWN
```

### CODE_FAILURE

Examples:

```text
syntax error
type error
compile error
local logic error
```

Typical route:

```text
REPAIRING
  ↓
bounded repair
  ↓
REVIEW
```

### ENVIRONMENT_FAILURE

Examples:

```text
missing service
broken container
network unavailable
dependency service unavailable
```

Typical route:

```text
INVESTIGATING
```

Application code must not be modified merely to compensate for environment failure.

### TEST_FAILURE

Examples:

```text
outdated assertion
invalid test blueprint
changed specification
missing coverage
```

Typical route:

```text
PLANNING
```

### SCOPE_FAILURE

Examples:

```text
unexpected file
scope expansion
unauthorized mutation
```

Typical route:

```text
STOP
→ ROLLBACK where safe
→ REPLAN
```

### UNKNOWN

Typical route:

```text
HUMAN_CONTROL
```

No automatic guessing.

---

# 33. Resource Model

Izen tracks resource use independently of correctness.

Relevant resources include:

```text
input tokens
cached input tokens
uncached input tokens
output tokens
requests
latency
execution time
shell commands
attempts
files
diff size
```

Telemetry exists to improve strategy.

Telemetry does not decide correctness.

---

# 34. Token Economics

Izen does not optimize for minimum raw token count.

The target is:

```text
minimum unnecessary model interaction
subject to
sufficient execution progress
and
required evidence
```

A large request may be appropriate when:

```text
context is necessary
provider cache is favorable
single-pass execution avoids repeated work
```

A small request may be appropriate when:

```text
target is narrow
context is sufficient
incremental execution is more efficient
```

Raw input tokens are therefore not equivalent to cost.

Raw output tokens are not equivalent to value.

Request count is not equivalent to waste.

---

# 35. Token Allocation Principle

Izen follows two complementary rules.

> **Never spend fewer tokens than the current execution unit requires merely to appear efficient.**

> **Never spend more tokens than the current execution unit can usefully consume merely because capacity is available.**

Therefore:

```text
UNDER-ALLOCATED
    ↓
Insufficient reasoning
    ↓
Failed or shallow execution

SUFFICIENT
    ↓
Minimum Viable Execution
    ↓
Useful progress

OVER-ALLOCATED
    ↓
Waste
```

---

# 36. Minimum Viable Execution

Minimum Viable Execution is not:

```text
smallest prompt
```

It is not:

```text
smallest output
```

It is not:

```text
single request
```

It means:

> **the minimum execution resources required to produce reliable useful progress for the current execution unit.**

This includes:

```text
context
output capacity
reasoning
verification
authority
time
requests
```

The runtime must adapt rather than blindly compress.

---

# 37. Provider Independence

The Runtime Core must not depend on:

```text
OpenRouter
Anthropic
OpenAI
Ollama
specific model names
specific free tiers
specific context sizes
specific cache implementations
```

Provider and model behavior are described through environment capability.

The same objective may execute as:

```text
ONE_SHOT
```

on one model and:

```text
INCREMENTAL
```

on another.

This is expected.

Provider substitution must not change authorization semantics.

---

# 38. Workspace Harness Discovery

Workspace capabilities are discovered lazily and cached.

The runtime may discover:

```text
parser
symbol graph
LSP
formatter
linter
compiler
type checker
build system
test runner
runtime validation
semantic search
git
filesystem features
```

Discovery must be:

```text
workspace-scoped
lazy
cached
invalidatable
```

It must not impose expensive probing on trivial execution.

The Core knows capabilities.

Capability providers know how those capabilities are obtained.

---

# 39. Cold-Start Workspaces

A workspace may contain only:

```text
one file
no project manifest
no tests
no compiler
no LSP
no configured tooling
```

Izen must still be capable of useful execution.

Verification degrades gracefully:

```text
deep verifier unavailable
        ↓
artifact integrity
        ↓
structural integrity when available
        ↓
honest evidence boundary
```

The system must not:

```text
No verifier
→ Verified
```

and must not:

```text
No verifier
→ all execution forbidden
```

unless the active execution policy explicitly requires stronger evidence.

---

# 40. Context Compilation

Context is a compiled resource.

The transcript is not the canonical execution state.

The Context Compiler consumes:

```text
Objective
Execution Unit
Current Artifact State
Dependencies
Plan
Previous Evidence
Model Environment
Budget
```

and produces the context required for the current unit.

Possible sources include:

```text
artifact state
symbol graph
dependency graph
AST / structural model
LSP information
semantic search
text retrieval
previous evidence
execution history
```

The compiler may choose:

```text
FULL
STRUCTURAL
REGIONAL
SYMBOL
DIFF
DELTA
SUMMARY
VERIFICATION
CONTINUATION
```

based on the current strategy.

---

# 41. Context Recompilation

When the execution state changes materially:

```text
artifact changed
dependency changed
scope changed
strategy changed
verification result changed
intent changed
```

the context may be recompiled.

The runtime must not assume that the previous prompt remains valid.

Likewise, context recompilation must not automatically imply that the entire conversation must be resent.

The canonical source is:

```text
objective
+
current artifacts
+
current dependencies
+
current execution state
```

not the raw transcript.

---

# 42. Model Interaction Contract

Every model interaction should have an explicit purpose.

Examples:

```text
intent interpretation
evidence analysis
plan generation
mutation proposal
continuation
failure classification
```

A model request without an execution purpose is a cost smell.

Every request should be explainable as:

```text
Why was this request necessary?
What context was required?
What was the expected output?
What execution value could it provide?
```

This is central to smart-harness behavior.

---

# 43. Approval Model

Reversibility and consent are different guarantees.

```text
CHECKPOINT
=
can undo

APPROVAL
=
human consent
```

Neither substitutes for the other.

Normal mutation:

```text
Human Approval
+
Checkpoint
+
Authorization
```

Micro-Plan:

```text
Budget
+
Scope
+
Checkpoint
+
Pre-Approval
```

No silent authorization.

No inferred consent.

---

# 44. Authorization Formula

Mutation authorization conceptually requires:

```text
ValidIntent
AND
ValidScope
AND
ValidPlanOrMicroPlan
AND
CheckpointCreated
AND
SourceHashMatch
AND
BudgetAvailable
AND
CapabilityGranted
AND
(
    HumanApproved
    OR
    BudgetIsPreApproval
)
```

The authorization decision belongs to the Capability Guard / Control Plane authorization path.

It must not be duplicated as an independent mutation authority elsewhere.

---

# 45. Verification and Review

Review operates on:

```text
Patch
  ↓
Verification Capabilities
  ↓
Evidence
  ↓
Failure Classification
  ↓
Completion / Recovery Decision
```

Review does not automatically imply repair.

Repair is another controlled execution decision.

---

# 46. Execution Efficiency

Izen measures efficiency by relationships such as:

```text
objective progress / model interaction
objective progress / uncached input
objective progress / output token
objective progress / request
objective progress / latency
objective progress / cost
```

These are telemetry metrics.

They are not correctness invariants.

Because objective progress is a vector rather than a universal scalar, metrics should be reported by signal where appropriate.

Examples:

```text
constraints resolved / request
evidence gained / request
verification level increased / request
dependencies resolved / request
```

No fake percentage should be created solely to make progress look measurable.

---

# 47. Instrumentation

Instrumentation is part of the architecture.

Izen should record:

```text
intent classification
execution class
strategy selected
context representation
context size
output allocation
actual token usage
cached input usage
uncached input usage
provider requests
latency
execution attempts
state fingerprints
verification evidence
continuation decisions
replans
rollback events
failure classifications
approval friction
```

Two classes of metrics must remain visible.

## Failure Classification Accuracy

Measure:

```text
correct classifications
incorrect classifications
UNKNOWN frequency
recovery correctness
```

## Approval Friction

Measure:

```text
plan approval frequency
scope invalidations
approval rejection
replan frequency
$hot fallback frequency
manual shell escape frequency
```

Telemetry exists to improve the architecture rather than quietly weakening its guarantees.

---

# 48. Presentation Contract

The TUI is an operational dashboard, not a transcript.

Fixed regions:

```text
workflow state
active artifact
artifact lifecycle
capability snapshot
budget remaining
execution outcome
evidence state
```

Scrollable regions:

```text
reasoning
dialogue
analysis
active model output
logs
```

The presentation layer subscribes to Control Plane state.

It must not maintain an independent semantic copy of runtime truth.

One state source drives:

```text
Runtime
TUI
Telemetry
Audit
```

---

# 49. Storage Model

Global storage:

```text
~/.izen/
```

contains only global infrastructure:

```text
binaries
shared configuration
global reusable infrastructure
cross-project cache
```

Project-specific provenance belongs to:

```text
./.izen/
```

including:

```text
sessions
artifact registry
graph indexes
patches
checkpoints
history
audit
```

Archived artifacts remain under:

```text
./.izen/history/<artifact_id>
```

Nothing carrying project lineage may be written to global storage merely for convenience.

---

# 50. External Workspace Changes

The workspace is not controlled exclusively by Izen.

Before consuming any artifact:

```text
Artifact Dependency Snapshot
        ↓
Current Workspace
        │
        ├── VALID
        │     ↓
        │   proceed
        │
        └── STALE
              ↓
          revalidate
              or
            replan
```

This applies equally to changes caused by:

```text
user
external tools
other processes
git
CI
formatters
generators
```

---

# 51. Failure and Resource Exhaustion

Resource exhaustion must be classified by type.

### Model Output Exhaustion

```text
OUTPUT_EXHAUSTED
```

may lead to:

```text
CONTINUE
REPLAN
STOP
```

depending on:

```text
remaining budget
progress
state
evidence
attempts
```

### Mutation Boundary Exhaustion

Examples:

```text
max files
max diff lines
scope exceeded
```

requires:

```text
STOP
ROLLBACK where appropriate
REPLAN or HUMAN_CONTROL
```

### Provider Quota Exhaustion

Examples:

```text
request quota
TPM
RPM
provider account limit
```

requires:

```text
STOP
or
strategy downgrade/alternative provider
```

according to available policy and authorization.

A provider limitation is never silently converted into objective completion.

---

# 52. Anti-Patterns

## Transcript Dumping

Sending all available context by default.

## Destructive Context Truncation

Removing information required for correct reasoning merely to hit a token budget.

## Token-First Planning

Selecting a strategy solely because it is cheap.

## Changed-as-Done

Treating artifact mutation as proof of objective completion.

## Diff-as-Progress

Treating patch size as semantic progress.

## Model-as-Authority

Allowing the model to choose its own capabilities, scope, budget, or terminal state.

## Silent Scope Expansion

Continuing after the original scope changed.

## State Explosion

Encoding execution sub-states into the workflow state enum.

Bad:

```text
BUILDING_OUTPUT_EXHAUSTED
BUILDING_RETRYING
BUILDING_REPLANNING
```

## Language-Specific Core Logic

Embedding HTML, Go, Rust, Python, or other domain rules into substrate core.

## Provider-Specific Core Logic

Allowing a provider's behavior to define Runtime semantics.

## Cache Obsession

Preserving a cache prefix at the cost of necessary context.

## Unlimited Repair

Continuing indefinitely because another model request is possible.

## Verification Assumption

Treating unavailable evidence as positive evidence.

## Fake Progress

Inventing a scalar percentage merely to make iterative execution appear measurable.

---

# 53. Core Invariants

These are non-negotiable.

### 1. No Mutation Without Authorization

```text
No Authorization = No Mutation
```

### 2. No Mutation Without Scope

```text
No Valid Scope = No Mutation
```

### 3. No Mutation Against Stale Dependencies

```text
Stale Artifact = Revalidate or Replan
```

### 4. No Silent Scope Expansion

```text
Scope Change = Invalidate + Replan
```

### 5. Unknown Failure Stops Automatic Reasoning-to-Mutation

```text
UNKNOWN = HUMAN_CONTROL
```

### 6. Repair Is Bounded

```text
Repair = Bounded Attempts
```

### 7. Authorization Is Not Approval

```text
Authorization ≠ Approval
```

except where the governing Micro-Plan explicitly constitutes pre-approval.

### 8. Mutation Is Not Completion

```text
Mutation.Success ≠ Execution.Completed
```

### 9. Resource Exhaustion Is Not Completion

```text
Output.Exhausted ≠ Objective.Completed
```

### 10. State Change Is Not Progress

```text
Artifact.Changed ≠ Objective.Progress
```

### 11. Missing Evidence Is Not Positive Evidence

```text
No Verifier ≠ Verified
```

### 12. Model Constraints Are Not Task Constraints

```text
Model Limitation ≠ Task Limitation
```

The runtime must adapt execution strategy before declaring a task impossible.

### 13. Required Context Must Not Be Destroyed for Token Savings

```text
Token Saving ≠ Permission to Remove Necessary Context
```

### 14. Cache Must Not Override Correctness

```text
Cache Hit ≠ Reason to Preserve Incorrect Context
```

### 15. No Second Mutation Authority

All mutation and termination decisions must pass through the authoritative Control Plane path.

### 16. User Intent Can Always Change

```text
New Intent
  ↓
Impact Analysis
  ↓
Invalidate Affected Artifacts
  ↓
Replan
  ↓
Re-approve where required
```

### 17. Execution Observation Does Not Authorize Execution

```text
Observation ≠ Authorization
```

### 18. Progress Does Not Imply Completion

```text
Progress ≠ Completion
```

### 19. Evidence State Does Not Redefine Workflow State

```text
Evidence ≠ Workflow State
```

### 20. Efficiency Never Overrides Correctness

```text
Efficiency is subordinate to
authority, integrity, and objective validity.
```

---

# 54. Layer Ownership

```text
HUMAN
  ↓
Intent
Approval
Correction
Final control

LLM
  ↓
Reasoning
Analysis
Proposal
Classification

CONTROL PLANE
  ↓
Workflow
Artifacts
Lineage
Dependencies
Capabilities
Authorization
Budgets
Execution Strategy
Execution Controller
Evidence interpretation
Terminal decisions

EXECUTION PLANE
  ↓
Read
Write
Execute
Test
Patch
Checkpoint
Rollback

CAPABILITY PROVIDERS
  ↓
Discovery
Context sources
Verification
Diagnostics
Tool access
Provider telemetry

PRESENTATION
  ↓
Render authoritative state
```

No layer silently assumes another layer's authority.

---

# 55. Architectural Definition of the Smart Harness

A conventional agent often behaves like:

```text
prompt
  ↓
model
  ↓
tool
  ↓
prompt
  ↓
model
```

Izen behaves like:

```text
objective
  ↓
environment observation
  ↓
execution strategy
  ↓
context compilation
  ↓
proposal
  ↓
authorization
  ↓
execution
  ↓
verification
  ↓
observation
  ↓
progress assessment
  ↓
continue / replan / stop
```

This is why Izen is a **Smart Harness** rather than merely an LLM wrapper.

The intelligence of the harness lies in deciding:

```text
what to retrieve
what to omit
what to execute
what not to execute
how much context is sufficient
how much output is sufficient
when to iterate
when to reuse
when to invalidate
when to verify
when to stop
when to ask the human
```

---

# 56. Language-Agnostic Execution

The Core must not understand application semantics through hardcoded language branches.

It understands:

```text
objective
scope
dependencies
capabilities
execution units
resources
evidence
verification
state
authority
```

Language-specific knowledge is supplied through capabilities.

Examples:

```text
Go
Rust
Python
TypeScript
JavaScript
HTML
CSS
SQL
Shell
JSON
YAML
unknown text
binary-aware workflows
```

The same Execution Substrate remains valid.

---

# 57. Model-Agnostic Execution

Izen must be able to operate with:

```text
local models
remote models
large models
small models
free-tier models
paid models
reasoning models
non-reasoning models
```

A model's limitations affect:

```text
Execution Environment
```

not:

```text
Execution Semantics
```

The Runtime adapts.

The architecture does not fork.

---

# 58. Practical Execution Examples

### Trivial Reasoning

```text
$prompt hi
```

```text
cost ≈ minimal
context = none
retrieval = none
execution = direct response
```

### Repository Question

```text
$prompt explain authentication
```

```text
cost = proportional
retrieval = structural → targeted
context = compiled
execution = analysis
```

### Small Mutation

```text
$hot fix this local issue
```

```text
bounded Micro-Plan
→ capability validation
→ checkpoint
→ mutation
→ review
→ bounded continuation if policy allows
```

### Large Refactor

```text
/build refactor authentication architecture
```

```text
Intent
→ Evidence
→ Plan
→ Approval
→ Strategy
→ Execution Frames
→ Mutation
→ Review
→ Replan / Repair / Complete
```

### Constrained Model

```text
max_output = 1024
```

does not mean:

```text
task impossible
```

It may mean:

```text
smaller execution units
+ progressive context
+ bounded continuation
```

### Cold Workspace

```text
no compiler
no tests
no LSP
```

does not mean:

```text
pretend success
```

It means:

```text
perform available integrity checks
report evidence boundary honestly
```

---

# 59. Architectural Mental Model

The entire system can be reduced to:

```text
                    OBJECTIVE
                        ↓
               EXECUTION ENVIRONMENT
                        ↓
                 EXECUTION CLASS
                        ↓
                EXECUTION STRATEGY
                        ↓
                  EXECUTION UNIT
                        ↓
                 CONTEXT COMPILATION
                        ↓
                    LLM PROPOSAL
                        ↓
                  ENGINE VALIDATION
                        ↓
                CAPABILITY AUTHORITY
                        ↓
                 APPROVAL WHEN NEEDED
                        ↓
                    CHECKPOINT
                        ↓
                     EXECUTION
                        ↓
                    VERIFICATION
                        ↓
                  OBSERVATION
                        ↓
              CONTINUE / REPLAN / STOP
                        ↓
                  TERMINAL STATE
```

The terminal state is:

```text
ExecutionOutcome × EvidenceState
```

The artifact history remains immutable.

The workspace remains source-of-truth checked.

The Control Plane remains the authority.

The LLM remains a proposal generator.

---

# 60. Final Principles

Izen's power does not come from always sending more context.

It comes from knowing when more context is necessary.

Izen's efficiency does not come from always sending less.

It comes from refusing to spend what has no useful execution value.

Izen's adaptability does not come from allowing the model to control itself.

It comes from allowing the Runtime to observe the environment and change execution strategy while preserving authority.

Izen's safety does not come from stopping everything.

It comes from distinguishing:

```text
safe to continue
safe to replan
safe to verify
safe to stop
safe to ask the human
```

Izen therefore follows:

> **Use the smallest sufficient context.**

> **Use the smallest sufficient execution unit.**

> **Allocate sufficient output, not artificially small output.**

> **Reuse valid state.**

> **Invalidate stale state.**

> **Verify what can be verified.**

> **Represent uncertainty honestly.**

> **Iterate when the environment requires it.**

> **Stop when further execution is no longer justified.**

> **Never trade authority, correctness, or required context for superficial token savings.**

The essence of Izen is:

```text
Simple execution.
Deep reasoning.
Explicit authority.
Proportional resources.
Composable capabilities.
Immutable lineage.
Adaptive strategy.
Honest evidence.
Bounded execution.
Human control.
```

And the defining principle is:

> **Izen does not optimize the model interaction. Izen optimizes the execution required to achieve the objective.**

That is the contract of the Izen Agent Runtime and Smart Harness.

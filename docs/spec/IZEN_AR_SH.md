
# IZEN — Agent Runtime & Smart Harness

## Master Architecture & Implementation Specification

**Repository:** `github.com/PizenLabs/izen`

**Primary Objective:**

Transform IZEN from an LLM client / execution wrapper that primarily:

```text
prompt
→ intent
→ model call
→ result
→ log
→ stop
```

into a genuine:

```text
objective
→ authoritative state
→ discovery
→ adaptive computation
→ proposal
→ authorization
→ execution
→ observation
→ evidence
→ verification
→ repair / continuation
→ proof
→ commit
```

**Deterministic Agent Runtime & Smart Harness**

---

# 0. NON-NEGOTIABLE DESIGN PRINCIPLE

IZEN must preserve:

> **LLMs reason. The Engine decides. Capabilities execute. Humans remain in control.**

And:

> **Dynamic Path, Static Authority, Truthful State Transition.**

The LLM is an **untrusted computation provider**.

The Runtime is the **authoritative owner of state, authorization, execution boundaries, evidence, verification, and objective completion**.

The LLM must never become the implicit source of truth merely because it produced a plausible response.

---

# 1. MISSION

The purpose of this implementation is not to make IZEN:

* behave exactly like OMP;
* copy OpenCode;
* copy Claude Code;
* add an infinite autonomous agent loop;
* maximize token usage;
* maximize the number of tools;
* add provider-native tools merely to satisfy a provider;
* replace deterministic runtime authority with model intelligence.

The purpose is to make IZEN capable of **actually completing objectives in the real environment**.

The runtime must be able to:

1. understand an objective;
2. inspect the actual workspace;
3. establish what is known and unknown;
4. construct a dynamic execution graph;
5. request bounded computation from an LLM;
6. treat model output as an untrusted proposal;
7. authorize only valid operations;
8. execute through runtime-owned capabilities;
9. observe the resulting environment;
10. record authoritative evidence;
11. verify progress against the objective;
12. diagnose deterministic failures;
13. perform bounded repairs;
14. continue when evidence requires more work;
15. stop when the objective is proven;
16. stop safely when progress is impossible;
17. preserve execution state across interruption;
18. resume from runtime state rather than replaying the original prompt;
19. hot-swap models without losing runtime state;
20. maintain truthful terminal states.

The objective is the unit of completion.

**A model response is not.**

---

# 2. EXISTING IZEN PHILOSOPHY MUST REMAIN INTACT

The implementation must preserve the following semantic separation:

```text
Intent
≠
Authorization
≠
Grant
≠
Execution
≠
Evidence
≠
Verification
≠
Objective Proof
```

Likewise:

```text
ProviderState
≠
ArtifactState
≠
MutationBoundaryState
≠
ObjectiveState
```

The existing Phase 14 distinction must not be collapsed.

For example:

```text
ProviderState = DONE
```

only means that the provider completed.

It does not mean:

```text
ArtifactState = PRODUCED
MutationBoundaryState = APPLIED
ObjectiveState = PROVEN
```

Only `ObjectiveState = PROVEN` establishes objective completion.

---

# 3. CURRENT ARCHITECTURAL PROBLEM

The current system can successfully perform portions of:

```text
UI
→ parser
→ intent
→ driver
→ executor
→ provider
→ result
```

but this is still fundamentally response-oriented.

The missing architectural loop is:

```text
environment
→ computation
→ mutation
→ observation
→ verification
→ adaptation
→ computation
```

Without this loop, IZEN can generate artifacts without reliably determining whether the requested objective has actually been achieved.

A successful model call therefore cannot be the terminal condition.

---

# 4. TARGET ARCHITECTURE

The target architecture is:

```text
                           HUMAN
                             │
                     Objective / Grant
                             │
                             ▼
                 ┌──────────────────────┐
                 │      CONTROL PLANE   │
                 │                      │
                 │ Objective            │
                 │ Intent               │
                 │ Authorization        │
                 │ Policy               │
                 │ Budget               │
                 │ Runtime State        │
                 │ Execution Graph      │
                 │ Scheduler            │
                 └──────────┬───────────┘
                            │
                      Compute Request
                            │
                            ▼
                 ┌──────────────────────┐
                 │         LLM          │
                 │                      │
                 │ Untrusted Compute    │
                 │ Reasoning            │
                 │ Proposal Generation  │
                 └──────────┬───────────┘
                            │
                         Proposal
                            │
                            ▼
                 ┌──────────────────────┐
                 │    CONTROL PLANE     │
                 │                      │
                 │ Validate             │
                 │ Admit                │
                 │ Authorize            │
                 └──────────┬───────────┘
                            │
                            ▼
                 ┌──────────────────────┐
                 │   EXECUTION PLANE    │
                 │                      │
                 │ Filesystem           │
                 │ Commands             │
                 │ Tests                │
                 │ Browser              │
                 │ Git                  │
                 │ Other Capabilities   │
                 └──────────┬───────────┘
                            │
                        Observation
                            │
                            ▼
                 ┌──────────────────────┐
                 │ EVIDENCE / VERIFY    │
                 │                      │
                 │ Evidence             │
                 │ Stabilization        │
                 │ Verification         │
                 │ Progress             │
                 │ Objective Proof      │
                 └──────────┬───────────┘
                            │
                         State'
                            │
                            ▼
                 ┌──────────────────────┐
                 │ AUTHORITATIVE LEDGER │
                 │                      │
                 │ Journal              │
                 │ Checkpoints          │
                 │ Recovery             │
                 │ Replay               │
                 └──────────┬───────────┘
                            │
                            └──────→ Scheduler
```

The runtime must own this loop.

The LLM must not own it.

---

# 5. CORE RUNTIME INVARIANT

Implement and preserve this invariant:

> **The LLM never owns runtime state. The LLM performs computation against an authoritative runtime snapshot.**

Therefore:

```text
Runtime State
      ↓
Compute Request
      ↓
LLM
      ↓
Untrusted Proposal
      ↓
Control Plane
      ↓
Execution
      ↓
Observation
      ↓
Evidence
      ↓
Verification
      ↓
Runtime State'
```

Never:

```text
LLM
 ↓
implicit state
 ↓
execution
```

Never allow raw model output to directly become:

* authorization;
* task completion;
* execution state;
* filesystem truth;
* verification truth;
* objective proof.

---

# 6. OBJECTIVE-FIRST EXECUTION

Introduce an explicit runtime concept of:

```text
Objective
```

An objective contains, conceptually:

```text
Goal
Invariants
Scope
Expected Outcome
Constraints
Authorization
Verification Requirements
```

The exact Go structure should follow the existing codebase conventions.

Do not introduce a parallel competing objective model if an authoritative equivalent already exists.

First inspect the repository and reuse existing semantics where possible.

---

# 7. OBJECTIVE IS NOT INTENT

Example:

```text
User:
$prompt please review this project and redesign my personal portfolio
```

Intent may be:

```text
BUILD / AUTONOMOUS_MUTATION
```

But the objective is:

```text
redesign the existing portfolio project
```

Intent determines what kind of operation the user requested.

Objective determines what must eventually be proven.

Do not collapse them.

---

# 8. CANONICAL RUNTIME STATE

Introduce or evolve a single authoritative runtime state.

Conceptually:

```go
type RuntimeState struct {
    Objective
    Invariants
    Workspace
    Execution
    Tasks
    Artifacts
    Evidence
    Verification
    Authorization
    Budget
    ActiveTask
    ActiveBuffer
    Checkpoint
}
```

The exact representation is implementation-dependent.

Critical rule:

> RuntimeState is runtime-owned.

The LLM may produce:

* summaries;
* interpretations;
* hypotheses;
* proposals;
* plans.

The LLM must not directly mutate authoritative RuntimeState.

---

# 9. STATE RECONSTRUCTION

Runtime state must be reconstructable from authoritative sources.

Conceptually:

```text
Execution Journal
+
Workspace
+
Git State
+
Artifacts
+
Evidence
+
Verification
+
Authorization
+
Execution Graph
```

must be sufficient to reconstruct the current runtime state.

The runtime must not depend on:

```text
conversation transcript
```

as its only source of truth.

The runtime must not require the original model to remember what happened.

---

# 10. AUTHORITATIVE EXECUTION LEDGER

Implement an execution ledger.

Do not treat it as a transcript database.

The ledger records **authoritative state transitions**.

Core principle:

> **Every authoritative runtime state transition must have a durable record.**

Not:

> Every token must be synchronously persisted.

---

# 11. EVENT CLASSES

At minimum support semantic events equivalent to:

```text
ExecutionStarted

ObjectiveResolved

ContextResolved

TaskCreated
TaskStarted
TaskCompleted
TaskBlocked

ModelInvocationStarted
ModelInvocationCompleted
ModelInvocationInterrupted
ModelInvocationFailed

ProposalProduced
ProposalRejected
ProposalAccepted

MutationStaged
MutationApplied
MutationRolledBack

CommandStarted
CommandCompleted

ObservationProduced
EvidenceProduced

VerificationStarted
VerificationPassed
VerificationFailed
VerificationInconclusive

RepairRequested

CheckpointCreated

ExecutionInterrupted
ExecutionResumed

ExecutionFailed
ExecutionCommitted
```

Use existing project terminology where available.

Do not duplicate existing event models unnecessarily.

---

# 12. STREAMING MUST NOT DEFINE STATE

Do not synchronously persist every token.

Instead:

```text
Provider Stream
      ↓
In-Memory Stream Buffer
      ↓
TUI / Telemetry
      ↓
Batch Persistence
```

Durable events describe meaningful execution transitions.

If the process dies during model generation:

```text
ModelInvocationStarted
```

may exist without:

```text
ModelInvocationCompleted
```

The runtime can reconstruct:

```text
Invocation = INTERRUPTED
```

without needing to reproduce every token.

This is sufficient for recovery.

---

# 13. JOURNAL STORAGE

The implementation may use:

* SQLite WAL;
* append-only event log;
* another durable storage backend.

Do not hardcode the storage implementation into runtime semantics.

The semantic contract is:

```text
append event
→ durable
→ ordered
→ replayable
→ integrity-preserving
```

Storage must support:

* monotonic sequence;
* execution/session identity;
* event type;
* schema version;
* timestamp;
* causal/task identity;
* payload;
* replay.

---

# 14. EVENT BOUNDARY

Events that change authoritative state must be durable before the runtime considers the transition committed.

For example:

```text
MutationApplied
```

must not exist only in memory.

Likewise:

```text
VerificationPassed
```

must be journaled before the runtime advances the objective.

This provides crash consistency.

---

# 15. RESUME SEMANTICS

`continue` must never mean:

```text
resend original prompt
```

and must never mean:

```text
reuse provider connection
```

It means:

```text
Journal
 ↓
Replay
 ↓
Reconstruct RuntimeState
 ↓
Inspect actual workspace
 ↓
Determine unfinished task
 ↓
Determine valid next transition
 ↓
Create new ComputeRequest
 ↓
Continue
```

Therefore:

> **Model failure must never equal Runtime state loss.**

And:

> **Process interruption must never silently destroy objective state.**

---

# 16. CHECKPOINT SEMANTICS

A checkpoint is a durable reconstruction point.

It should identify:

```text
execution sequence
runtime state version
workspace identity
workspace revision
active task
authorization boundary
budget state
relevant artifact state
```

Checkpointing must not depend on model memory.

---

# 17. CONTEXT REHYDRATION

The runtime must separate:

```text
Canonical Runtime State
```

from:

```text
Model-Facing Context
```

The latter is a materialized view.

Pipeline:

```text
Canonical State
      ↓
Relevant State Selection
      ↓
Context Compaction
      ↓
Model Capability Adaptation
      ↓
ComputeRequest
```

The model should receive enough context to perform the current computation.

It does not need the complete historical transcript.

---

# 18. CONTEXT COMPACTION

Do not blindly truncate old messages.

Preserve:

```text
Objective
Invariants
Authorization
Current Workspace Facts
Completed Tasks
Pending Tasks
Failed Tasks
Important Evidence
Verification Results
Relevant Decisions
Active Task
Active Buffer
```

Discard or compact information that is no longer relevant.

The authoritative journal remains the source of truth.

A compacted context is never the source of truth.

---

# 19. MODEL HOT-SWAP

Model identity must not be embedded into objective state.

A model can be replaced:

```text
Model A
→ interruption
→ Model B
→ continuation
```

without losing:

```text
Objective
Task Graph
Evidence
Verification
Authorization
Budget
Workspace State
```

The new model receives a fresh ComputeRequest derived from RuntimeState.

---

# 20. COMPUTE REQUEST

Introduce an explicit runtime-level computation request.

Conceptually:

```go
type ComputeRequest struct {
    Objective
    Task
    RelevantState
    Evidence
    Constraints
    Budget
    ModelCapabilities
}
```

The exact schema should follow the repository.

The model is asked to perform a bounded computation.

Examples:

```text
analyze workspace
diagnose failure
propose patch
generate artifact
interpret evidence
suggest next task
```

---

# 21. COMPUTE RESULT

Model output must be represented as an untrusted result.

Conceptually:

```go
type ComputeResult struct {
    Proposal
    Usage
    ProviderMetadata
    CompletionState
}
```

Do not allow provider metadata to become runtime truth.

For example:

```text
finish_reason = stop
```

does not mean:

```text
Objective = PROVEN
```

---

# 22. PROPOSAL BOUNDARY

Every model result enters a proposal boundary.

Possible proposal types:

```text
AnalysisProposal
PlanProposal
ArtifactProposal
PatchProposal
CommandProposal
RepairProposal
ContinuationProposal
VerificationInterpretation
```

The runtime validates:

```text
schema
scope
authorization
capability
workspace
budget
task relevance
```

before execution.

---

# 23. NO DIRECT MODEL-TO-FILESYSTEM AUTHORITY

Never implement:

```text
LLM output
→ write file
```

without an authoritative runtime transition.

Correct:

```text
LLM
 ↓
Proposal
 ↓
Validation
 ↓
Admission
 ↓
Authorization
 ↓
Capability
 ↓
Mutation
```

---

# 24. DYNAMIC EXECUTION GRAPH

The runtime needs an explicit execution graph.

Conceptually:

```text
Objective
    ↓
Task Graph
    ├── Discovery
    ├── Analysis
    ├── Mutation
    ├── Observation
    ├── Verification
    └── Repair
```

The graph is dynamic.

The runtime may add tasks when evidence requires them.

Do not hardcode a fixed workflow.

---

# 25. TASK STATE MACHINE

Tasks should have explicit state.

Conceptually:

```text
PENDING
→ READY
→ COMPUTING
→ PROPOSED
→ AUTHORIZED
→ EXECUTING
→ OBSERVING
→ VERIFYING
→ PROVEN
```

Failure:

```text
VERIFYING
→ FAILED
→ DIAGNOSING
→ REPAIRING
→ VERIFYING
```

Terminal alternatives:

```text
BLOCKED
HUMAN_REQUIRED
FAILED
```

Do not silently convert failed tasks into successful tasks.

---

# 26. ADAPTIVE EXECUTION LOOP

The runtime's central loop should become:

```text
TASK
 ↓
DISCOVER
 ↓
PLAN
 ↓
COMPUTE
 ↓
PROPOSE
 ↓
AUTHORIZE
 ↓
EXECUTE
 ↓
OBSERVE
 ↓
VERIFY
 ↓
 ├── PROVEN
 │      ↓
 │   NEXT TASK
 │
 └── FAILED
        ↓
      DIAGNOSE
        ↓
      REPAIR
        ↓
      COMPUTE
```

This is not an infinite autonomous loop.

Every iteration is bounded and journaled.

---

# 27. CONTINUATION MUST BE EVIDENCE-DRIVEN

Do not continue because:

```text
model said continue
```

Continue because:

```text
runtime evidence
```

shows that the objective remains incomplete.

Examples:

```text
artifact exists
but browser verification missing
→ create verification task
```

```text
browser renders
but console error exists
→ create diagnosis task
```

```text
test fails
→ create repair task
```

```text
all invariants verified
→ ObjectiveState = PROVEN
```

---

# 28. EVIDENCE IS FIRST-CLASS

Introduce typed Evidence.

Examples:

```text
FilesystemEvidence
GitEvidence
CompilerEvidence
TestEvidence
LinterEvidence

BrowserScreenshotEvidence
BrowserDOMEvidence
BrowserConsoleEvidence
BrowserErrorEvidence
BrowserNetworkEvidence

ASTEvidence
ProcessEvidence
CommandEvidence
```

Evidence must originate from runtime-controlled capabilities.

The model can interpret evidence.

The model cannot fabricate evidence.

---

# 29. OBSERVATION PROVIDERS

Capabilities that observe the environment should produce typed observations.

Conceptually:

```text
Capability
 ↓
Observation
 ↓
Evidence
 ↓
Verification
```

Do not treat every observation as an opaque string.

Prefer structured runtime data.

---

# 30. BROWSER AS EVIDENCE PROVIDER

For browser-capable objectives, support capabilities such as:

```text
Open
Reload
Screenshot
DOM
Console
Errors
Network
Click
Type
Scroll
Viewport
```

These capabilities are governed by the same:

```text
Authorization
Budget
Journal
Evidence
Verification
```

as filesystem and command capabilities.

The browser is not a special autonomous authority.

---

# 31. VERIFICATION ENGINE

Verification must be objective-specific.

Examples:

### Code

```text
compile
+
tests
+
static analysis
```

### Web

```text
page loads
+
CSS loads
+
JS executes
+
console clean
+
required DOM exists
+
visual observation
+
interaction checks
```

### Bug Fix

```text
original failure reproduced
→ fix
→ failure no longer reproduced
+
regression test
```

### Documentation

```text
required sections
+
valid links
+
repository consistency
```

Do not use a universal:

```text
command succeeded = verified
```

rule.

---

# 32. VERIFICATION STABILIZATION

Verification must distinguish transient instability from deterministic defects.

States:

```text
OBSERVED
→ STABILIZING
→ VERIFIED
```

or:

```text
OBSERVED
→ STABILIZING
→ FAILED_DETERMINISTIC
```

or:

```text
OBSERVED
→ STABILIZING
→ INCONCLUSIVE
```

Examples of transient conditions:

```text
network delay
browser startup race
resource not ready
CSS not yet loaded
animation not settled
process startup
temporary timeout
```

The verifier may retry according to a bounded policy.

---

# 33. VERIFICATION RESULT

At minimum:

```text
PASSED
FAILED_DETERMINISTIC
FAILED_TRANSIENT
INCONCLUSIVE
BLOCKED
```

Important invariant:

> **Insufficient evidence must never automatically become a defect.**

`INCONCLUSIVE` may create:

```text
diagnosis
additional observation
human review
```

It must not immediately trigger an arbitrary repair.

---

# 34. FAILURE FINGERPRINT

Introduce failure identity.

Conceptually:

```go
type FailureFingerprint struct {
    Verifier
    ErrorClass
    Location
    Artifact
    Symbol
    Signature
}
```

The exact representation depends on the repository.

Purpose:

```text
failure A
→ repair
→ failure A
→ repair
→ failure A
```

must be detectable.

---

# 35. PROGRESS DETECTION

Do not limit repair loops only by:

```text
file patched > N times
```

Track runtime progress.

Conceptually:

```text
ProgressSnapshot {
    ObjectiveCoverage
    UnresolvedInvariants
    FailureFingerprints
    VerifiedArtifacts
    VerificationResults
    MutationCount
}
```

After each iteration:

```text
State[n]
→ State[n+1]
```

calculate whether meaningful progress occurred.

---

# 36. NO-PROGRESS POLICY

If repeated execution produces:

```text
same objective coverage
+
same unresolved invariant
+
same failure fingerprint
+
no meaningful evidence change
```

the runtime must classify:

```text
NO_PROGRESS
```

and stop or escalate.

Possible terminal states:

```text
HUMAN_REQUIRED
OBJECTIVE_BLOCKED
BUDGET_EXHAUSTED
```

Do not burn tokens indefinitely.

---

# 37. REPAIR LOOP

Repair must be:

```text
Evidence
 ↓
Failure Classification
 ↓
Diagnosis
 ↓
Repair Proposal
 ↓
Authorization
 ↓
Execution
 ↓
Observation
 ↓
Verification
```

Never:

```text
failure
→ ask LLM to "try again"
→ execute
```

---

# 38. REPAIR SAFETY

A repair task must have evidence supporting the existence of a defect.

Do not repair based solely on:

```text
model suspicion
```

unless the task is explicitly diagnostic and no mutation is authorized.

This prevents hallucinated repair.

---

# 39. DYNAMIC GRAPH BOUNDS

The execution graph must be bounded.

At minimum:

```text
MaxSteps
MaxTaskDepth
MaxModelCalls
MaxTotalTokens
MaxWallTime
MaxToolCalls
MaxMutationBytes
MaxRetries
MaxContext
MaxExternalEffects
```

These are runtime-owned.

The model cannot increase them.

---

# 40. PROGRESS + BUDGET + AUTHORIZATION

Termination must consider all three:

```text
Authorization
Budget
Progress
```

Possible outcomes:

```text
PROVEN
FAILED
BLOCKED
HUMAN_REQUIRED
BUDGET_EXHAUSTED
NO_PROGRESS
```

Do not classify every termination as “success” or “failure”.

---

# 41. TRANSACTIONAL EXECUTION

Filesystem mutations should support staging where possible.

Architecture:

```text
Workspace
 ↓
TransactionManager
 ↓
Staging Backend
 ↓
Mutation
 ↓
Verification
 ↓
Commit / Rollback
```

Do not assume all workspaces are Git repositories.

---

# 42. TRANSACTION BACKENDS

Possible implementations:

```text
GitWorktreeBackend
APFSCloneBackend
OverlayFSBackend
DirectWorkspaceBackend
NoTransactionBackend
```

The runtime must abstract over them.

Git Worktree is an implementation option, not the definition of transactional execution.

---

# 43. EFFECT CLASSIFICATION

Classify execution effects:

```text
REVERSIBLE
TRANSACTIONAL
IRREVERSIBLE
```

Examples:

```text
file mutation
→ reversible

Git mutation
→ reversible

temporary process
→ ephemeral

database migration
→ potentially irreversible

external HTTP mutation
→ potentially irreversible

deployment
→ external / potentially irreversible
```

The runtime must never claim rollback semantics it cannot guarantee.

---

# 44. GIT SEMANTICS

Git remains authoritative for repository workspace state where applicable.

But Git is not the complete runtime ledger.

Use:

```text
Git
=
workspace truth
```

and:

```text
Execution Journal
=
execution truth
```

and:

```text
Evidence
=
observation truth
```

and:

```text
Verification
=
objective truth
```

---

# 45. NO HEURISTIC TARGET FABRICATION

Do not implement mappings such as:

```text
HTML
→ index.html

CSS
→ styles.css

JS
→ script.js

redesign
→ design task
```

merely because they are likely.

Semantic classification may help identify intent.

It must not invent mutation targets.

---

# 46. WORKSPACE DISCOVERY

For broad objectives, first inspect the real environment.

Discovery should establish facts such as:

```text
project structure
framework
entry points
build system
existing assets
existing scripts
existing tests
runtime commands
Git state
relevant files
```

Only after sufficient discovery should mutation targets become concrete.

---

# 47. NO STATIC ARTIFACT TEMPLATES

Do not hardcode a portfolio implementation such as:

```text
index.html
styles.css
script.js
```

because the benchmark prompt resembles a known example.

The runtime must derive actual artifacts from the workspace.

A target that cannot be established must remain unknown.

Unknown must not be converted into fabricated certainty.

---

# 48. SYNTAX-AWARE CONTINUATION

AST/tree-sitter may provide evidence such as:

```text
incomplete function
syntax error
missing node
unfinished expression
```

But:

```text
ASTEvidence
≠
Authorization
```

Correct boundary:

```text
AST
 ↓
Evidence
 ↓
Task / Proposal
 ↓
Authorization
 ↓
Execution
```

---

# 49. CAPABILITY MODEL

Capabilities should be typed.

Examples:

```text
ReadWorkspace
ReadFile
SearchWorkspace

WriteFile
ApplyPatch
DeleteFile

RunCommand
RunTest
RunBuild

BrowserOpen
BrowserObserve
BrowserInteract

CreateCheckpoint
Rollback

NetworkAccess
ExternalMutation
```

Each capability should define:

```text
authorization
input schema
output schema
budget
evidence schema
failure semantics
transaction semantics
```

---

# 50. CONTROL PLANE VS EXECUTION PLANE

The Control Plane decides:

```text
what may happen
why it may happen
what task it belongs to
what budget applies
what authorization applies
what evidence is required
what verification is required
```

The Execution Plane performs:

```text
the authorized operation
```

Do not move policy decisions into capabilities.

Do not let capabilities reinterpret authorization.

---

# 51. AUTONOMY DRIVER

The existing `autonomy.Driver` must remain a scheduler/orchestration component.

Do not turn it into a monolithic:

```text
agent brain
```

It should coordinate:

```text
state
→ task
→ compute
→ proposal
→ authorization
→ execution
→ observation
→ verification
→ continuation
```

Semantic responsibilities should remain in dedicated runtime components.

---

# 52. RUNTIME EXECUTOR

The existing `execution.RuntimeExecutor` remains the authoritative execution boundary.

Do not bypass it.

All mutation-capable execution must continue through the canonical execution path.

Do not create a second hidden executor.

Do not duplicate execution authority.

---

# 53. SINGLE AUTHORITY

There must be one authoritative path for:

```text
authorization
execution
mutation
checkpoint
verification transition
objective transition
```

If multiple packages can independently perform these transitions, consolidate them.

---

# 54. `$prompt`

`$prompt` means:

> bounded adaptive execution authority.

It does not mean:

```text
unlimited autonomy
```

The runtime may dynamically:

```text
discover
plan
mutate
test
observe
repair
retest
```

inside the authorized envelope.

Runtime controls:

```text
capability
workspace
budget
mutation
external effects
verification
```

---

# 55. `$hot`

`$hot` remains:

> declared narrower execution with budget-as-pre-approval.

It must use the same runtime substrate.

Only policy/authorization differs.

Do not create a second agent architecture for `$hot`.

---

# 56. `/ask`

`/ask` remains read-only conversation.

It must not accidentally enter mutation-capable objective execution.

Do not regress the existing permission model.

---

# 57. `/plan`

`/plan` remains read-oriented planning.

A plan is not execution.

Model output must not mutate the workspace merely because it contains patches or commands.

---

# 58. STREAMING ARCHITECTURE

Streaming is presentation of computation.

It must not define runtime semantics.

Correct:

```text
Provider Stream
 ↓
Stream Event
 ↓
Runtime Buffer
 ↓
TUI
```

Provider:

```text
DONE
```

does not imply:

```text
TASK PROVEN
```

or:

```text
OBJECTIVE PROVEN
```

---

# 59. TOKEN ACCOUNTING

Track separately:

```text
input tokens
output tokens
reasoning tokens
context tokens
per-call tokens
per-task tokens
execution total
```

Do not treat provider context size as equivalent to newly generated computation.

Long-running agents may have:

```text
large accumulated context
+
small incremental outputs
```

This is valid.

The runtime should optimize for:

> **objective progress per bounded compute**

not maximum output per call.

---

# 60. ADAPTIVE COMPUTE

Do not force:

```text
one task = one model call
```

or:

```text
every task = fixed 4096 tokens
```

Compute should respond to uncertainty.

Example:

```text
simple inspection
→ small call

ambiguous diagnosis
→ larger call

repair
→ focused call

verification interpretation
→ small call

architecture problem
→ larger call
```

Budget remains runtime-owned.

---

# 61. OMP REFERENCE PRINCIPLE

OMP-style execution may be used as an empirical capability reference.

Do not copy OMP architecture.

Observed behavior such as:

```text
large accumulated context
+
many incremental calls
+
tool interactions
+
small follow-up computations
```

demonstrates that practical agent execution may require repeated incremental computation.

This is evidence for:

```text
iterative stateful execution
```

not a requirement to reproduce OMP's token count, tool model, UI, or autonomy model.

Do not optimize IZEN to:

```text
match OMP token usage
```

or:

```text
match OMP call count
```

Optimize for:

```text
objective completion
+
truthful state
+
bounded compute
+
evidence
+
verification
```

---

# 62. EFFICIENCY

Define efficiency as approximately:

```text
Objective Progress
------------------
Runtime Cost
```

where cost includes:

```text
tokens
latency
tool calls
mutations
retries
external effects
human interventions
```

A small computation that prevents a large incorrect mutation may be more efficient than a large model response.

---

# 63. FAILURE TAXONOMY

Do not collapse failures into:

```text
MODEL_FAILED
```

Distinguish at least:

```text
MODEL_FAILURE
TRANSPORT_FAILURE
CONTEXT_FAILURE
PARSER_FAILURE
PROPOSAL_FAILURE
AUTHORIZATION_FAILURE
EXECUTION_FAILURE
OBSERVATION_FAILURE
VERIFICATION_FAILURE
BUDGET_EXHAUSTED
NO_PROGRESS
OBJECTIVE_BLOCKED
HUMAN_REQUIRED
```

Use existing failure semantics where they already exist.

---

# 64. RECOVERY

Recovery must be:

```text
observe
→ classify
→ reconstruct state
→ determine next valid transition
→ compute
→ propose
→ authorize
→ execute
→ verify
```

Do not use generic:

```text
retry same prompt
```

as the recovery mechanism.

---

# 65. HUMAN ESCALATION

The runtime must explicitly support:

```text
HUMAN_REQUIRED
```

when:

```text
authorization boundary exceeded
irreversible effect requires approval
verification is inconclusive
no progress
budget insufficient
objective ambiguous
recovery exhausted
```

Human escalation is not a runtime failure.

It is a valid control-plane state.

---

# 66. TUI REQUIREMENT

Do not redesign the UI first.

The UI must become a projection of authoritative runtime state.

Instead of only:

```text
MODEL STREAMING
4000 TOKENS
SUCCESS
```

the runtime should expose information such as:

```text
OBJECTIVE
Redesign portfolio

TASK
Verify rendered homepage

STATE
OBSERVING

EVIDENCE
✓ page loaded
✓ CSS loaded
✓ console clean
! visual verification pending

NEXT
capture observation
```

The exact UI may evolve later.

The semantic state must exist first.

---

# 67. UI MUST NEVER INVENT STATE

The TUI must not infer:

```text
success
progress
completion
verification
```

from:

```text
provider completion
```

All status indicators must be derived from runtime state/events.

---

# 68. ACCEPTANCE BENCHMARK

Use a real project benchmark such as:

```text
$prompt

Please review this project and redesign a professional personal
portfolio page for me using HTML, CSS, and JS.
The author's name is TomHunter, an AI Engineer.
```

The benchmark must be evaluated as an objective execution problem.

Do not prescribe:

```text
index.html
styles.css
script.js
```

Do not prescribe a particular model.

Do not prescribe a particular number of calls.

Do not prescribe a particular token count.

---

# 69. EXPECTED BENCHMARK BEHAVIOR

The runtime should demonstrate some equivalent sequence of:

```text
OBJECTIVE_CREATED

WORKSPACE_DISCOVERY

PROJECT_STRUCTURE_ESTABLISHED

TASK_CREATED
inspect existing application

COMPUTE

TASK_CREATED
design / implementation proposal

PROPOSAL_CREATED

AUTHORIZATION

MUTATION_STAGED

MUTATION_APPLIED

APPLICATION_STARTED

BROWSER_OBSERVATION

EVIDENCE_PRODUCED

VERIFICATION

FAILED / PASSED

if failed:

DIAGNOSIS

REPAIR_PROPOSAL

AUTHORIZATION

REPAIR_EXECUTION

RE-OBSERVATION

RE-VERIFICATION

OBJECTIVE_PROVEN
```

The exact path may differ.

That is intentional.

---

# 70. ACCEPTANCE INVARIANT A1 — OBJECTIVE TRUTH

A successful execution must terminate with:

```text
ObjectiveState = PROVEN
```

or an explicit non-success state:

```text
FAILED
BLOCKED
HUMAN_REQUIRED
BUDGET_EXHAUSTED
NO_PROGRESS
```

Never report successful completion merely because the provider stopped.

---

# 71. ACCEPTANCE INVARIANT A2 — CRASH RECOVERY

Interrupt execution during:

```text
model computation
mutation
command
verification
```

Restart IZEN.

Runtime must reconstruct the execution state.

It must not silently start a new unrelated objective.

---

# 72. ACCEPTANCE INVARIANT A3 — MODEL HOT SWAP

Start execution with Model A.

Interrupt.

Switch to Model B.

Continue.

The following must remain intact:

```text
Objective
Task Graph
Evidence
Verification
Authorization
Workspace State
Budget
```

---

# 73. ACCEPTANCE INVARIANT A4 — EVIDENCE

Model assertion alone must never establish objective proof.

There must be runtime evidence appropriate to the objective.

---

# 74. ACCEPTANCE INVARIANT A5 — VERIFICATION

A mutation cannot imply successful objective completion.

Verification must establish the relevant invariant.

---

# 75. ACCEPTANCE INVARIANT A6 — REPAIR

Introduce a deterministic failure.

Expected:

```text
failure
→ evidence
→ diagnosis
→ repair
→ verification
```

not:

```text
failure
→ final response
```

---

# 76. ACCEPTANCE INVARIANT A7 — TRANSIENT FAILURE

Introduce a transient browser/network/process condition.

Expected:

```text
observation
→ stabilization/retry
→ successful observation
```

without unnecessary repair.

---

# 77. ACCEPTANCE INVARIANT A8 — INCONCLUSIVE

Create a situation where evidence is insufficient.

Expected:

```text
INCONCLUSIVE
```

not:

```text
FAILED
```

and not:

```text
repair
```

unless additional evidence establishes a defect.

---

# 78. ACCEPTANCE INVARIANT A9 — NO PROGRESS

Create a deterministic repair loop where repeated attempts produce the same failure fingerprint.

Expected:

```text
NO_PROGRESS
→ HUMAN_REQUIRED
```

or:

```text
OBJECTIVE_BLOCKED
```

within the configured bounds.

---

# 79. ACCEPTANCE INVARIANT A10 — AUTHORIZATION

Force the model to propose an operation outside the current authorization boundary.

Expected:

```text
proposal rejected
```

The model must not be able to expand its own authority.

---

# 80. ACCEPTANCE INVARIANT A11 — TARGET DISCOVERY

Use a project whose structure differs from the benchmark example.

Expected:

```text
runtime discovers actual structure
```

It must not fabricate:

```text
index.html
styles.css
script.js
```

targets.

---

# 81. ACCEPTANCE INVARIANT A12 — TRUTHFUL TERMINAL STATE

If:

```text
ProviderState = DONE
ArtifactState = PRODUCED
MutationBoundaryState = APPLIED
VerificationState = FAILED
```

then:

```text
ObjectiveState != PROVEN
```

This invariant is mandatory.

---

# 82. ACCEPTANCE INVARIANT A13 — JOURNAL REPLAY

Given a journal from an interrupted execution:

```text
journal
→ replay
→ reconstructed state
```

must produce equivalent authoritative state.

Replay must not invoke the LLM.

---

# 83. ACCEPTANCE INVARIANT A14 — NO HIDDEN EXECUTOR

All mutations must remain inside the canonical RuntimeExecutor / execution authority path.

No capability may silently bypass authorization.

No secondary executor may be introduced.

---

# 84. ACCEPTANCE INVARIANT A15 — BUDGET

Exhaust:

```text
MaxTokens
MaxCalls
MaxWallTime
MaxToolCalls
```

The runtime must terminate predictably and preserve state.

---

# 85. IMPLEMENTATION ORDER

Do not perform a large uncontrolled rewrite.

Implement in vertical slices.

## Slice 1 — Canonical Runtime State

Establish:

```text
Objective
Task
Execution
Evidence
Verification
Budget
```

without changing existing behavior unnecessarily.

Acceptance:

```text
state transitions are explicit
```

---

## Slice 2 — Authoritative Execution Ledger

Implement:

```text
event
append
sequence
replay
checkpoint
```

Acceptance:

```text
journal replay reproduces state
```

---

## Slice 3 — Execution Graph

Implement:

```text
Objective
→ Task Graph
→ Task State
```

Acceptance:

```text
multiple tasks can execute sequentially
```

---

## Slice 4 — Compute Boundary

Introduce:

```text
ComputeRequest
→ LLM
→ Proposal
```

Acceptance:

```text
model output remains untrusted
```

---

## Slice 5 — Evidence

Introduce:

```text
Observation
→ Evidence
```

Acceptance:

```text
model cannot fabricate evidence
```

---

## Slice 6 — Verification

Implement:

```text
Evidence
→ Stabilization
→ Verification
```

Acceptance:

```text
PASS / FAILED_DETERMINISTIC / INCONCLUSIVE
```

---

## Slice 7 — Adaptive Continuation

Implement:

```text
verification
→ next task
```

Acceptance:

```text
runtime continues only when state requires it
```

---

## Slice 8 — Repair

Implement:

```text
failure
→ diagnosis
→ repair
→ verification
```

Acceptance:

```text
controlled deterministic failure can recover
```

---

## Slice 9 — Progress Protection

Implement:

```text
FailureFingerprint
ProgressSnapshot
NoProgressDetection
```

Acceptance:

```text
fix-break loops terminate
```

---

## Slice 10 — Transaction Boundary

Implement:

```text
TransactionManager
```

with backend abstraction.

Acceptance:

```text
failed mutation can be discarded where rollback is supported
```

---

## Slice 11 — Browser Evidence

Add browser observation capabilities.

Acceptance:

```text
web objective can be verified against runtime evidence
```

---

## Slice 12 — Resume / Hot Swap

Verify:

```text
interrupt
→ reconstruct
→ change model
→ continue
```

---

## Slice 13 — `$prompt` Integration

Only after the runtime substrate works.

`$prompt` becomes:

```text
bounded adaptive execution
```

rather than a separate autonomous implementation.

---

## Slice 14 — TUI Projection

Finally expose:

```text
objective
task
state
evidence
verification
progress
next transition
```

in the UI.

---

# 86. DO NOT MODIFY THESE SEMANTICS WITHOUT PROOF

Before changing existing architecture, inspect and preserve:

```text
authorization
mode policy
capability boundaries
RuntimeExecutor authority
Phase 14 state distinctions
ContextSpec / ExecutionSpec separation
checkpoint semantics
failure classification
existing `$prompt` / `$hot` boundaries
```

Do not remove an existing invariant simply because the new architecture seems cleaner.

If an existing abstraction conflicts with the target architecture:

1. identify the conflict;
2. document it;
3. determine migration strategy;
4. add tests;
5. migrate;
6. remove the obsolete path only after equivalent authority is proven.

---

# 87. CODEBASE AUDIT BEFORE IMPLEMENTATION

Before modifying code, inspect the real repository.

Trace the actual call graph from:

```text
UI input
→ parser
→ intent dispatch
→ autonomy
→ planner
→ context
→ RuntimeExecutor
→ provider
→ mutation
→ result
→ TUI
```

Identify:

```text
duplicate executors
duplicate planners
hidden mutation paths
implicit state
provider-owned state
heuristic target mapping
terminal-state inference
streaming state coupling
```

Do not rely on package names or documentation alone.

Follow actual call paths.

---

# 88. MIGRATION RULE

Do not implement a second runtime beside the existing runtime indefinitely.

Prefer:

```text
existing authority
→ evolve
→ migrate
→ remove obsolete path
```

rather than:

```text
old executor
+
new executor
+
adapter
+
special autonomous executor
```

This project has previously suffered from duplicate execution paths.

Avoid repeating that failure mode.

---

# 89. TEST STRATEGY

Tests must exist at multiple levels.

### Unit

Test:

```text
state transitions
event serialization
replay
fingerprints
progress
verification classification
budget
authorization
```

### Integration

Test:

```text
model
→ proposal
→ executor
→ evidence
→ verification
```

### Crash Recovery

Test process interruption at different boundaries.

### Acceptance

Run real objective-oriented projects.

### Race

Run:

```text
go test -race ./...
```

where applicable.

### Determinism

The runtime state machine must behave deterministically given identical authoritative inputs and events, even when model computation is non-deterministic.

---

# 90. DETERMINISM DEFINITION

IZEN does not need deterministic LLM output.

It needs deterministic authority.

Therefore:

```text
LLM output
= nondeterministic input
```

but:

```text
authorization
+
state transition
+
capability enforcement
+
verification semantics
=
runtime-controlled
```

The path may change.

The authority rules may not.

---

# 91. WHAT “SMART” MEANS

Do not measure Smart Harness quality by:

```text
number of tools
number of tokens
number of model calls
amount of autonomy
```

A Smart Harness is one that can determine:

```text
what is known
what is unknown
what should happen next
what is authorized
what actually happened
what evidence exists
whether progress occurred
whether the objective is proven
when to continue
when to stop
when to ask the human
```

---

# 92. WHAT “AGENT RUNTIME” MEANS

IZEN qualifies as an Agent Runtime when:

```text
Objective
 ↓
Runtime State
 ↓
Task
 ↓
Computation
 ↓
Proposal
 ↓
Authorization
 ↓
Execution
 ↓
Evidence
 ↓
Verification
 ↓
State Transition
 ↓
Continuation
```

is a first-class runtime mechanism rather than an accidental consequence of prompt engineering.

---

# 93. FINAL ARCHITECTURAL TEST

At the end of implementation, ask:

### Can the model be replaced?

Yes.

### Can the model fail?

Yes.

### Can the model produce a bad proposal?

Yes.

### Can the runtime recover?

Yes.

### Can the runtime resume without model memory?

Yes.

### Can the runtime determine that a mutation did not solve the objective?

Yes.

### Can the runtime collect evidence independently of the model?

Yes.

### Can the runtime detect a repair loop?

Yes.

### Can the runtime stop because progress is impossible?

Yes.

### Can the runtime prove completion?

Yes.

### Can a model expand its own authority?

No.

### Can provider completion imply objective completion?

No.

If any answer contradicts these invariants, the implementation is incomplete.

---

# 94. FINAL DEFINITION OF DONE

IZEN is not considered transformed merely because it has:

* tools;
* browser support;
* streaming;
* checkpoints;
* multiple models;
* planning;
* an autonomous mode;
* task graphs;
* token accounting.

The transformation is complete only when IZEN can demonstrate:

```text
                    OBJECTIVE
                        │
                        ▼
               AUTHORITATIVE STATE
                        │
                        ▼
                    DISCOVERY
                        │
                        ▼
                   TASK GRAPH
                        │
                        ▼
                 BOUNDED COMPUTE
                        │
                        ▼
                 UNTRUSTED PROPOSAL
                        │
                        ▼
                  AUTHORIZATION
                        │
                        ▼
                    EXECUTION
                        │
                        ▼
                   OBSERVATION
                        │
                        ▼
                     EVIDENCE
                        │
                        ▼
                   VERIFICATION
                    /       \
                PASSED      FAILED
                  │            │
                  │         DIAGNOSE
                  │            │
                  │          REPAIR
                  │            │
                  │            └──────→ COMPUTE
                  │
                  ▼
              PROGRESS
                  │
          ┌───────┴────────┐
          │                │
       NEXT TASK         PROVEN
          │                │
          └───────→        ▼
                    OBJECTIVE COMPLETE
```

while preserving:

```text
Human Authority
+
Runtime Authority
+
Model Independence
+
Truthful State
+
Bounded Autonomy
+
Evidence
+
Verification
+
Recoverability
+
Reversibility where possible
```

---

# 95. FINAL PRINCIPLE

Do not build IZEN into:

> **a better autonomous prompt loop.**

Build it into:

> **a runtime that owns the lifecycle of an objective.**

The LLM should become increasingly capable at reasoning.

The Runtime should become increasingly capable at:

```text
state
authority
scheduling
execution
observation
verification
recovery
```

Capabilities should become increasingly rich.

But the authority hierarchy must remain:

```text
HUMAN
  ↓
CONTROL PLANE
  ↓
EXECUTION PLANE
  ↓
CAPABILITIES
```

with the LLM operating as:

```text
UNTRUSTED COMPUTE
```

inside that system.

The final invariant is:

> **Every computation must be reconstructible from authoritative state.**

And the final success condition is:

> **IZEN does not finish because the model finished. IZEN finishes because the Runtime has sufficient evidence to prove that the objective is complete.**

That is the transition from:

```text
LLM Client
```

to:

```text
Agent Runtime
```

and from:

```text
Shell Tool
```

to:

```text
Deterministic Smart Harness.
```

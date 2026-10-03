
# IZEN Execution Architecture

**Status:** Architectural Constitution
**Scope:** Core runtime, execution engine, capabilities, evidence, state, safety, scheduling, recovery
**Applies to:** Every workspace, command, model, provider, tool, artifact type, and execution workload

---

## 1. Purpose

IZEN is a general-purpose Agent Runtime and Smart Harness.

It is not a web-development agent, coding agent, chat wrapper, autonomous loop, or model-specific orchestration layer.

The runtime must remain domain-agnostic.

Web development, Go, Python, Rust, shell tasks, repository maintenance, investigation, testing, configuration changes, and other workloads are execution workloads that use the same runtime substrate.

The architecture must therefore optimize for:

* correctness
* safety
* efficiency
* transparency
* determinism where determinism is possible
* bounded adaptation where adaptation is necessary
* recoverability
* evidence-based state
* practical usefulness

The runtime must never become specialized around the current benchmark workload.

---

# 2. Core Principle

> **LLMs reason. The Engine decides. Capabilities execute. Humans remain in control.**

The LLM is an untrusted computational component.

The runtime is the authority.

Capabilities are controlled execution mechanisms.

Humans retain authority over consequential decisions.

---

# 3. Fundamental Principle

> **Dynamic Path, Static Authority, Truthful State Transition.**

The runtime may dynamically determine the next useful action.

The authorities governing that action must remain static and explicit.

Every state transition must represent something that actually happened.

The runtime must never claim:

* a capability was used when it was not;
* a file was inspected when it was not;
* a mutation occurred when it did not;
* verification succeeded when it was skipped;
* an objective was completed because a model said so;
* a task was proven merely because bytes changed.

---

# 4. Architectural Layers

IZEN has four conceptual layers:

```text
HUMAN
  ↓
LLM
  ↓
CONTROL PLANE
  ↓
EXECUTION PLANE
```

## 4.1 HUMAN

The human provides:

* intent
* constraints
* authorization
* decisions
* approvals where required
* corrections
* cancellation
* high-value judgment

Human authority must not be replaced by model confidence.

Human approval must not become a meaningless confirmation dialog for every trivial operation.

Approval is required where the operation has meaningful decision value, risk, irreversibility, security implications, external impact, or material resource consumption.

---

## 4.2 LLM

The LLM may:

* reason over supplied context;
* analyze observations;
* propose actions;
* generate artifacts;
* explain alternatives;
* identify uncertainty;
* suggest repair strategies.

The LLM may not:

* authorize itself;
* grant itself capabilities;
* redefine workspace policy;
* invent execution evidence;
* declare its own output verified;
* declare an objective proven;
* silently expand scope;
* mutate the workspace directly;
* bypass the runtime.

LLM output is always an untrusted proposal until validated by the runtime.

---

## 4.3 CONTROL PLANE

The Control Plane owns:

* intent classification;
* workspace policy;
* capability resolution;
* authorization;
* scope;
* context;
* execution specification;
* scheduling;
* budgets;
* dependency ordering;
* failure classification;
* continuation decisions;
* evidence interpretation;
* objective completion.

There must be one authoritative owner for each of these responsibilities.

---

## 4.4 EXECUTION PLANE

The Execution Plane owns:

* filesystem operations;
* process execution;
* tool invocation;
* patch application;
* mutation;
* checkpoints;
* rollback;
* verification;
* observable execution evidence.

The Execution Plane must never invent authority.

It executes only operations authorized by the Control Plane.

---

# 5. One Canonical Execution Path

Every real execution must use one canonical runtime path.

```text
Input
  ↓
Parse
  ↓
Route
  ↓
Classify
  ↓
Resolve Workspace Policy
  ↓
Resolve Capabilities
  ↓
Observe
  ↓
Compile Context
  ↓
Create ExecutionSpec
  ↓
Schedule
  ↓
Compute / Execute
  ↓
Validate Result
  ↓
Request Approval When Required
  ↓
Apply Mutation / Execute Action
  ↓
Verify
  ↓
Record Evidence
  ↓
Evaluate Objective
  ↓
Continue / Stop
```

No production feature may create a parallel execution path.

There must not be competing:

* classifiers;
* executors;
* mutation authorities;
* completion authorities;
* context pipelines;
* approval mechanisms;
* scheduling loops.

Legacy paths must either be removed or isolated outside the production runtime.

---

# 6. Single Authority Rule

Every architectural responsibility has exactly one authority.

| Responsibility          | Authority                      |
| ----------------------- | ------------------------------ |
| Intent classification   | Canonical classifier           |
| Workspace policy        | Policy Resolver                |
| Capability resolution   | Capability Resolver            |
| Authorization           | Authorization Boundary         |
| Context                 | Context Pipeline               |
| Execution specification | ExecutionSpec                  |
| Scheduling              | Runtime Scheduler              |
| Mutation                | Mutation Boundary              |
| Verification            | Verification subsystem         |
| Evidence                | Evidence subsystem             |
| Objective completion    | Objective Completion Authority |
| Runtime state           | Canonical State Ledger         |

A helper may calculate information.

A helper may not silently become a second authority.

---

# 7. Intent Is Not Authorization

The following concepts must remain distinct:

```text
Intent
≠
Authorization
≠
Capability
≠
Grant
≠
Execution
≠
Evidence
≠
Verification
≠
Completion
```

A request to modify a file does not itself authorize the modification.

A capability grant does not prove that the capability was used.

A successful mutation does not prove that the objective was satisfied.

A model response does not constitute evidence.

---

# 8. ExecutionSpec Is Authoritative

Before consequential execution, the runtime must establish an authoritative `ExecutionSpec`.

The ExecutionSpec must describe, as applicable:

* intent;
* workspace;
* scope;
* capabilities;
* authorization state;
* targets;
* constraints;
* budget;
* dependencies;
* required evidence;
* verification requirements;
* failure policy.

The LLM may propose changes to the plan.

It may not mutate the authoritative ExecutionSpec directly.

Any material change to scope, capability, target, budget, or risk must return through the Control Plane.

---

# 9. Evidence Before Action

> **If IZEN cannot establish what it is acting on, it must not act.**

The runtime must observe before executing operations that depend on observation.

Examples:

```text
discover → inspect → determine → act
```

not:

```text
guess → act → discover what happened
```

The runtime must not infer executable targets from weak heuristics.

Unknown or ambiguous targets must remain unknown or ambiguous until sufficient evidence exists.

No heuristic may silently transform:

```text
"HTML"
```

into:

```text
"index.html"
```

unless the workspace evidence establishes that relationship.

---

# 10. Anti-Hallucination Rule

IZEN must always distinguish:

```text
Known
Observed
Derived
Proposed
Executed
Verified
Unknown
```

The runtime must never promote:

```text
Proposed → Executed
```

or:

```text
Claimed → Verified
```

without evidence.

If the runtime does not know something, the state must represent that uncertainty.

Unknown is a valid state.

Refusal is a valid state.

Incomplete execution is a valid state.

Stopping is preferable to fabricating certainty.

---

# 11. Minimum Sufficient Context

IZEN must provide the LLM with enough context to perform the current computation correctly.

It must not provide less than necessary.

It must not provide irrelevant information merely because it is available.

The objective is:

> **Enough context to make the next correct decision, and no unnecessary context.**

Context must be:

* bounded;
* attributable;
* relevant;
* reproducible where practical;
* scoped to the current ExecutionSpec.

Token count is telemetry.

Token count is not evidence of context quality.

---

# 12. Capability Reality Rule

A capability is considered real only when it:

1. is reachable through the canonical runtime path;
2. is authorized by policy;
3. executes against the real workspace or target;
4. produces observable evidence;
5. correctly reports its result.

An interface, abstraction, method, or dormant implementation does not count as a capability merely because it exists in the repository.

---

# 13. Mutation Boundary

All mutations must cross one authoritative Mutation Boundary.

No component may:

* write directly around the mutation subsystem;
* apply a patch outside the MutationSet;
* mutate using model output directly;
* reuse stale artifacts after computation failure;
* infer authorization from successful computation.

A valid mutation requires:

```text
Authorized ExecutionSpec
        +
Valid MutationCandidate
        +
Mutation Boundary
        +
Mutation Evidence
```

---

# 14. Failed Computation Must Never Become Mutation

This is a hard invariant.

```text
COMPUTING
   ↓
FAILED / EXHAUSTED
```

must not transition into:

```text
MUTATION_PENDING
```

or:

```text
APPLYING
```

unless a new valid computation explicitly produces a valid mutation candidate.

Previously generated partial, stale, or abandoned output must not remain executable after the computation that produced it has failed.

In particular:

```text
LLM output exhausted
+
no new artifact bytes
=
NO MUTATION
```

Human approval does not repair a failed computation.

Authorization authorizes an operation.

It does not validate the result of that operation.

---

# 15. Artifact Validity

Artifact validity has multiple independent dimensions:

```text
Syntax
Shape
Identity
Target Binding
Task Fitness
Cross-Artifact Coherence
Verification
```

Syntactically valid output is not necessarily task-valid output.

A valid HTML document is not automatically a valid portfolio implementation.

A compiling source file is not automatically a correct bug fix.

A changed configuration file is not automatically a successful migration.

The runtime must avoid confusing structural validity with objective completion.

---

# 16. Objective Completion

Mutation is not completion.

The following are distinct:

```text
ProviderState
ArtifactState
MutationState
VerificationState
ObjectiveState
```

Only objective-specific evidence may establish objective completion.

Example:

```text
ProviderState = DONE
ArtifactState = PRODUCED
MutationState = APPLIED
VerificationState = PASSED
ObjectiveState = PROVEN
```

is valid only when the evidence satisfies the objective contract.

This is invalid:

```text
MutationState = APPLIED
→ therefore ObjectiveState = PROVEN
```

---

# 17. Truthful State Transitions

Every runtime state must correspond to an actual event.

The UI must render runtime state.

The UI must not predict runtime state.

Forbidden:

```text
✓ inspect
✓ analyze
✓ mutate
```

before those operations have occurred.

Required:

```text
DISCOVERY_STARTED
DISCOVERY_COMPLETED
CONTEXT_READY
COMPUTE_STARTED
COMPUTE_COMPLETED
MUTATION_APPLIED
VERIFICATION_COMPLETED
OBJECTIVE_PROVEN
```

Events must be emitted from actual execution boundaries.

---

# 18. Transparency Rule

Every consequential operation must be understandable to the user.

IZEN must expose, at an appropriate level:

* what it is doing;
* why it is doing it;
* what target is affected;
* what capability is being used;
* whether approval is required;
* what actually happened;
* what failed;
* what remains incomplete.

Do not replace meaningful execution information with generic logs.

Do not dump internal implementation noise instead of explaining the operation.

The runtime should communicate:

```text
Action
Reason
Target
Risk
Result
Evidence
Next step
```

when those fields are relevant.

---

# 19. Human Authority

Human authority is decisive for consequential decisions.

However:

> **Human control does not mean mechanical confirmation of every operation.**

Low-risk, reversible, already-authorized operations may execute automatically within the granted boundary.

Human approval should be reserved for decisions with meaningful:

* risk;
* irreversibility;
* external impact;
* security implications;
* scope expansion;
* resource cost;
* destructive consequences;
* ambiguous intent;
* high-value judgment.

The runtime should minimize confirmation fatigue.

A confirmation dialog that appears for everything is not meaningful human control.

---

# 20. Risk-Aware Execution

Every potentially consequential action should have a risk classification.

Risk must consider:

* operation type;
* target;
* reversibility;
* privilege;
* external impact;
* data sensitivity;
* resource consumption;
* blast radius.

Examples of operations that normally require elevated scrutiny:

* destructive deletion;
* forceful Git operations;
* history rewriting;
* privileged commands;
* credential or secret changes;
* system-level modifications;
* external network side effects;
* package installation with meaningful system impact;
* database destructive operations;
* broad workspace mutations;
* irreversible migrations.

The runtime must not classify dangerous commands as safe merely because they are syntactically valid.

---

# 21. Value and Risk Assessment

IZEN may expose a quantitative or categorical assessment of an operation when useful.

For example:

```text
Risk: 72%
Decision value: High
Reversibility: Low
Scope: Workspace-wide
Approval: Required
```

However, numeric values must not be presented as objective truth when they are heuristic estimates.

The runtime must expose the basis of the assessment where practical.

A percentage is a decision aid, not an authority.

---

# 22. Execution Ordering

IZEN must understand dependencies between operations.

It must not execute every discovered action immediately.

The scheduler should establish:

```text
required before
optional before
independent
deferred
blocked
```

Examples:

```text
discover repository
    ↓
read configuration
    ↓
determine test command
    ↓
modify source
    ↓
run focused test
    ↓
run broader verification
```

Independent operations may execute concurrently when:

* there is no dependency;
* resource limits allow it;
* execution order does not affect correctness;
* concurrent execution does not increase risk unnecessarily.

---

# 23. Lazy Execution

IZEN should be lazy where laziness improves:

* latency;
* token usage;
* CPU usage;
* memory usage;
* network usage;
* clarity;
* reliability.

Do not execute work merely because it is available.

Do the minimum necessary work to establish the next reliable state.

Example:

```text
Need to understand one configuration file
→ do not scan the entire repository.

Need to run one focused test
→ do not immediately run the entire test suite.

Need to determine whether a dependency exists
→ inspect package metadata before performing expensive discovery.
```

Lazy execution must never compromise correctness or safety.

---

# 24. Adaptive Token Budgeting

LLM budgets must be task-aware.

Do not allocate a fixed token budget mechanically to every step.

Budgeting should consider:

* task complexity;
* artifact size;
* context size;
* model capability;
* previous progress;
* remaining work;
* output rate;
* failure history;
* continuation viability.

The runtime must detect:

```text
no progress
repeated truncation
repeated failure
context exhaustion
budget exhaustion
```

and change strategy rather than blindly repeating the same call.

---

# 25. Progress Must Be Measurable

A continuation is valid only when it can make progress.

Progress may be represented by:

* new artifact bytes;
* completed target;
* new evidence;
* resolved dependency;
* verified result;
* reduced failure surface;
* newly established fact.

If a bounded step produces no meaningful progress:

```text
do not blindly retry
```

The scheduler must decide whether to:

* continue with a different budget;
* reduce scope;
* change model;
* change strategy;
* request human input;
* stop.

---

# 26. Loop Safety

IZEN must be resistant to infinite loops.

Every execution loop must have:

* step bounds;
* token bounds;
* time bounds;
* resource bounds;
* repetition detection;
* progress detection;
* failure classification;
* termination conditions.

A loop must never continue merely because the model keeps producing output.

The runtime owns termination.

---

# 27. Recovery and Continuation

Execution must survive recoverable interruptions where practical.

Relevant interruptions include:

* model output limits;
* provider failure;
* network interruption;
* process interruption;
* terminal interruption;
* model switching;
* runtime restart;
* partial execution;
* verification failure.

The runtime should persist sufficient state to determine:

```text
what was known
what was attempted
what actually happened
what was applied
what remains
what must not be repeated
what can safely continue
```

---

# 28. Model Switching

The LLM provider/model is replaceable.

Switching models must not destroy runtime state.

The runtime must preserve:

* ExecutionSpec;
* context identity;
* evidence;
* mutation state;
* verification state;
* remaining objective;
* budgets;
* failure history;
* continuation position.

A model is a compute engine, not the owner of the task.

---

# 29. Interrupt Safety

If execution is interrupted, IZEN must fail into a truthful recoverable state.

It must never assume:

```text
interrupted = completed
```

or:

```text
process disconnected = operation succeeded
```

After interruption, the runtime must reconcile observable state before continuing.

For mutations, this may require:

```text
checkpoint
→ inspect actual disk state
→ reconcile MutationSet
→ continue or rollback
```

---

# 30. Verification

Verification must be proportional to the objective.

Verification may include:

* syntax checks;
* compilation;
* tests;
* static analysis;
* structural inspection;
* command results;
* runtime checks;
* domain-specific validation;
* user-visible inspection.

If verification is unavailable, IZEN must say so.

It must never report:

```text
verified
```

when verification was merely skipped.

---

# 31. Failure Is Information

Failures must be classified, not hidden.

Useful failure categories include:

```text
INPUT_INVALID
TARGET_UNKNOWN
TARGET_AMBIGUOUS
UNAUTHORIZED
CONTEXT_INSUFFICIENT
COMPUTE_FAILED
OUTPUT_EXHAUSTED
NO_PROGRESS
CAPABILITY_UNAVAILABLE
MUTATION_REJECTED
MUTATION_FAILED
VERIFICATION_FAILED
OBJECTIVE_UNSUBSTANTIATED
INTERRUPTED
RESOURCE_EXHAUSTED
```

The user should receive the smallest useful explanation of:

```text
what failed
why
what state remains true
what can happen next
```

---

# 32. No Silent Recovery

Recovery must not silently change the task.

If IZEN changes:

* target;
* scope;
* model;
* strategy;
* budget;
* command;
* execution mode;

the change must remain within policy and be observable when consequential.

A recovery mechanism must never become a hidden autonomous authority.

---

# 33. No Feature May Bypass the Runtime

Any new feature must use the canonical:

```text
Policy
→ Context
→ ExecutionSpec
→ Scheduler
→ Capability
→ Evidence
→ Verification
```

path.

Forbidden:

* direct provider calls from UI;
* direct filesystem writes from model adapters;
* ad-hoc shell execution;
* feature-specific mutation paths;
* feature-specific approval paths;
* feature-specific completion claims;
* hidden retries;
* hidden autonomous loops.

---

# 34. Benchmark Independence

A benchmark must test the runtime.

The runtime must not be designed around the benchmark.

A static HTML/CSS/JS portfolio task may be used because its output is visually observable.

It must not cause IZEN to acquire web-specific runtime architecture.

The same execution primitives must work for:

* web;
* Go;
* Python;
* Rust;
* shell;
* configuration;
* repository maintenance;
* testing;
* investigation;
* other supported workloads.

Benchmark-specific validators belong in benchmark/test infrastructure unless they represent a genuinely reusable runtime capability.

---

# 35. Practicality Rule

Architecture exists to make execution reliable.

No abstraction should exist merely because it is theoretically elegant.

No abstraction should exist merely to support another abstraction.

Prefer:

```text
simple
explicit
testable
observable
reversible
composable
```

over:

```text
clever
implicit
duplicated
heuristic
opaque
over-engineered
```

---

# 36. Efficiency Rule

IZEN should minimize:

* unnecessary LLM calls;
* unnecessary context;
* unnecessary filesystem scans;
* unnecessary tool calls;
* unnecessary verification;
* unnecessary retries;
* unnecessary user confirmations;
* unnecessary serialization;
* unnecessary latency.

But optimization must never bypass:

* authorization;
* evidence;
* safety;
* correctness;
* state reconciliation.

The goal is not maximum automation.

The goal is:

> **Maximum useful work per unit of time, tokens, computation, and risk.**

---

# 37. Completion Rule

Every execution must terminate in a truthful state.

Valid terminal outcomes include:

```text
PROVEN
FAILED
UNSUBSTANTIATED
BLOCKED
REQUIRES_AUTHORIZATION
CANCELLED
INTERRUPTED
```

Never use:

```text
DONE
```

as a generic synonym for “the process stopped.”

---

# 38. Final Execution Principle

IZEN follows one simple rule:

> **If IZEN cannot prove why an action should be taken, what it is acting on, and what happened afterward, it must not pretend certainty.**

Therefore:

```text
Understand what must be done.
Establish what is known.
Determine what is sufficient.
Execute only what is authorized.
Observe what actually happened.
Verify what can be verified.
Stop when evidence is insufficient.
```

Every execution should end with a concise human-readable conclusion:

```text
TL;TR:
<what was done>
<what was proven>
<what remains uncertain or incomplete>
```

The conclusion is derived from runtime evidence, not generated as an independent model claim.

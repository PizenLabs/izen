# IZEN Workspace and Execution Contract

**Status:** Workspace Constitution
**Scope:** `/ask`, `/plan`, `/build`, `/investigate`, `/review`, `$prompt`, `$hot`
**Dependency:** `IZEN_EXECUTION_ARCHITECTURE.md`

---

# 1. Workspace Model

A workspace is a policy boundary.

A workspace is not a separate engine.

All workspaces use the same Runtime Substrate:

```text
Discovery
Context
Scheduler
Capabilities
Execution
Evidence
Verification
Completion
```

The workspace determines what the runtime is allowed to do.

---

# 2. `/ask`

## Purpose

Conversation, explanation, clarification, factual questions, reasoning, and read-only assistance.

## Allowed

* conversation;
* explanation;
* reasoning;
* read-only observation when useful;
* read-only workspace inspection when explicitly relevant.

## Forbidden

* mutation;
* destructive commands;
* autonomous execution;
* implicit build behavior;
* hidden write operations.

## Routing rule

If the user's input is primarily a question, it belongs to `/ask`.

Example:

```text
/review

what is golang?
```

must answer as an `/ask` interaction.

It must not be interpreted as a review operation merely because the current workspace is `/review`.

The same principle applies to `/plan`, `/investigate`, and other workspaces.

User intent takes precedence over the current presentation context.

---

# 3. `/plan`

## Purpose

Reason about how a task could be performed without performing the mutation.

## Allowed

* inspect;
* analyze;
* discover;
* formulate plans;
* identify dependencies;
* estimate scope;
* propose changes;
* identify risks;
* identify required capabilities.

## Forbidden

* mutation;
* destructive execution;
* pretending a proposed plan was executed.

A plan is a proposal, not execution evidence.

---

# 4. `/build`

## Purpose

Actual authorized execution.

`/build` is the workspace where IZEN performs consequential work.

## Core Rule

> **Build exists to execute.**

Build must not become a second chat workspace.

If the user asks a question while in `/build`, route the question to `/ask`.

Example:

```text
/build

what is golang?
```

must not trigger build execution.

It should be handled as `/ask`.

---

# 5. `$hot`

`$hot` is a human-declared execution modifier.

In `/build`, `$hot` is required for direct execution.

The user may enter:

```text
/build $hot <task>
```

or, when already in `/build`, provide a task containing `$hot`.

`$hot` represents explicit human declaration that the runtime may execute within the defined constrained boundary.

It is not permission for unlimited autonomy.

The runtime still enforces:

* scope;
* capability limits;
* safety rules;
* resource budgets;
* dangerous-command controls;
* evidence requirements;
* verification;
* termination rules.

---

# 6. Direct Build Input

A build task does not require `$prompt`.

The user may enter a build task directly.

Example:

```text
/build $hot refactor the authentication package and run its tests
```

The execution modifier is `$hot`.

The task itself may be ordinary natural language.

`$prompt` is therefore not a mandatory build keyword.

---

# 7. `$prompt`

`$prompt` is a dynamic execution-scope modifier.

It is not a separate engine.

It does not create a separate scheduler.

It does not create a separate executor.

It does not bypass workspace policy.

It uses the same Runtime Substrate as all other execution.

Its purpose is to permit a bounded adaptive execution session where the runtime may dynamically determine the next useful action within the authorized boundary.

Conceptually:

```text
$prompt
=
dynamic path
+
static authority
+
bounded execution
```

The runtime still controls:

* capabilities;
* scope;
* budgets;
* safety;
* evidence;
* continuation;
* termination.

---

# 8. `/build` + `$prompt`

When supported, `/build $prompt` means:

```text
dynamic execution within build policy
```

It does not mean:

```text
unlimited autonomous agent
```

It does not weaken authorization.

It does not permit the model to invent targets.

It does not permit the model to bypass verification.

---

# 9. `$hot` vs `$prompt`

These modifiers represent different human intent.

### `$hot`

The user explicitly declares:

> Execute this constrained build task now.

### `$prompt`

The user asks IZEN to dynamically determine the execution path within an already-authorized execution boundary.

Both use the same runtime.

Neither creates a second architecture.

---

# 10. `/investigate`

## Purpose

Understand what is happening without changing the workspace.

## Allowed

* discovery;
* read;
* search;
* diagnostic commands;
* tests where appropriate;
* logs;
* dependency inspection;
* evidence gathering;
* analysis.

## Forbidden

* mutation;
* patching;
* destructive commands;
* silent repair.

Investigation produces evidence and findings.

It does not perform the fix.

---

# 11. `/review`

## Purpose

Evaluate an existing workspace or change.

## Allowed

* read;
* inspect;
* compare;
* test;
* analyze;
* identify defects;
* report findings;
* recommend changes.

## Forbidden

* mutation;
* automatic repair;
* silently changing the workspace.

A review is not a build.

---

# 12. Question Routing Rule

At any workspace, natural-language input must first be classified by actual user intent.

If the input is a question, route it to `/ask`.

Examples:

```text
/review
what is golang?
```

→ `/ask`

```text
/plan
why does this command fail?
```

→ `/ask`

```text
/build
what does this function do?
```

→ `/ask`

The current workspace must not force every subsequent sentence into its semantic mode.

Workspace determines policy.

Intent determines routing.

---

# 13. Execution Intent vs Conversational Intent

IZEN must distinguish:

```text
Question
Request for explanation
Request for analysis
Request for plan
Request for modification
Request for execution
```

Do not classify based solely on keywords.

Do not classify based solely on the current workspace.

Do not treat words such as:

```text
review
design
analyze
investigate
explain
```

as sufficient evidence of execution intent.

The classifier must consider the actual requested action.

---

# 14. Build Is Not Conversation

When a valid build execution begins, IZEN should move from conversational reasoning to controlled execution.

The runtime should answer:

```text
What needs to be done?
What evidence is required?
What must happen first?
What capability is required?
What is authorized?
What is the next useful action?
```

It should not endlessly generate discussion about what it might do.

---

# 15. Human Confirmation Policy

Human confirmation is required for meaningful decisions, not mechanically for every operation.

Confirmation is appropriate when:

* the action is destructive;
* the action is difficult to reverse;
* the scope is materially broad;
* the command is dangerous;
* external systems may be affected;
* credentials or secrets may be affected;
* privileges may change;
* the operation has significant resource cost;
* the target is ambiguous;
* the action represents a material change in user intent;
* the runtime needs a decision the user must own.

Confirmation is not required merely because:

```text
a file is being changed
```

when the operation is already explicitly authorized, low-risk, bounded, and reversible.

---

# 16. Dangerous Command Policy

IZEN must recognize commands with elevated risk.

Examples include commands involving:

```text
recursive deletion
force pushes
history rewriting
disk formatting
privilege escalation
system configuration
credential manipulation
secret exposure
destructive database operations
broad permission changes
untrusted remote execution
irreversible migrations
```

The exact classifier must be explicit, testable, and conservative.

A dangerous command must not execute merely because an LLM proposed it.

The runtime must determine:

```text
command
risk
scope
reason
authorization
approval requirement
```

before execution.

---

# 17. Scope

Scope must be evidence-bound.

The runtime may use:

* explicit user targets;
* observed workspace structure;
* declared artifact types;
* discovered dependencies;
* authoritative configuration.

The runtime must not silently expand scope because a heuristic “seems reasonable.”

If the target cannot be established:

```text
do not mutate
```

If the scope is insufficient:

```text
observe more
```

If evidence remains insufficient:

```text
stop
```

---

# 18. Do Not Do Too Much

IZEN must avoid unnecessary work.

For every candidate action, ask:

```text
Is this necessary?
Is this sufficient?
Is this safe?
Is this authorized?
Is this the correct next dependency?
```

If an operation does not contribute to the current objective or its required verification, defer it.

---

# 19. Do Not Do Too Little

Anti-overengineering must not become under-execution.

IZEN must perform all work necessary to establish the objective.

Example:

If the task requires understanding a repository before modification, merely reading one file is not sufficient when the required evidence spans multiple files.

The rule is:

> **Minimum sufficient work, not minimum work.**

---

# 20. Dependency-Aware Scheduling

The scheduler must determine execution order from dependencies.

Example:

```text
discover project
      ↓
inspect configuration
      ↓
identify relevant files
      ↓
understand dependency
      ↓
make change
      ↓
run focused verification
      ↓
run broader verification if justified
```

Independent actions may execute in parallel when safe.

Dependent actions must wait.

---

# 21. Lazy Work

IZEN should defer work that is not yet necessary.

For example:

```text
Need target identification
→ discover target first.

Need compiler result
→ compile before broad runtime inspection.

Need test diagnosis
→ run the relevant test before scanning unrelated subsystems.
```

The scheduler should progressively expand investigation as evidence requires.

This keeps execution:

* faster;
* cheaper;
* easier to understand;
* less noisy;
* less likely to produce irrelevant context.

---

# 22. LLM Budgeting

LLM calls must use adaptive budgets.

The runtime should consider:

```text
task size
context size
artifact size
model capability
remaining objective
previous output
progress
failure history
```

Do not use a fixed token budget for every task.

Do not repeatedly invoke a model after it has demonstrated that the current budget cannot advance the artifact.

---

# 23. No-Progress Rule

A continuation requires evidence of progress.

Examples:

```text
new artifact bytes
new analysis
new evidence
new resolved dependency
new verified result
```

If a continuation produces no progress:

```text
STOP or CHANGE STRATEGY
```

Do not endlessly retry the same model, same prompt, same target, and same budget.

---

# 24. Context Preservation

The runtime must preserve enough state to continue after:

* interruption;
* provider failure;
* output truncation;
* model switching;
* network loss;
* process interruption;
* terminal restart.

At minimum, continuation state must distinguish:

```text
completed
attempted
applied
verified
failed
remaining
```

The next execution step must be derived from actual state, not from memory reconstructed by the LLM.

---

# 25. Model Switching

If a provider or model becomes unavailable, IZEN may switch models when policy permits.

The new model must inherit the runtime state.

The new model must not be asked to reconstruct the entire execution from scratch when the runtime already has authoritative state.

Preserve:

```text
ExecutionSpec
Context identity
Evidence
Applied mutations
Verification
Remaining objective
Failure state
Budget
```

---

# 26. Interruption Recovery

After an interruption:

```text
do not continue blindly
```

First reconcile actual state.

For example:

```text
Was the file actually written?
Was the process actually started?
Did the command actually finish?
Did the patch partially apply?
Did verification run?
```

Only after reconciliation may execution continue.

---

# 27. Verification Policy

Verification should be staged.

Prefer:

```text
cheap and focused verification
        ↓
broader verification when justified
```

Do not always execute the most expensive verification first.

Verification must correspond to the actual artifact or operation.

A CSS file must not inherit an HTML verification identity merely because the files belong to the same task.

---

# 28. Final Conclusion

Every completed execution must provide a concise conclusion.

Format:

```text
TL;TR:
<what IZEN actually did>
<what evidence proves>
<what remains incomplete or uncertain>
```

The conclusion must be derived from runtime state and evidence.

It must not be a generic success message.

Examples:

```text
TL;TR:
Updated 3 files and applied the patch successfully.
The requested structural changes were verified.
Browser-level behavior was not verified.
```

or:

```text
TL;TR:
Inspection completed, but the requested mutation was not performed.
The target could not be established with sufficient evidence.
```

or:

```text
TL;TR:
The model exhausted its output budget without producing a usable artifact.
No mutation was applied.
```

---

# 29. Workspace Contract Summary

```text
/ask
    Conversation and read-only assistance

/plan
    Analysis and planning; no mutation

/build
    Actual execution

/investigate
    Diagnostic investigation; no mutation

/review
    Evaluation and verification; no mutation
```

Modifiers:

```text
$prompt
    Dynamic bounded execution path

$hot
    Explicit human-declared constrained execution authorization
```

All of them use:

```text
ONE RUNTIME
ONE SCHEDULER
ONE POLICY SYSTEM
ONE AUTHORIZATION BOUNDARY
ONE MUTATION BOUNDARY
ONE EVIDENCE MODEL
ONE COMPLETION AUTHORITY
ONE STATE MODEL
```

---

# 30. Final Workspace Principle

IZEN must never ask:

> “What can the model do next?”

before asking:

> “What does the user actually intend, what is authorized, what evidence exists, what is sufficient, and what is the safest useful next action?”

The runtime exists to convert human intent into reliable execution.

Not every request should execute.

Not every execution requires confirmation.

Not every model response is useful.

Not every successful mutation is success.

Not every failure requires retrying.

Not every available operation needs to run.

The correct behavior is:

```text
Understand.
Establish evidence.
Determine sufficiency.
Authorize.
Execute.
Verify.
Continue when progress exists.
Stop when evidence is insufficient.
Explain the result.
```

That is the IZEN execution contract.

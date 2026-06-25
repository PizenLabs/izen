# PHILOSOPHY.md

## Izen Philosophy

Izen is built on one central belief:

AI should strengthen human judgment, not replace it.

This is the foundation of everything.

Every feature, workflow, and architectural decision must preserve this principle.

If a feature increases automation but reduces human understanding, it should be rejected.

⸻

Core Principles

⸻

1. Human-centered

The human remains the source of truth.

AI is an assistant.

Not an authority.

Not an owner.

Not an autonomous operator.

Izen exists to help humans:

* understand code
* inspect systems
* plan changes
* investigate failures
* execute safely

Human judgment remains final.

Rule:

never optimize away human understanding

⸻

2. Clarity over speed

Fast is useful.

Clear is required.

A fast answer that hides reasoning is dangerous.

Izen must optimize for:

clarity > control > trust > speed

Always.

Rule:

unclear speed is technical debt

⸻

3. Explicit over implicit

Nothing important should happen silently.

The user must know:

* what Izen is doing
* why it is doing it
* what strategy it is using
* what fallback was triggered

Example:

Graph lookup failed.
Escalating to semantic search.

Example:

Semantic confidence low.
Fallback: ripgrep.

Rule:

invisible behavior reduces trust

⸻

4. Minimal by default

Complexity should be activated only when necessary.

Simple inputs should remain simple.

Example:

Hi

must not trigger:

* repository scans
* graph builds
* semantic analysis
* execution engines

Rule:

complexity must be proportional to intent

⸻

5. Local-first

Local is the default.

Not cloud.

Not remote.

Not external.

Izen assumes:

the machine running it owns the work.

Benefits:

* faster
* safer
* more private
* more transparent

Rule:

remote systems must remain optional

⸻

6. Security-aware

Every capability has risk.

Nothing should be trusted by default.

Especially:

* shell execution
* external MCP
* file mutation
* external providers

Security model:

explicit
scoped
revocable

Rule:

capability without boundaries becomes liability

⸻

7. Reversible by design

Every meaningful mutation must be recoverable.

This includes:

* file changes
* generated patches
* execution paths
* plan changes

Mechanisms:

* Git checkpoints
* patch storage
* audit logs

Rule:

if it cannot be reversed, it should not be automatic

⸻

8. Structure before intelligence

Raw context is expensive.

Structured context is powerful.

Izen should always prefer:

graph
symbols
call chains
dependency slices

Before:

full files
full directories
full repositories

Rule:

compress before reasoning

⸻

9. Semantic-first, text-resilient

Preferred order:

Graph
→ Semantic
→ Text fallback

Meaning:

Use the strongest understanding first.

Fallback must always exist.

Reason:

real repositories are imperfect.

Examples:

* broken files
* generated files
* malformed syntax
* logs
* configs

Rule:

purity without resilience is fragile

⸻

10. Modes over prompts

Izen is mode-driven.

Not prompt-driven.

Modes define:

* permissions
* behavior
* boundaries
* retrieval strategies

Not personalities.

Rule:

behavior should be declarative, not improvised

⸻

Operational Rules

⸻

Rule 1

Before adding a feature, ask:

does it improve understanding?

If no:

do not build it.

⸻

Rule 2

Before adding a feature, ask:

does it reduce noise?

If no:

do not build it.

⸻

Rule 3

Before adding a feature, ask:

does it preserve human control?

If no:

do not build it.

⸻

Rule 4

Before adding a feature, ask:

does it increase trust?

If no:

do not build it.

⸻

Rule 5

Before adding a feature, ask:

does it fit local-first?

If no:

it must remain optional.

⸻

Rule 6

Before adding retrieval logic, ask:

can this be solved with graph first?

If yes:

avoid raw reads.

⸻

Rule 7

Before loading context, ask:

is this necessary now?

If no:

delay loading.

⸻

Rule 8

Before executing code, ask:

is this reversible?

If no:

require confirmation.

⸻

Rule 9

Before calling external systems, ask:

can local solve this first?

If yes:

prefer local.

⸻

Rule 10

Before increasing autonomy, ask:

does this reduce human understanding?

If yes:

reject it.

⸻

Anti-Patterns

Izen must avoid:

⸻

Blind autonomy

Wrong:

AI decides and mutates without user clarity

⸻

Context dumping

Wrong:

read entire repository into model

⸻

Silent fallbacks

Wrong:

semantic failed → fallback without telling user

⸻

Prompt blob architecture

Wrong:

agents/*.md control behavior

⸻

Hidden memory

Wrong:

silent state affecting behavior

⸻

Feature accumulation

Wrong:

adding because competitors have it

⸻

Final Rule

If a feature makes Izen more powerful but less understandable:

do not build it.

If a feature makes Izen faster but less trustworthy:

do not build it.

If a feature makes Izen more autonomous but less human-centered:

do not build it.

Izen is not built to replace the human.

It is built to make the human stronger.

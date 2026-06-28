Izen Philosophy & Command Registry

Izen is built on one central, non-negotiable belief:

AI should strengthen human judgment, not replace it.

This is the foundation of everything. Every feature, workflow, and architectural decision must preserve this principle. If a feature increases automation but reduces human understanding, it must be rejected.

Core Principles

1. Human-Centered

The human remains the source of truth. AI is an assistant, not an authority, not an owner, and not an autonomous operator. Izen exists to help humans understand code, inspect systems, plan changes, investigate failures, and execute safely. Human judgment remains final.

Rule: Never optimize away human understanding.

2. Clarity Over Speed

Fast is useful; clear is required. A fast answer that hides its reasoning is highly dangerous. Izen must optimize for this hierarchy of values:


$$\text{Clarity} > \text{Control} > \text{Trust} > \text{Speed}$$

Rule: Unclear speed is technical debt.

3. Explicit Over Implicit

Nothing important should happen silently. The user must always know what Izen is doing, why it is doing it, what retrieval strategy it is using, and what fallback was triggered.

Example: [System]: Graph lookup failed. Escalating to semantic search.

Example: [System]: Semantic confidence low. Fallback: ripgrep lookup.

Rule: Invisible behavior reduces trust.

4. Minimal by Default & Resource Budgeting

Complexity must be activated only when necessary. Simple inputs should remain simple and cheap. Context is financial and performance debt; every automated retrieval step must calculate and estimate its token weight before sending payloads to the language model.

Example: Sending "Hi" must never trigger repository scans, graph builds, semantic analysis, or execution engines.

Rule: Complexity and resource consumption must be proportional to intent.

5. Local-First

Local is the default. Not cloud, not remote, not external. Izen assumes the machine running it owns the work. This makes it faster, safer, more private, and more transparent.

Rule: Remote systems must remain optional.

6. Security-Aware & Scoped Isolation

Every capability has risk. Nothing should be trusted by default, especially shell execution, external Model Context Protocol (MCP) servers, file mutations, and external API providers.

Rule: Capability without explicit boundaries becomes liability.

7. Reversible by Design

Every meaningful mutation must be easily recoverable. This includes file changes, generated patches, execution paths, and workspace plans. This is enforced through Git checkpoints, patch storage, and local audit logs.

Rule: If a mutation cannot be reversed, it must not be automated.

8. Structure Before Intelligence

Raw context is expensive and noisy. Structured context is powerful. Izen should always prefer:


$$\text{Graph AST} \rightarrow \text{Symbol Definitions} \rightarrow \text{Call Chains} \rightarrow \text{Dependency Slices}$$


before resorting to full files, full directories, or full repositories.

Rule: Compress context before reasoning.

9. Semantic-First, Text-Resilient

The preferred retrieval sequence is:


$$\text{Graph AST Query} \rightarrow \text{Semantic Search} \rightarrow \text{Text Fallback}$$


We use the strongest structural understanding first, but reliable fallback mechanisms must always exist because real-world repositories are messy (containing malformed syntax, uncommitted drafts, huge configs, and logs).

Rule: Purity without resilience is fragile.

10. Modes Over Prompts

Izen is strictly mode-driven, not prompt-driven. Modes define permissions, runtime behaviors, retrieval boundaries, and tool access—never superficial AI personalities.

Rule: Behavior must be declarative and deterministic, not improvised.

11. UI as a High-Throughput Dashboard

The terminal is an industrial workspace, not a casual chat app.

Layout over Stream: Persistent metadata (active models, token usage, Git branch, mode status) must live in fixed, dedicated UI regions (Status Bars, Side Panes).

No Scrolling for Info: Scrolling chat lines should strictly hold active reasoning and human dialogue.

Rule: Never make the user scroll through chat logs to find active metadata.

Operational Rules

Rule 1: Before adding a feature, ask: Does it improve human understanding? If no, do not build it.

Rule 2: Before adding a feature, ask: Does it reduce noise? If no, do not build it.

Rule 3: Before adding a feature, ask: Does it preserve human control? If no, do not build it.

Rule 4: Before adding a feature, ask: Does it increase trust? If no, do not build it.

Rule 5: Before adding a feature, ask: Does it fit local-first? If no, it must remain optional.

Rule 6: Before adding retrieval logic, ask: Can this be solved with a Graph query first? If yes, avoid raw file reads.

Rule 7: Before loading context, ask: Is this necessary right now? If no, delay loading.

Rule 8: Before executing a shell command (!), ask: Does this command alter state outside the Git tree (e.g., databases, Docker containers, infrastructure)? If yes, warn the user that this action is irreversible via Git, and require explicit confirmation.

Rule 9: Before calling external systems, ask: Can local solve this first? If yes, prefer local.

Rule 10: Before increasing autonomy, ask: Does this reduce human understanding? If yes, reject it.

Rule 11 (Diagnostic Rule): Before analyzing raw terminal outputs, stack traces, or logs, ask: Can this noise be parsed or pre-filtered locally (e.g., stripping timestamp bloat or duplicate polling lines)? If yes, filter locally first. Raw diagnostics must be skimmed by engines before being read by intelligence.

Rule 12 (Token Budget Guard): If an operation’s calculated input token weight exceeds the current mode's target threshold, Izen must pause, report the weight to the user, and prompt for human pruning.

Anti-Patterns

Blind Autonomy

Wrong: The AI decides, mutates, and runs things behind the scenes without user clarity.

Right: AI proposes a plan; human approves, adjusts, or rejects.

Context Dumping

Wrong: Blindly dumping an entire repository or file into the model's context.

Right: Injecting only calculated dependency slices and compressed AST nodes.

Silent Fallbacks

Wrong: Semantic search failed, so the system silently falls back to a broad regex search without letting the user know.

Right: Explicitly logging every transition of retrieval strategies to the screen.

Prompt Blob Architecture

Wrong: Controlling complex application behavior by piling long prose-based instructions inside LLM system prompts (agents/*.md).

Right: Code controls system boundaries and enforces invariants; prompts only seed intelligence. If the LLM violates a constraint, write a validator, parser, or AST filter in Go.

Hidden Memory

Wrong: Maintaining hidden conversational states that affect AI behavior without being visible to the user.

Right: Making all contextual memory explicit, inspectable, and editable.

Feature Accumulation

Wrong: Adding features simply because competitors or other frameworks have them.

Right: Only building features that strictly align with Izen’s operational rules.

The Omniscient Graph Trap

Wrong: Assuming the codebase dependency graph is always perfect and fully indexed.

Right: Treating the graph as a map, not the ground truth. When the workspace is dirty, cross-reference AST structures with uncommitted Git diffs before reasoning.

Hardened Command Registry & Rationale

To maintain an uncluttered, industrial TUI and eliminate interface noise, Izen enforces a minimal command set. Commands are classified strictly under three criteria: Human Control, State Reversibility, and Context Management.

1. Retained Chat Commands

These are the only slash-commands allowed inside the chat input. They serve a functional purpose that cannot be mapped to static hotkeys without losing precision.

/? or /help

Rationale: Empowers human understanding of the workspace interface on demand.

/quit

Rationale: Provides a predictable, explicit exit hatch that safely commits the active session state.

/mode <ask|plan|build|investigate|review>

Rationale: Establishes a declarative boundary for AI capabilities. It is the primary way the human restricts what the AI is allowed to do.

/objective <description>

Rationale: Expresses the user's high-level intent. This focuses the AI's generation and retrieval engines, keeping token noise low.

/clear

Rationale: Resets the conversation viewport. Essential for human focus and clearing psychological noise.

/drop [target_path]

Rationale: Allows manual, fine-grained removal of files from the context stack, putting the user in complete control of inputs.

/undo

Rationale: Kicks off the reversibility engine to rollback file mutations and local changes.

/commit

Rationale: Signals that the human has audited the changes and marked the current state as a safe baseline.

/checkpoint

Rationale: Creates a manual, pre-emptive local snapshot before triggering complex or experimental runs.

2. The Shell Escape Hatch

!<shell command>

Rationale: A high-utility escape hatch that allows engineers to query or run their system directly. It bypasses AI-mediation entirely, keeping the human as the dominant executor. It is bound by ReadOnly() mode guards and non-Git mutation guards.

3. Refactored / Eliminated Commands (The "Why")

To protect Izen from feature bloat, several traditional commands have been removed or integrated into cleaner UX components:

Removed: /models & /tokens

Why: Querying active models or token usage via chat command is an anti-pattern that violates UI as a High-Throughput Dashboard (Principle 11). Chat space is precious.

Refactored to: Persistent fields on the TUI Status Bar at the bottom of the terminal window. Active model and real-time token metrics are visible at a glance without polluting the terminal logs.

Removed: /history

Why: Printing history as a block of text in the viewport adds massive visual noise and disrupts the flow of code analysis.

Refactored to: Terminal-standard behavior. Pressing the Up and Down arrow keys in the chat input cycles through command and message history, preserving viewport cleanliness.

Removed: /resume

Why: Explicitly typing a command to resume sessions is redundant and adds friction.

Refactored to: Implicit startup restoration. Izen automatically reads the local state of the last active directory session on boot or accepts a session ID via CLI flags (e.g., izen --session <id>).

Final Rule

If a feature makes Izen more powerful but less understandable: do not build it.

If a feature makes Izen faster but less trustworthy: do not build it.

If a feature makes Izen more autonomous but less human-centered: do not build it.

Izen is not built to replace the human. It is built to make the human stronger.

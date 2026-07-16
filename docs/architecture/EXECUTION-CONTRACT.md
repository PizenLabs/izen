# Izen Execution Safety Pipeline (Execution Contract)

Philosophy

Izen is built on a simple principle:

AI should never execute code directly against the developer’s environment. Every action must pass through a verifiable execution pipeline before reaching the workspace.

The pipeline is designed to minimize trust in AI while maximizing developer control, transparency, and recoverability.

⸻

Pipeline Overview

          AI Agent
              │
              ▼
      Intent / Proposal
              │
              ▼
┌────────────────────────────────────────────┐
│          Izen Safety Pipeline              │
├────────────────────────────────────────────┤
│ 1. Capability & Policy Engine              │
│ 2. Git Snapshot                            │
│ 3. Structural Diff Analysis                │
│ 4. Build, Test & Security Verification     │
│ 5. Immutable Audit Log                     │
│ 6. Risk Classification                     │
│ 7. Optional Sandbox (Docker / VM)          │
└─────────────────────┬──────────────────────┘
                      │
              Human Decision
                      │
          Apply to Workspace

⸻

1. Capability & Policy Engine

The first line of defense.

Every AI action is evaluated against a predefined capability model before execution.

Examples:

workspace.read
workspace.write
git.commit
go.build
go.test

Restricted capabilities:

filesystem.home
filesystem.system
network.external
sudo.execute
credential.read

Unknown capabilities are denied by default.

Principle

Default Deny.

⸻

2. Git Snapshot

Before any mutation, Izen captures the current workspace state.

Goals

* Instant rollback
* Safe experimentation
* Deterministic recovery
* Workspace integrity

Snapshots allow developers to revert AI-generated changes without manual intervention.

⸻

3. Structural Diff Analysis

Instead of presenting raw line-based diffs, Izen analyzes semantic changes.

Example output:

Modified package:
internal/auth
Affected:
• Login()
• JWT validation
• Authentication middleware
Public API:
No changes
Database:
No schema changes

This allows developers to review intent rather than implementation details.

⸻

4. Build, Test & Security Verification

Before changes are accepted, Izen performs automated verification.

Typical pipeline:

go fmt
go vet
go test
golangci-lint
govulncheck

Projects may extend this stage with custom verification commands.

Changes failing verification never proceed automatically.

⸻

5. Immutable Audit Log

Every action is recorded.

Example:

09:14
Capability:
workspace.write
Risk:
Medium
Decision:
Approved
Verification:
Passed

The audit log provides complete traceability for every AI-assisted modification.

⸻

6. Risk Classification

Izen estimates operational risk before execution.

Typical indicators include:

* destructive filesystem operations
* credential access
* privilege escalation
* network communication
* shell execution
* mass deletion
* code obfuscation

The classifier does not determine whether code is “malicious.”

Instead, it estimates execution risk.

Possible levels:

Low
Medium
High
Critical

⸻

7. Optional Sandbox

Sandbox execution is policy-driven.

Low-risk operations may execute directly.

High-risk operations may require:

* Docker
* Virtual Machine
* Disposable Workspace
* Remote Runner

The sandbox is optional because isolation introduces I/O overhead and startup latency.

Izen applies sandboxing only when policy requires additional containment.

⸻

Design Principles

* Zero Trust — AI output is never trusted by default.
* Least Privilege — AI receives only the minimum required capabilities.
* Human Authority — Developers remain the final decision-makers.
* Recoverability — Every change must be reversible.
* Transparency — Every action is visible and explainable.
* Defense in Depth — Multiple independent safeguards protect the workspace.
* Policy over Prompts — Security is enforced by the execution engine, not by relying on AI instructions.


// Completion gate: the evidence-backed authority for a "completed" claim.
//
// ── Why this exists ────────────────────────────────────────────────────────
// A provider returning, an execution loop ending, or an absence of errors are
// NOT completion. For a mutation execution the only thing that can substantiate
// completion is the sealed ExecutionEvidence the runtime publishes at
// termination: an untainted COMMITTED record whose mutation set the boundary
// actually applied. This file is the mechanical enforcement of that rule, and
// it is a REDUCER over observed facts — it holds no authority of its own and
// computes nothing that no event reported.
//
// The runtime seals and publishes the evidence BEFORE execution.finished
// (internal/execution/executor.go, sealTerminalEvidence → CompleteExecution), so
// a consumer that observes the completion event has already observed the record
// that justifies it. When the record is missing or refuses, completion is
// blocked — never assumed.
//
// ── Scope: mutation executions only ─────────────────────────────────────────
// The hard invariant is scoped to executions that could change the workspace.
// An execution that never entered the mutation boundary has no filesystem
// claim to substantiate, and its deliverable is the content the invocation
// returned; there the runtime's own terminal verdict stands unopposed. Making a
// read-only answer demand a mutation record would be a category error, not
// rigour.
package presentation

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/execution"
)

// CompletionVerdict is the gate's ruling on one completion claim.
type CompletionVerdict struct {
	// Granted is true only when observed evidence substantiates completion.
	Granted bool
	// Reason is the deterministic, user-readable explanation of a refusal
	// ("" when Granted).
	Reason string
}

// CompletionGate accumulates the observed facts a completion claim is judged
// against. It is populated exclusively by the event reducer; nothing else
// writes to it.
type CompletionGate struct {
	// MutationObserved is true when the execution entered the mutation
	// boundary — the discriminator that makes a completion claim require
	// evidence.
	MutationObserved bool
	// EvidenceObserved is true when a sealed ExecutionEvidence record arrived
	// for this execution.
	EvidenceObserved bool
	// EvidenceOutcome is the canonical outcome of the sealed record.
	EvidenceOutcome execution.ExecutionOutcome
	// EvidenceTainted mirrors the sealed record's mutation-set taint flag: a
	// partial applied-then-rolled-back set, or an apply whose verification gate
	// ran and failed.
	EvidenceTainted bool
	// FilesMutated is the sealed record's count of durably committed files —
	// targets the boundary proved were applied AND changed. It is the runtime's
	// own "the mutation state is known" fact, consumed rather than re-derived.
	FilesMutated int
}

// ObserveMutation records that the execution entered the mutation boundary.
func (g *CompletionGate) ObserveMutation() { g.MutationObserved = true }

// ObserveEvidence records a sealed evidence record's scalar facts. Only the
// first record for an execution binds the gate: the record is immutable and a
// later duplicate for the same attempt carries no new truth.
func (g *CompletionGate) ObserveEvidence(outcome string, tainted bool, filesMutated int) {
	if g.EvidenceObserved {
		return
	}
	g.EvidenceObserved = true
	g.EvidenceOutcome = execution.ExecutionOutcome(outcome)
	g.EvidenceTainted = tainted
	g.FilesMutated = filesMutated
}

// Verdict reduces the observed facts into the single ruling the projection is
// allowed to act on. The order of the rules is the precedence:
//
//  1. No sealed record at all → completion is unsubstantiated. A mutation
//     execution that terminates without publishing evidence has made a claim
//     nobody can check; refusing is the only truthful outcome.
//  2. Tainted mutations → blocked. Partial truth is never projected as truth.
//  3. Non-committed outcome → blocked, with the outcome named.
//  4. Committed and untainted, but nothing was actually mutated → blocked. A
//     committed outcome over an empty mutation set is the exact shape of a false
//     completion: the loop ended, no error was raised, and no file changed.
//  5. Committed, untainted, with a proven mutation → granted.
//  6. Non-mutation execution with no evidence obligation → granted (see the
//     package comment on scope).
//
// Note what this gate does NOT do: it does not re-run or second-guess the
// verification policy. Whether a gate was required, and whether "skipped" is
// acceptable, is the runtime's decision — it is already folded into the
// outcome and the taint flag. The gate reads those rulings; it does not make its
// own.
func (g CompletionGate) Verdict() CompletionVerdict {
	if !g.MutationObserved && !g.EvidenceObserved {
		// No mutation boundary was entered and no record was published: there is
		// no workspace claim to substantiate, so the runtime's terminal verdict
		// stands.
		return CompletionVerdict{Granted: true}
	}
	if !g.EvidenceObserved {
		return CompletionVerdict{Reason: "no sealed execution evidence was published for this attempt"}
	}
	if g.EvidenceTainted {
		return CompletionVerdict{Reason: "sealed evidence carries tainted mutations; partial state is not truth"}
	}
	if !g.EvidenceOutcome.Committed() {
		return CompletionVerdict{Reason: fmt.Sprintf("sealed evidence outcome is %s, not COMMITTED", evidenceOutcomeName(g.EvidenceOutcome))}
	}
	if g.MutationObserved && g.FilesMutated == 0 {
		return CompletionVerdict{Reason: "sealed evidence committed no durable file change; the objective was not met by mutation"}
	}
	return CompletionVerdict{Granted: true}
}

// evidenceOutcomeName renders a sealed outcome for a user-readable refusal. An
// unrecognised value is rendered as-is rather than hidden: a refusal must never
// be less specific than the fact that caused it.
func evidenceOutcomeName(o execution.ExecutionOutcome) string {
	if o == "" {
		return "UNSET"
	}
	return string(o)
}

package autonomy

// ── HARDENED PRE-FLIGHT ADMISSION GATE (Phase 16.1) ────────────────────────
//
// This file implements I12 (No Evidence, No Provider) and I13 (Evidence ≠
// Authority) at the last boundary before planning and provider streaming.
//
// The pipeline has three separable questions and this gate keeps them
// separable:
//
//	Intent      — MAY the objective act at all?        (admission)
//	Target      — WHERE may it act?                    (resolution)
//	Evidence    — WHAT does the workspace contain?     (discovery)
//
// A mutation objective is admitted to the planner ONLY when all three hold:
//
//	1. TargetBinding is admissible — an explicit file, or a user-disambiguated
//	   target. A discovered candidate is NOT admissible on its own.
//	2. ContextSpec carries non-empty AUTHORITATIVE evidence.
//	3. ExecutionSpec has an admissible mutation boundary.
//	4. Zero unresolved target ambiguity remains.
//
// When any of them fails the gate returns BLOCK or DISAMBIGUATE — never ADMIT —
// and the driver halts BEFORE any provider token is billed. "No Evidence, No
// Provider" is therefore structural: there is no branch that proceeds on a
// guess, and no provider call is reachable from a blocked spec.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/execution"
)

// IntentClass is the coarse intent vocabulary the admission gate reasons over.
// IsMutation is the only predicate the gate consults on this axis, so the
// distinction that matters is mutation vs read-only, not the specific verb.
type IntentClass string

const (
	// IntentAsk: a pure question. Read-only.
	IntentAsk IntentClass = "ASK"
	// IntentInvestigate: read-only investigation.
	IntentInvestigate IntentClass = "INVESTIGATE"
	// IntentPlan: read-only planning.
	IntentPlan IntentClass = "PLAN"
	// IntentBuild: an objective that will produce filesystem mutations.
	IntentBuild IntentClass = "BUILD"
	// IntentMutate: an explicit mutation objective.
	IntentMutate IntentClass = "MUTATE"
)

// IsMutation reports whether the intent is mutation-capable. Only a
// mutation-capable intent is subject to the target/evidence/boundary gate; a
// read-only objective may run without a target, because it writes nothing.
func (i IntentClass) IsMutation() bool {
	return i == IntentMutate || i == IntentBuild
}

// MutationBoundary is the admission-time boundary of the objective: does the
// runtime hold a PROVEN place to mutate? It is set by the caller from the
// resolved target/evidence state and is deliberately separate from
// execution.MutationBoundaryState (the post-execution FILESYSTEM fact).
type MutationBoundary string

const (
	// MutationBoundaryUnbound: no proven mutation boundary exists. A mutation
	// MUST NOT be admitted.
	MutationBoundaryUnbound MutationBoundary = "UNBOUND"
	// MutationBoundaryBound: a proven mutation boundary exists.
	MutationBoundaryBound MutationBoundary = "BOUND"
)

// EvidenceState is the admission-time evidence axis. PRODUCED means
// authoritative, non-empty workspace evidence was attached to the context;
// NONE means the context is empty.
type EvidenceState string

const (
	// EvidenceNone: no authoritative evidence is attached.
	EvidenceNone EvidenceState = "NONE"
	// EvidenceProduced: authoritative evidence is attached.
	EvidenceProduced EvidenceState = "PRODUCED"
)

// ContextChannel is one authoritative context feed attached to the execution
// context. A mutation objective with zero channels has nothing to compile a
// prompt from and must not reach a provider.
type ContextChannel struct {
	// Kind is the channel kind (workspace, target, session…).
	Kind string
	// Source names where the channel's content came from.
	Source string
	// Authoritative reports whether the channel is runtime-observed evidence
	// rather than a model claim.
	Authoritative bool
}

// ExecutionSpec is the frozen, admission-time view of an execution. It is a
// pure value: constructing one invokes nothing and mutates nothing. The gate
// reads it; the gate never writes it.
type ExecutionSpec struct {
	// Intent is the classified objective intent.
	Intent IntentClass
	// TargetBinding is the resolver's verdict for the mutation target. Nil for
	// a read-only objective that never resolved a target.
	TargetBinding *execution.TargetBindingResult
	// ExplicitTargets is the caller-stated target set. An explicitly stated
	// file that does not exist yet is an admissible CREATION target; a
	// not-found result with no explicit statement is not.
	ExplicitTargets []string
	// WorkspaceEvidence is the DISCOVERY output. It is context only (I13): it is
	// never read to populate the mutation target.
	WorkspaceEvidence *execution.WorkspaceProfile
	// ContextChannels are the authoritative context feeds. A mutation with zero
	// channels is inadmissible.
	ContextChannels []ContextChannel
	// MutationBoundary is the admission-time boundary state.
	MutationBoundary MutationBoundary

	// ── Observed lifecycle state (for state-purity assertions) ──────────
	// These are the four independent lifecycle axes the authority layer
	// defines. On a blocked spec every one of them is at its zero value: a
	// block may not leave a partial "produced" fact behind.
	Evidence  EvidenceState
	Artifact  execution.ArtifactState
	Mutation  execution.MutationBoundaryState
	Objective execution.ObjectiveState

	// ProviderCalls / ProviderTokens are the billed provider facts observed at
	// admission time. On a BLOCK they MUST both be zero (I12).
	ProviderCalls  int
	ProviderTokens int
}

// HasAdmissibleMutationBoundary reports whether a proven mutation boundary
// exists. It is the AND of the boundary flag and a dispatchable TargetBinding:
// a boundary declared BOUND by a caller who holds no proven target is not a
// boundary, it is a claim.
func (s ExecutionSpec) HasAdmissibleMutationBoundary() bool {
	if s.MutationBoundary != MutationBoundaryBound {
		return false
	}
	if s.TargetBinding == nil {
		return false
	}
	if s.TargetBinding.Dispatchable() {
		return true
	}
	// An explicitly named creation target is an admissible boundary: the caller
	// named the file, so "does not exist yet" is a creation, not an invention.
	return s.TargetBinding.State == execution.TargetStateNotFound && len(provenExplicit(s.ExplicitTargets)) > 0
}

// provenExplicit filters blanks from a stated target set.
func provenExplicit(targets []string) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		if strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
	}
	return out
}

// HasAuthoritativeEvidence reports whether the context carries non-empty
// authoritative evidence.
func (s ExecutionSpec) HasAuthoritativeEvidence() bool {
	if s.Evidence == EvidenceProduced {
		return true
	}
	for _, ch := range s.ContextChannels {
		if ch.Authoritative && strings.TrimSpace(ch.Kind) != "" {
			return true
		}
	}
	return false
}

// AdmissionVerdict is the closed, three-value outcome of the gate.
type AdmissionVerdict string

const (
	// AdmissionAdmit: the objective may proceed to planning.
	AdmissionAdmit AdmissionVerdict = "ADMIT"
	// AdmissionDisambiguate: the human must name the target. No provider call.
	AdmissionDisambiguate AdmissionVerdict = "DISAMBIGUATE"
	// AdmissionBlock: the objective is inadmissible. No provider call.
	AdmissionBlock AdmissionVerdict = "BLOCK"
)

// AdmissionFailureClass is the stable failure taxonomy for a blocked
// admission. It is deliberately a small closed set so a caller cannot invent a
// new class at a call site.
type AdmissionFailureClass string

const (
	// FailureClassInadmissibleTarget: the objective has no admissible target or
	// no admissible mutation boundary. Both are target-side failures.
	FailureClassInadmissibleTarget AdmissionFailureClass = "INADMISSIBLE_TARGET"
)

// ErrInadmissibleTarget is the sentinel returned when an objective has no
// admissible target (unbound directory with no usable candidates, or no
// candidates at all).
var ErrInadmissibleTarget = errors.New("autonomy: inadmissible target — the objective has no proven, admissible mutation target")

// ErrUnboundMutationBoundary is the sentinel returned when a mutation objective
// reaches admission without a proven mutation boundary.
var ErrUnboundMutationBoundary = errors.New("autonomy: unbound mutation boundary — no proven place to mutate")

// PreflightOutcome is the complete, evidence-carrying verdict of one gate pass.
type PreflightOutcome struct {
	// Verdict is the closed three-value outcome.
	Verdict AdmissionVerdict
	// Err is the typed sentinel for a BLOCK (nil otherwise).
	Err error
	// FailureClass classifies a BLOCK.
	FailureClass AdmissionFailureClass
	// Reason explains the verdict deterministically.
	Reason string
	// Candidates is the disambiguation set for a DISAMBIGUATE verdict.
	Candidates []string
}

// Blocked reports whether the verdict forbids a provider call.
func (o PreflightOutcome) Blocked() bool {
	return o.Verdict == AdmissionBlock || o.Verdict == AdmissionDisambiguate
}

// Failed reports whether the verdict is a hard block.
func (o PreflightOutcome) Failed() bool { return o.Verdict == AdmissionBlock }

// HaltPreflight is the canonical BLOCK constructor. It is a function (rather
// than an inline struct) so no call site can produce a BLOCK without a failure
// class and a reason.
func HaltPreflight(err error, class AdmissionFailureClass, reason string) PreflightOutcome {
	return PreflightOutcome{
		Verdict:      AdmissionBlock,
		Err:          err,
		FailureClass: class,
		Reason:       reason,
	}
}

// TransitionToAwaitingDisambiguation is the canonical DISAMBIGUATE constructor.
// It carries the candidate set the human must choose between.
func TransitionToAwaitingDisambiguation(spec ExecutionSpec) PreflightOutcome {
	var candidates []string
	if spec.TargetBinding != nil {
		candidates = append(candidates, spec.TargetBinding.Candidates...)
	}
	if len(candidates) == 0 && spec.WorkspaceEvidence != nil {
		candidates = spec.WorkspaceEvidence.CandidatePaths()
	}
	return PreflightOutcome{
		Verdict:    AdmissionDisambiguate,
		Candidates: candidates,
		Reason: fmt.Sprintf(
			"the target is unresolved and the workspace offers %d candidate(s); the human must name the target before any provider call",
			len(candidates)),
	}
}

// EvaluatePreflightAdmission applies I12/I13 to one execution spec. It is a
// PURE function: it reads the spec, invokes nothing, and touches no provider.
//
// The evaluation order is load-bearing. Disambiguation is offered BEFORE a
// hard block, because "choose one of these two files" is actionable while
// "inadmissible" is not; and the boundary check runs last, because a proven
// target is the precondition for a proven boundary.
func EvaluatePreflightAdmission(spec ExecutionSpec) PreflightOutcome {
	// A read-only objective writes nothing, so the target/evidence/boundary
	// gate does not apply to it.
	if !spec.Intent.IsMutation() {
		return PreflightOutcome{Verdict: AdmissionAdmit, Reason: "read-only intent requires no mutation target"}
	}

	// ── No Evidence, No Provider ───────────────────────────────────────
	// A mutation with no target evidence at all cannot be planned. This is the
	// unbreakable arm of I12.
	if spec.TargetBinding == nil {
		return HaltPreflight(ErrInadmissibleTarget, FailureClassInadmissibleTarget,
			"mutation intent reached admission with no target binding; the runtime will not dispatch against an unknown destination")
	}

	switch spec.TargetBinding.State {
	case execution.TargetStateUnboundDirectory:
		// I11: a directory is not a file. It is never promoted. The only
		// admissible continuations are a human choice or a hard block.
		candidates := spec.TargetBinding.Candidates
		if spec.WorkspaceEvidence != nil && len(candidates) == 0 {
			candidates = spec.WorkspaceEvidence.CandidatePaths()
		}
		if len(candidates) > 1 {
			return TransitionToAwaitingDisambiguation(spec)
		}
		if !spec.HasAuthoritativeEvidence() && len(spec.ContextChannels) == 0 {
			return HaltPreflight(ErrInadmissibleTarget, FailureClassInadmissibleTarget,
				"the target names a directory, no unique candidate exists, and the context carries no authoritative evidence")
		}
	case execution.TargetStateAmbiguous:
		return TransitionToAwaitingDisambiguation(spec)
	case execution.TargetStateUnboundPath:
		return HaltPreflight(ErrInadmissibleTarget, FailureClassInadmissibleTarget,
			fmt.Sprintf("the target statement is %s; there is no proven file to mutate", spec.TargetBinding.State))
	case execution.TargetStateNotFound:
		// A creation is legitimate ONLY when the caller explicitly named the
		// file. "Not found" with no explicit statement is not a target; it is
		// the absence of one.
		if len(provenExplicit(spec.ExplicitTargets)) == 0 {
			return HaltPreflight(ErrInadmissibleTarget, FailureClassInadmissibleTarget,
				"no target file exists and the objective named none; the runtime will not invent a destination")
		}
	}

	// ── Admissible mutation boundary ───────────────────────────────────
	if !spec.HasAdmissibleMutationBoundary() {
		return HaltPreflight(ErrUnboundMutationBoundary, FailureClassInadmissibleTarget,
			"the mutation objective reached admission without a proven mutation boundary")
	}

	// ── Authoritative evidence ─────────────────────────────────────────
	if !spec.HasAuthoritativeEvidence() {
		return HaltPreflight(ErrInadmissibleTarget, FailureClassInadmissibleTarget,
			"the mutation objective reached admission with no authoritative context evidence")
	}

	return PreflightOutcome{
		Verdict: AdmissionAdmit,
		Reason:  "target proven, mutation boundary admissible, authoritative evidence present",
	}
}

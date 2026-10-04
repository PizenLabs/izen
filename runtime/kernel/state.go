package kernel

import (
	"fmt"
	"strings"
)

// Status is the execution's lifecycle position. It is deliberately NOT a
// success flag: an execution can be FAILED and have perfectly good evidence, and
// can be SUSPENDED with nothing wrong at all.
type Status string

const (
	// StatusAdmitted: the spec validated and the execution was authorized. No
	// capability has been invoked.
	StatusAdmitted Status = "ADMITTED"
	// StatusRunning: at least one capability is being invoked and the execution
	// has not settled.
	StatusRunning Status = "RUNNING"
	// StatusSuspended: the execution stopped deliberately at a bound and can
	// continue from its recorded position.
	StatusSuspended Status = "SUSPENDED"
	// StatusSettled: the execution reached terminal truth. Read Outcome for what
	// that truth is; Status alone never means success.
	StatusSettled Status = "SETTLED"
)

// Terminal reports whether the status settles the execution.
func (s Status) Terminal() bool { return s == StatusSettled }

// String returns the raw status label.
func (s Status) String() string { return string(s) }

// ── The four separate axes ──────────────────────────────────────────────────
//
// Each axis answers exactly one question about a different boundary. Merging
// them is what produces the four lies this kernel exists to prevent:
//
//	Provider DONE      != Artifact PRODUCED
//	Artifact PRODUCED  != Mutation APPLIED
//	Mutation APPLIED   != Objective PROVEN
//
// They are separate types, not separate fields of one enum, so no function can
// accept a single value that stands for more than one boundary.

// ProviderAxis is the TRANSPORT boundary: what the reasoning or process backend
// actually did. It carries no task meaning. `Done` means the socket closed, not
// that anything was accomplished.
type ProviderAxis string

const (
	// ProviderUntouched: no provider or command invocation has been made.
	ProviderUntouched ProviderAxis = "UNTOUCHED"
	// ProviderInvoked: an invocation is in flight or has returned.
	ProviderInvoked ProviderAxis = "INVOKED"
	// ProviderDone: the backend stopped responding. NOT task completion.
	ProviderDone ProviderAxis = "DONE"
	// ProviderTruncated: the backend stopped because it hit its bound, so what
	// arrived is an incomplete prefix by definition.
	ProviderTruncated ProviderAxis = "TRUNCATED"
	// ProviderFailed: the backend failed or refused.
	ProviderFailed ProviderAxis = "FAILED"
)

// Truncated reports whether what the provider delivered is a known-incomplete
// prefix. A truncated delivery can never contribute to a PROVEN outcome.
func (a ProviderAxis) Truncated() bool { return a == ProviderTruncated }

// Satisfied reports whether the transport settled. It is a transport fact only.
func (a ProviderAxis) Settled() bool {
	switch a {
	case ProviderDone, ProviderTruncated, ProviderFailed:
		return true
	default:
		return false
	}
}

// String returns the raw axis label.
func (a ProviderAxis) String() string { return string(a) }

// ArtifactAxis is the PARSER boundary: whether anything usable was extracted from
// what arrived. Raw text is not an artifact, and a truncated prefix is not a
// produced artifact.
type ArtifactAxis string

const (
	// ArtifactNone: nothing usable was extracted.
	ArtifactNone ArtifactAxis = "NONE"
	// ArtifactPartial: something was extracted but the delivery was truncated,
	// so it is a prefix rather than a finished artifact.
	ArtifactPartial ArtifactAxis = "PARTIAL"
	// ArtifactProduced: at least one complete, valid artifact was extracted.
	ArtifactProduced ArtifactAxis = "PRODUCED"
)

// String returns the raw axis label.
func (a ArtifactAxis) String() string { return string(a) }

// MutationAxis is the FILESYSTEM boundary: what actually happened on disk. It is
// derived exclusively from mutation evidence, so it can only say APPLIED when
// bytes were proven to have been written.
type MutationAxis string

const (
	// MutationNone: no write evidence exists.
	MutationNone MutationAxis = "NONE"
	// MutationApplied: at least one destination was proven written and not
	// rolled back.
	MutationApplied MutationAxis = "APPLIED"
	// MutationRolledBack: a write happened and was undone. Never durable truth,
	// and never counted as progress.
	MutationRolledBack MutationAxis = "ROLLED_BACK"
)

// Durable reports whether the filesystem boundary holds committed truth. Only
// this predicate may contribute to a PROVEN outcome.
func (a MutationAxis) Durable() bool { return a == MutationApplied }

// String returns the raw axis label.
func (a MutationAxis) String() string { return string(a) }

// VerifyAxis is the VERIFICATION boundary: what an independent check concluded.
// It is separate from mutation and from artifacts so that "the write landed" and
// "the result is correct" can never be reported as the same fact.
type VerifyAxis string

const (
	// VerifyNotRun: no verification was attempted.
	VerifyNotRun VerifyAxis = "NOT_RUN"
	// VerifyPassed: verification ran and passed.
	VerifyPassed VerifyAxis = "PASSED"
	// VerifyFailed: verification ran and did not pass.
	VerifyFailed VerifyAxis = "FAILED"
	// VerifyNotApplicable: verification was provably unnecessary. Distinct from
	// Passed, and the state records which one it was.
	VerifyNotApplicable VerifyAxis = "NOT_APPLICABLE"
)

// Satisfied reports whether the verification requirement is met. A provably
// not-applicable check satisfies the requirement, but the axis still records
// WHICH, so no projection can render "verified" for a check that never ran.
func (a VerifyAxis) Satisfied() bool {
	return a == VerifyPassed || a == VerifyNotApplicable
}

// String returns the raw axis label.
func (a VerifyAxis) String() string { return string(a) }

// State is the single authoritative execution state. There is exactly one of
// these per execution, it is advanced only by the reducer, and it is advanced
// only by events that describe something that actually happened.
//
// Revision increases by exactly one per accepted transition. A consumer that
// sees a revision it has already processed is looking at a replay, and a
// consumer that sees a gap knows it missed a transition rather than silently
// continuing from a state that never existed.
type State struct {
	// ExecutionID identifies the execution.
	ExecutionID string
	// Revision is the monotonic transition counter.
	Revision uint64
	// Spec is the immutable execution description.
	Spec Spec
	// Status is the lifecycle position.
	Status Status
	// Cursor is the index of the step currently being executed, or len(Program)
	// when the program is complete.
	Cursor int
	// Grant is the authorization the execution runs under.
	Grant Grant
	// Budget is the runtime's own consumption record.
	Budget Accounting

	// Provider is the transport axis.
	Provider ProviderAxis
	// Artifact is the parser axis.
	Artifact ArtifactAxis
	// Mutation is the filesystem axis.
	Mutation MutationAxis
	// Verify is the verification axis.
	Verify VerifyAxis

	// Evidence is the durable observation log, in order.
	Evidence []Evidence
	// Terminal is the terminal truth, set only when Status is SETTLED.
	Terminal Terminal

	// set is the kernel's indexed view over Evidence. It is rebuilt by the
	// reducer on every transition rather than mutated in place, so a State value
	// is always internally consistent with its own Evidence slice.
	set *evidenceSet

	// verifyStarted records that a verification.started event has been applied.
	//
	// It exists because "a check passed" and "a check ran and passed" are
	// different claims, and only the second one may appear in a log. Without this
	// flag a truncated or reordered log could carry a verification verdict with no
	// verification behind it, and Fold would accept it.
	verifyStarted bool
}

// Terminal is the settled verdict about an execution.
type Terminal struct {
	// Outcome is the evidence-gated verdict.
	Outcome Outcome
	// Class is the failure class, or "" for a PROVEN outcome.
	Class FailureClass
	// Reason is the one-line deterministic explanation of the verdict.
	Reason string
	// Step is the step the verdict concerns, when it concerns one.
	Step string
	// Unmet lists the contract clauses the evidence did not satisfy. It is empty
	// only when the outcome is PROVEN.
	Unmet []string
	// Revision is the state revision at which the verdict was recorded.
	Revision uint64
}

// newState builds the initial state for a validated spec and grant. It is the
// only place a State is created, so no execution can begin from a state that
// skipped admission.
func newState(spec Spec, grant Grant) State {
	s := State{
		ExecutionID: spec.ExecutionID,
		Revision:    0,
		Spec:        spec,
		Status:      StatusAdmitted,
		Cursor:      0,
		Grant:       grant,
		Budget: Accounting{
			Declared:      spec.Budget,
			PerCapability: make(map[CapabilityID]int),
		},
		Provider: ProviderUntouched,
		Artifact: ArtifactNone,
		Mutation: MutationNone,
		Verify:   VerifyNotRun,
		set:      newEvidenceSet(),
	}
	return s
}

// Initial returns the admitted state a spec and grant begin from.
//
// It is exported because replay needs it: Fold rebuilds an execution by starting
// from the state that existed before the first event and applying the log. Without
// this, a reader holding a durable log would have no defined starting point, and a
// log that cannot be replayed is a log that cannot be audited.
//
// The returned state is a value. Replaying a log against a different spec or a
// different grant produces a fold failure rather than a plausible-looking state,
// so this cannot be used to reconstruct history that never happened.
func Initial(spec Spec, grant Grant) State {
	return newState(spec, grant)
}

// reindex rebuilds the evidence index from the Evidence slice. The reducer calls
// it after every accepted transition, which is what keeps a State value
// self-consistent and therefore safe to copy, persist, and compare.
func (s *State) reindex() {
	set := newEvidenceSet()
	for _, e := range s.Evidence {
		set.add(e)
	}
	s.set = set
}

// evidenceSet returns the indexed evidence view, rebuilding it if a State was
// constructed as a literal rather than through the reducer.
func (s State) evidenceSet() *evidenceSet {
	if s.set == nil || s.set.totalRecords() != len(s.Evidence) {
		s.reindex()
	}
	return s.set
}

func (s *evidenceSet) totalRecords() int {
	if s == nil {
		return 0
	}
	return len(s.all)
}

// Settled reports whether the execution has reached terminal truth.
func (s State) Settled() bool { return s.Status.Terminal() }

// MutatedTargets returns the concrete targets the state proves were written.
func (s State) MutatedTargets() []string { return s.evidenceSet().mutatedTargets() }

// ObservedTargets returns every target the state has any observation about.
func (s State) ObservedTargets() []string { return s.evidenceSet().observedTargets() }

// Summary renders a one-line truthful summary for a log or a terminal report.
//
// It reports each axis separately on purpose: a single word like "success"
// would have to lie about at least three of them.
func (s State) Summary() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "execution=%s revision=%d status=%s", s.ExecutionID, s.Revision, s.Status)
	fmt.Fprintf(&sb, " provider=%s artifact=%s mutation=%s verify=%s", s.Provider, s.Artifact, s.Mutation, s.Verify)
	fmt.Fprintf(&sb, " cursor=%d/%d", s.Cursor, len(s.Spec.Program))
	fmt.Fprintf(&sb, " budget{%s}", s.Budget.String())
	if s.Status.Terminal() {
		fmt.Fprintf(&sb, " outcome=%s", s.Terminal.Outcome)
		if s.Terminal.Class != "" {
			fmt.Fprintf(&sb, " class=%s", s.Terminal.Class)
		}
		if len(s.Terminal.Unmet) > 0 {
			fmt.Fprintf(&sb, " unmet=[%s]", strings.Join(s.Terminal.Unmet, ";"))
		}
	}
	return sb.String()
}

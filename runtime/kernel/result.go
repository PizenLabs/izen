package kernel

import (
	"fmt"
	"strings"
)

// Result is the terminal projection of one execution.
//
// It is a value, taken after the fact, and it is honest about what it does not
// know. The only field that authorizes a completion claim is Outcome, and
// OutcomeProven is reachable only through adjudication over evidence that the
// event log contains. A Result cannot be constructed with OutcomeProven by a
// caller and treated as authoritative, because Result carries the state it came
// from and the log that produced it.
type Result struct {
	// ExecutionID identifies the execution.
	ExecutionID string
	// Outcome is the evidence-gated verdict.
	Outcome Outcome
	// Class is the failure class, empty when the outcome is PROVEN.
	Class FailureClass
	// Reason is the one-line deterministic explanation.
	Reason string
	// Unmet lists the contract clauses the evidence did not satisfy.
	Unmet []string
	// Evidence is the complete observation record for the execution.
	Evidence []Evidence
	// Budget is the runtime's own consumption record.
	Budget Accounting
	// Revision is the state revision at which the verdict was recorded.
	Revision uint64
	// State is the authoritative state the verdict was derived from.
	State State
}

// Proves reports whether the verdict authorizes a completion claim. Read this
// before reporting success; nothing else in this struct does.
func (r Result) Proves() bool { return r.Outcome.Proves() }

// Settled reports whether the execution reached terminal truth.
func (r Result) Settled() bool { return r.Outcome.Terminal() }

// Err returns the terminal failure as an error, or nil for a PROVEN outcome and
// for a cancellation the caller already knows about.
//
// It returns nil for OutcomeCancelled and OutcomeInterrupted so a caller can tell
// "the runtime declined" from "the runtime broke", which are different operational
// responses.
func (r Result) Err() error {
	if r.Outcome.Proves() {
		return nil
	}
	return Block{
		Class:  r.classOrDerived(),
		Reason: r.Reason,
	}
}

func (r Result) classOrDerived() FailureClass {
	if r.Class != "" && r.Class.Valid() {
		return r.Class
	}
	switch r.Outcome {
	case OutcomeUnsubstantiated:
		return FailureUnsubstantiated
	case OutcomeCancelled:
		return FailureCancelled
	case OutcomeInterrupted:
		return FailureInterrupted
	case OutcomeBudgetExhausted:
		return FailureBudgetExhausted
	case OutcomeRequiresAuthorization:
		return FailureAuthorization
	default:
		return FailureExecution
	}
}

// MutatedTargets returns the concrete targets the execution proves it wrote.
func (r Result) MutatedTargets() []string {
	return r.State.MutatedTargets()
}

// ObservedTargets returns every target the execution has an observation about.
func (r Result) ObservedTargets() []string {
	return r.State.ObservedTargets()
}

// String renders the result for a terminal report.
//
// It reports each axis separately. A single word like "success" would have to lie
// about at least three boundaries, which is the failure mode this whole package
// exists to prevent.
func (r Result) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "execution=%s outcome=%s", r.ExecutionID, r.Outcome)
	if r.Class != "" {
		fmt.Fprintf(&sb, " class=%s", r.Class)
	}
	fmt.Fprintf(&sb, " revision=%d", r.Revision)
	if r.Reason != "" {
		sb.WriteString("\n  reason: ")
		sb.WriteString(r.Reason)
	}
	s := r.State
	fmt.Fprintf(&sb, "\n  axes: provider=%s artifact=%s mutation=%s verify=%s",
		s.Provider, s.Artifact, s.Mutation, s.Verify)
	if mutated := r.MutatedTargets(); len(mutated) > 0 {
		fmt.Fprintf(&sb, "\n  mutated: %s", strings.Join(mutated, " "))
	}
	if observed := r.ObservedTargets(); len(observed) > 0 {
		fmt.Fprintf(&sb, "\n  observed: %s", strings.Join(observed, " "))
	}
	if len(r.Unmet) > 0 {
		fmt.Fprintf(&sb, "\n  unmet: %s", strings.Join(r.Unmet, ", "))
	}
	fmt.Fprintf(&sb, "\n  budget: %s", r.Budget.String())
	return sb.String()
}

// EvidenceLog is the kernel's read model over an execution's evidence.
//
// It is a concrete type rather than an interface. An interface here would have
// exactly one implementation and no second boundary to cross, which is precisely
// the kind of abstraction this kernel is meant to be free of: a consumer reads
// the evidence and never writes it, so a read model over a value is all that is
// needed.
type EvidenceLog struct {
	recorded []Evidence
	set      *evidenceSet
}

// NewEvidenceLog builds a read model over the given records.
func NewEvidenceLog(recorded []Evidence) *EvidenceLog {
	set := newEvidenceSet()
	for _, e := range recorded {
		set.add(e)
	}
	return &EvidenceLog{recorded: append([]Evidence(nil), recorded...), set: set}
}

// Kinds returns the evidence vocabulary that was observed, in canonical order.
func (l *EvidenceLog) Kinds() []EvidenceKind {
	if l == nil {
		return nil
	}
	var out []EvidenceKind
	for _, k := range allEvidenceKinds {
		if l.set.has(k) {
			out = append(out, k)
		}
	}
	return out
}

// For returns the evidence recorded for the given kind.
func (l *EvidenceLog) For(kind EvidenceKind) []Evidence {
	if l == nil {
		return nil
	}
	return append([]Evidence(nil), l.set.byKind[kind]...)
}

// Has reports whether any evidence of the given kind was recorded.
func (l *EvidenceLog) Has(kind EvidenceKind) bool {
	return l != nil && l.set.has(kind)
}

// Recorded is the durable list a consumer can project.
func (l *EvidenceLog) Recorded() []Evidence {
	if l == nil {
		return nil
	}
	return append([]Evidence(nil), l.recorded...)
}

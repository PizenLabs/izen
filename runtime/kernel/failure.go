package kernel

import (
	"errors"
	"fmt"
	"strings"
)

// FailureClass is the closed, domain-neutral taxonomy of why an execution
// stopped. It answers exactly one question: "what kind of situation is this?",
// never "did something go wrong".
//
// A generic `error = true` is what makes a runtime impossible to audit, so the
// vocabulary is closed: adding a member is a deliberate edit to this file, which
// is what makes every terminal result attributable to a named cause.
type FailureClass string

const (
	// FailureInvalidSpec: the ExecutionSpec is not executable as written (no
	// steps, a capability outside the vocabulary, a contract that demands
	// mutation it cannot have). The kernel refuses before any capability runs.
	FailureInvalidSpec FailureClass = "INVALID_SPEC"

	// FailureAuthorization: the Control Plane did not grant what the step
	// required. This is never retried and never escalated into a capability
	// problem: the capability may be perfectly available, it was simply not
	// permitted.
	FailureAuthorization FailureClass = "AUTHORIZATION_FAILURE"

	// FailureCapabilityUnavailable: the named capability is not registered. No
	// amount of retrying, re-prompting or re-scoping produces it. This is the
	// class that must surface instead of a fabricated success.
	FailureCapabilityUnavailable FailureClass = "CAPABILITY_UNAVAILABLE"

	// FailureCapability: the capability was granted and invoked, and the
	// invocation itself broke (a port that would not bind, an unreadable path).
	FailureCapability FailureClass = "CAPABILITY_FAILURE"

	// FailureProvider: the reasoning backend failed, refused, or returned
	// nothing usable. Provider output is data; this class records that the data
	// never arrived, and never stands in for execution truth.
	FailureProvider FailureClass = "PROVIDER_FAILURE"

	// FailureExecution: an authorized execution ran and returned a real
	// non-success outcome (non-zero exit, refused command, absent toolchain).
	FailureExecution FailureClass = "EXECUTION_FAILURE"

	// FailureArtifact: no usable artifact could be produced. Distinct from
	// FailureProvider: the provider answered, and the answer carried nothing
	// the contract could accept.
	FailureArtifact FailureClass = "ARTIFACT_FAILURE"

	// FailureMutation: an authorized mutation was attempted and did not land,
	// or landed and was rolled back. Nothing is durable.
	FailureMutation FailureClass = "MUTATION_FAILURE"

	// FailureVerification: verification ran and did not pass. This is a fact
	// about the objective's obligations, not about the code that produced it.
	FailureVerification FailureClass = "VERIFICATION_FAILURE"

	// FailureBudgetExhausted: an explicit bound was reached. Truncation is
	// never reported as success.
	FailureBudgetExhausted FailureClass = "BUDGET_EXHAUSTED"

	// FailureCancelled: the caller withdrew the execution deliberately, through
	// the cancellation path rather than by crashing.
	FailureCancelled FailureClass = "CANCELLED"

	// FailureInterrupted: the execution stopped for a reason outside the
	// kernel's control (process signal, power loss, panic upstream). The
	// distinction from FailureCancelled matters: a cancelled execution chose
	// its own ending, an interrupted one did not.
	FailureInterrupted FailureClass = "INTERRUPTED"

	// FailureUnsubstantiated: the execution ended without evidence that
	// satisfies the contract. Not a failure — simply unproven. It is its own
	// class precisely so a runtime never has to invent a cause it cannot cite.
	FailureUnsubstantiated FailureClass = "UNSUBSTANTIATED"
)

// allFailureClasses is the canonical ordered taxonomy. Order is stable so that
// rendered catalogs and test tables are reproducible.
var allFailureClasses = []FailureClass{
	FailureInvalidSpec,
	FailureAuthorization,
	FailureCapabilityUnavailable,
	FailureCapability,
	FailureProvider,
	FailureExecution,
	FailureArtifact,
	FailureMutation,
	FailureVerification,
	FailureBudgetExhausted,
	FailureCancelled,
	FailureInterrupted,
	FailureUnsubstantiated,
}

// AllFailureClasses returns the canonical ordered taxonomy.
func AllFailureClasses() []FailureClass {
	out := make([]FailureClass, len(allFailureClasses))
	copy(out, allFailureClasses)
	return out
}

// Valid reports whether c is a member of the closed taxonomy.
func (c FailureClass) Valid() bool {
	for _, known := range allFailureClasses {
		if known == c {
			return true
		}
	}
	return false
}

// String returns the raw class label.
func (c FailureClass) String() string { return string(c) }

// Terminal reports whether this class settles the execution. Every member of
// the taxonomy does; a zero FailureClass (no failure) does not.
func (c FailureClass) Terminal() bool { return c.Valid() }

// Block is an attributable stop: what could not be done, which class it belongs
// to, and which step and capability were actually involved.
//
// A Block is not a verdict about the objective. It is the kernel declining to
// continue, with the reason made inspectable. Recovery may not widen authority,
// so a Block carries the evidence that produced it rather than a suggestion.
type Block struct {
	// Class is the taxonomy entry this block belongs to.
	Class FailureClass
	// Reason is a one-line deterministic explanation. It must be derived from
	// the situation, never from a caller's narrative.
	Reason string
	// Step is the identifier of the step that was blocked ("" when the block is
	// execution-level).
	Step string
	// Capability is the capability the block concerns ("" when the block is not
	// about a specific capability).
	Capability CapabilityID
	// Evidence names the Evidence IDs that produced the block, so a reader can
	// walk from the verdict back to the observation that caused it.
	Evidence []string
}

// Error renders the block for a terminal reason string.
func (b Block) Error() string {
	var sb strings.Builder
	sb.WriteString(string(b.Class))
	if b.Step != "" {
		sb.WriteString(" [step ")
		sb.WriteString(b.Step)
		sb.WriteString("]")
	}
	if b.Capability != "" {
		sb.WriteString(" [")
		sb.WriteString(string(b.Capability))
		sb.WriteString("]")
	}
	if b.Reason != "" {
		sb.WriteString(": ")
		sb.WriteString(b.Reason)
	}
	return sb.String()
}

// ClassOf extracts the failure class from an error, returning the zero class
// when the error is not a kernel Block. Callers that must name a cause use this
// so they cannot accidentally report a generic failure for a typed one.
func ClassOf(err error) FailureClass {
	if err == nil {
		return ""
	}
	var b Block
	if errors.As(err, &b) {
		return b.Class
	}
	return ""
}

// newBlock is the single construction site for blocks, so a block can never be
// built with a class outside the closed taxonomy.
func newBlock(class FailureClass, step string, cap CapabilityID, reason string, evidence ...string) Block {
	if !class.Valid() {
		// A caller naming a class outside the taxonomy is a programming error.
		// Fail closed on the most honest class available rather than inventing
		// a new one.
		class = FailureUnsubstantiated
	}
	return Block{
		Class:      class,
		Step:       step,
		Capability: cap,
		Reason:     reason,
		Evidence:   append([]string(nil), evidence...),
	}
}

// blockf is newBlock with a formatted reason. The formatting is the only
// freedom a call site has over a block's wording.
func blockf(class FailureClass, step string, cap CapabilityID, format string, args ...any) Block {
	return newBlock(class, step, cap, fmt.Sprintf(format, args...))
}

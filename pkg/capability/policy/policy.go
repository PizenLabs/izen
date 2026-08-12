// Package policy implements the configurable failure policy engine of the V3
// Artifact Protocol. When an artifact fails a gate, the policy classifies the
// failure and decides whether the pipeline should reprompt the model (retry)
// or stop immediately (abort).
//
// Classification is deterministic and sentinel-based: contract violations and
// syntax errors are always recoverable by re-prompting, while permission
// denials are never worth retrying and abort the run.
package policy

import "errors"

// ValidationFailureKind classifies the reason an artifact gate failed.
type ValidationFailureKind int

const (
	// FailureContractViolation means the model output contained no valid
	// artifact contract (see extractor.ErrContractViolation).
	FailureContractViolation ValidationFailureKind = iota
	// FailureSyntax means the artifact parsed but violated a language
	// syntax rule (see the validator package).
	FailureSyntax
	// FailurePermissionDenied means the pipeline lacks permission to
	// proceed; retrying cannot help.
	FailurePermissionDenied
	// FailureUnknown is the catch-all for failures the policy cannot
	// classify.
	FailureUnknown
)

// String returns a human-readable failure-kind label.
func (k ValidationFailureKind) String() string {
	switch k {
	case FailureContractViolation:
		return "contract_violation"
	case FailureSyntax:
		return "syntax_error"
	case FailurePermissionDenied:
		return "permission_denied"
	default:
		return "unknown"
	}
}

// PolicyDecision is the deterministic verdict of a FailurePolicy.
type PolicyDecision int

const (
	// DecisionRetry means the failure is recoverable: the pipeline should
	// reprompt the model with the failure directive and try again.
	DecisionRetry PolicyDecision = iota
	// DecisionAbort means the failure is permanent: the pipeline must stop
	// immediately and surface the error.
	DecisionAbort
)

// String returns a human-readable decision label.
func (d PolicyDecision) String() string {
	if d == DecisionAbort {
		return "abort"
	}
	return "retry"
}

// ErrPermissionDenied is the sentinel a caller wraps (or returns directly)
// when a gate is denied for permission reasons. The standard policy maps it to
// DecisionAbort.
var ErrPermissionDenied = errors.New("policy: permission denied")

// FailurePolicy is the pluggable contract of the failure policy engine. Handle
// classifies a gate failure into a retry-or-abort decision; the pipeline
// consults MaxAttempts to enforce the retry budget before giving up.
type FailurePolicy interface {
	// Handle classifies err and returns the policy decision.
	Handle(err error) PolicyDecision
	// MaxAttempts returns the maximum number of retry attempts the policy
	// permits before a recoverable failure becomes a hard failure.
	MaxAttempts() int
}

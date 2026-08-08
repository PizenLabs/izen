package policy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/extractor"
)

// maxRetryAttempts is the default retry budget of the standard policy: a
// recoverable failure may be re-prompted at most this many times.
const maxRetryAttempts = 3

// protocolDirective is the reprompt directive for contract violations: it
// restates the V3 Protocol Specification so the model knows exactly which
// fence shape is accepted.
const protocolDirective = "Protocol Specification directive: your previous response contained no valid artifact contract. " +
	"Re-emit every file strictly inside a formal artifact fence: \":::artifact <workspace-relative-path>\" ... \":::\" " +
	"or a ```lang:path fenced block. No prose outside the fences."

// StandardFailurePolicy is the default failure policy of the V3 pipeline. It
// classifies failures deterministically:
//
//	ErrContractViolation  -> DecisionRetry (Protocol Specification directive)
//	Syntax error          -> DecisionRetry (detailed AST parse directive)
//	PermissionDenied      -> DecisionAbort (stop immediately)
//	Anything else         -> DecisionRetry (safe default for transient errors)
//
// The retry budget is capped at maxAttempts = 3.
type StandardFailurePolicy struct {
	maxAttempts int
}

// NewStandardFailurePolicy returns a StandardFailurePolicy with a retry budget
// of 3 attempts.
func NewStandardFailurePolicy() *StandardFailurePolicy {
	return &StandardFailurePolicy{maxAttempts: maxRetryAttempts}
}

// WithMaxAttempts returns a copy of p with a custom retry budget. A value <= 0
// is ignored so the budget can never be accidentally disabled.
func (p *StandardFailurePolicy) WithMaxAttempts(n int) *StandardFailurePolicy {
	if n <= 0 {
		return p
	}
	cp := *p
	cp.maxAttempts = n
	return &cp
}

// Handle implements FailurePolicy.
func (p *StandardFailurePolicy) Handle(err error) PolicyDecision {
	switch classify(err) {
	case FailurePermissionDenied:
		return DecisionAbort
	default:
		return DecisionRetry
	}
}

// MaxAttempts implements FailurePolicy.
func (p *StandardFailurePolicy) MaxAttempts() int {
	if p == nil || p.maxAttempts <= 0 {
		return maxRetryAttempts
	}
	return p.maxAttempts
}

// Exhausted reports whether attempts has consumed the retry budget.
func (p *StandardFailurePolicy) Exhausted(attempts int) bool {
	return attempts >= p.MaxAttempts()
}

// Classify maps err to its ValidationFailureKind. Contract violations and
// syntax errors are detected via sentinels/error shape; everything else is
// FailureUnknown (which Handle still maps to retry).
func Classify(err error) ValidationFailureKind {
	switch {
	case err == nil:
		return FailureUnknown
	case errors.Is(err, ErrPermissionDenied):
		return FailurePermissionDenied
	case errors.Is(err, extractor.ErrContractViolation):
		return FailureContractViolation
	case isSyntaxError(err):
		return FailureSyntax
	default:
		return FailureUnknown
	}
}

// classify is the unexported alias used by Handle.
func classify(err error) ValidationFailureKind { return Classify(err) }

// isSyntaxError reports whether err carries the shape of a parser diagnostic
// (a "lang: ..." prefix emitted by the validator package) or a raw Go/JSON
// parser error. Validator errors are the only classification the policy knows
// are syntax failures without a sentinel.
func isSyntaxError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, prefix := range []string{"html: ", "json: ", "go: ", "validator: "} {
		if strings.HasPrefix(msg, prefix) {
			return true
		}
	}
	return false
}

// Directive returns the reprompt directive for err. Contract violations get
// the Protocol Specification directive; syntax errors get the detailed parser
// diagnostics; permission denials and unknown failures return an empty
// directive (the caller should not reprompt).
func Directive(err error) string {
	switch Classify(err) {
	case FailureContractViolation:
		return protocolDirective
	case FailureSyntax:
		return fmt.Sprintf("Syntax error in your previous response. Fix the following parse errors and re-emit every file strictly inside a formal artifact fence:\n%s", err)
	default:
		return ""
	}
}

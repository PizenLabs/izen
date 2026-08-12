package policy

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/extractor"
)

func TestStandardFailurePolicyRetriesSyntaxErrors(t *testing.T) {
	p := NewStandardFailurePolicy()
	for _, err := range []error{
		fmt.Errorf("go: 1:3: expected ';', found '}'"),
		fmt.Errorf("html: unterminated tag at line 2"),
		fmt.Errorf("json: invalid syntax"),
	} {
		if got := p.Handle(err); got != DecisionRetry {
			t.Errorf("Handle(%v) = %v, want DecisionRetry", err, got)
		}
	}
}

func TestStandardFailurePolicyRetriesContractViolations(t *testing.T) {
	p := NewStandardFailurePolicy()
	err := fmt.Errorf("%w: no fences found", extractor.ErrContractViolation)
	if got := p.Handle(err); got != DecisionRetry {
		t.Fatalf("Handle(contract violation) = %v, want DecisionRetry", got)
	}
	if Classify(err) != FailureContractViolation {
		t.Fatalf("Classify = %v, want FailureContractViolation", Classify(err))
	}
}

func TestStandardFailurePolicyAbortsPermissionDenied(t *testing.T) {
	p := NewStandardFailurePolicy()
	if got := p.Handle(ErrPermissionDenied); got != DecisionAbort {
		t.Fatalf("Handle(permission denied) = %v, want DecisionAbort", got)
	}
	wrapped := fmt.Errorf("%w: write denied", ErrPermissionDenied)
	if got := p.Handle(wrapped); got != DecisionAbort {
		t.Fatalf("Handle(wrapped permission denied) = %v, want DecisionAbort", got)
	}
}

func TestStandardFailurePolicyMaxAttemptsCap(t *testing.T) {
	p := NewStandardFailurePolicy()
	if p.MaxAttempts() != 3 {
		t.Fatalf("MaxAttempts = %d, want 3", p.MaxAttempts())
	}
	for i, want := range map[int]bool{
		0: false, 1: false, 2: false,
		3: true, 4: true,
	} {
		if got := p.Exhausted(i); got != want {
			t.Errorf("Exhausted(%d) = %v, want %v", i, got, want)
		}
	}
}

func TestStandardFailurePolicyCustomBudget(t *testing.T) {
	p := NewStandardFailurePolicy().WithMaxAttempts(5)
	if p.MaxAttempts() != 5 {
		t.Fatalf("custom MaxAttempts = %d, want 5", p.MaxAttempts())
	}
	// Non-positive budgets are ignored (the cap can never be disabled).
	if q := NewStandardFailurePolicy().WithMaxAttempts(0); q.MaxAttempts() != 3 {
		t.Fatalf("WithMaxAttempts(0) = %d, want 3", q.MaxAttempts())
	}
}

func TestDirectiveForContractViolation(t *testing.T) {
	d := Directive(extractor.ErrContractViolation)
	if !strings.Contains(d, "Protocol Specification directive") {
		t.Errorf("contract directive must carry the protocol directive, got %q", d)
	}
}

func TestDirectiveForSyntaxError(t *testing.T) {
	d := Directive(fmt.Errorf("go: 1:1: expected 'package', found 'x'"))
	if !strings.Contains(d, "go: 1:1") {
		t.Errorf("syntax directive must embed the AST parse error, got %q", d)
	}
}

func TestDirectiveForAbortIsEmpty(t *testing.T) {
	if d := Directive(ErrPermissionDenied); d != "" {
		t.Errorf("abort directive must be empty, got %q", d)
	}
}

func TestClassifyUnknown(t *testing.T) {
	if Classify(errors.New("some unrelated error")) != FailureUnknown {
		t.Error("unrelated errors must classify as FailureUnknown")
	}
	if Classify(nil) != FailureUnknown {
		t.Error("nil must classify as FailureUnknown")
	}
}

package ingestion

import (
	"errors"
	"testing"
)

// TestProcessWithPolicyGatesMarkupRepair pins the contract-aware repair gate:
// the HTML tag-balance heuristic is only proposed when the artifact's resolved
// contract permits markup repair. A non-markup target must be rejected to the
// contract retry loop, never silently rewritten.
func TestProcessWithPolicyGatesMarkupRepair(t *testing.T) {
	// Unbalanced <div> is syntactically invalid under the content-only
	// classifier, and the HTML balancer can "fix" it.
	payload := "# Title\n<div>content\n"

	allowed, err := ProcessWithPolicy(payload, true)
	if err == nil {
		t.Fatal("expected ErrSyntaxInvalid for an unbalanced payload")
	}
	if !errors.Is(err, ErrSyntaxInvalid) {
		t.Fatalf("error = %v, want ErrSyntaxInvalid", err)
	}
	if allowed.RepairCandidate == nil {
		t.Fatal("markup repair was not proposed when the contract permits it")
	}

	denied, err := ProcessWithPolicy(payload, false)
	if err == nil {
		t.Fatal("expected ErrSyntaxInvalid for an unbalanced payload")
	}
	if !errors.Is(err, ErrSyntaxInvalid) {
		t.Fatalf("error = %v, want ErrSyntaxInvalid", err)
	}
	if denied.RepairCandidate != nil {
		t.Fatalf("markup repair was proposed for a non-markup contract: %+v", denied.RepairCandidate)
	}
}

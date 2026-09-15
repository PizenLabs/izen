package provider

import "testing"

// Universal Stream Outcome Invariant: stop->COMPLETE, length->PARTIAL,
// error->FAILED, cancel->CANCELLED across ALL provider tiers.
func TestMapFinishReasonUniversal(t *testing.T) {
	cases := map[string]StreamOutcome{
		"stop":      StreamComplete,
		"length":    StreamPartial,
		"error":     StreamFailed,
		"cancel":    StreamCancelled,
		"cancelled": StreamCancelled,
	}
	for in, want := range cases {
		if got := MapFinishReason(in); got != want {
			t.Errorf("MapFinishReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProviderCapabilitiesConstrained(t *testing.T) {
	free := ProviderCapabilities{OutputTokenCap: 1024}
	if !free.IsConstrained() {
		t.Error("OutputTokenCap=1024 must be constrained")
	}
	paid := ProviderCapabilities{OutputTokenCap: 16384}
	if paid.IsConstrained() {
		t.Error("OutputTokenCap=16384 must not be constrained")
	}
	unknown := ProviderCapabilities{}
	if unknown.IsConstrained() {
		t.Error("unknown ceiling must not be constrained")
	}
}

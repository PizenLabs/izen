package modes

import "testing"

// TestEffectiveCapabilitiesNeverElevates pins Mode vs Authority
// decoupling: the policy surface filters an explicit grant but never
// adds bits. /build with an empty grant stays empty so the
// authorization gate denies with ErrCapabilityDenied.
func TestEffectiveCapabilitiesNeverElevates(t *testing.T) {
	all := CapRead | CapWrite | CapShell | CapTest | CapPatch | CapCheckpoint
	for _, m := range []Mode{ModeAsk, ModePlan, ModeBuild, ModeInvestigate, ModeReview} {
		if got := EffectiveCapabilities(m, 0); got != 0 {
			t.Fatalf("mode %s elevates empty grant to %v", m, got)
		}
		if EffectiveCapabilitiesGrants(m, 0, CapWrite) {
			t.Fatalf("mode %s grants write without token", m)
		}
		// The effective set never exceeds the explicit grant.
		if got := EffectiveCapabilities(m, all); got&^m.Capabilities() != 0 {
			t.Fatalf("mode %s effective set exceeds policy surface", m)
		}
		if got := EffectiveCapabilities(m, CapRead); got != CapRead {
			t.Fatalf("mode %s filtered read grant = %v, want read", m, got)
		}
	}
	// /build filters but does not invent: write-only grant under /build
	// keeps write; an empty grant under /build keeps nothing.
	if !EffectiveCapabilitiesGrants(ModeBuild, CapWrite, CapWrite) {
		t.Fatal("/build must preserve an explicit write grant")
	}
	if EffectiveCapabilitiesGrants(ModeBuild, 0, CapWrite) {
		t.Fatal("/build must not grant write without a token")
	}
}

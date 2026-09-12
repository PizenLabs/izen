package capability

import domaincap "github.com/PizenLabs/izen/internal/domain/capability"

// ── STEP 1 transitional bridge ────────────────────────────────────────────
// Canonical authorization state lives in internal/domain/capability. These
// aliases keep the legacy execution bridge compiling while high-level
// callers (ui/, planner/, graph/) migrate to the domain. No execution logic
// is deleted in this phase; profiles.go and execution guards stay here.
type Capability = domaincap.Capability
type ScopeRule = domaincap.ScopeRule
type CapabilitySet = domaincap.CapabilitySet

const (
	CapabilityRead       = domaincap.CapabilityRead
	CapabilityWrite      = domaincap.CapabilityWrite
	CapabilityExecute    = domaincap.CapabilityExecute
	CapabilityTest       = domaincap.CapabilityTest
	CapabilityPatch      = domaincap.CapabilityPatch
	CapabilityCheckpoint = domaincap.CapabilityCheckpoint
	CapabilityRollback   = domaincap.CapabilityRollback
)

// NewCapabilitySet returns an empty CapabilitySet (canonical constructor).
func NewCapabilitySet() *CapabilitySet {
	return domaincap.NewCapabilitySet()
}

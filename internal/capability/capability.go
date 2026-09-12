// Package capability defines the decoupled Capability contract of the Izen
// Agent Runtime V3: a single source of truth for what a capability can do,
// how it presents itself to models, and how it validates artifacts.
//
// STEP 1 transitional bridge: the canonical DTOs (CapabilityID,
// ValidationResult, CapabilityContract) and the bitmask authorization state
// live in internal/domain/capability. This package keeps the execution
// bridge (Registry, defaults, alignment, guards) and re-exports the
// canonical contract types via aliases so existing callers keep compiling
// while high-level consumers migrate to the domain.
package capability

import domaincap "github.com/PizenLabs/izen/internal/domain/capability"

// CapabilityID uniquely identifies a capability within a Registry.
type CapabilityID = domaincap.CapabilityID

// ValidationResult is the outcome of a capability validating an artifact.
type ValidationResult = domaincap.ValidationResult

// Capability is the single-source-of-truth contract for one workspace
// capability (aliased to the domain CapabilityContract; renamed there to
// avoid colliding with the bitmask Capability in the same domain package).
type Capability = domaincap.CapabilityContract

// Pass returns a passing ValidationResult. Optional reasons document why.
func Pass(reasons ...string) ValidationResult {
	return domaincap.Pass(reasons...)
}

// Fail returns a failing ValidationResult carrying the rejection reasons.
func Fail(reasons ...string) ValidationResult {
	return domaincap.Fail(reasons...)
}

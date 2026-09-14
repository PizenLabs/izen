package authorization

import (
	"github.com/PizenLabs/izen/internal/core/domain"
)

// SideEffectKind classifies an action by reversibility. Reversible
// actions (local filesystem mutations backed by atomic checkpoint
// rollback) are governed by capability tokens. Irreversible actions
// (external process execution, shell invocation, network calls)
// unconditionally require explicit human approval/token verification in
// addition to the capability token.
type SideEffectKind int

const (
	// SideEffectReversible covers local filesystem mutations backed by
	// atomic checkpoint rollback (file write/delete, patch application).
	SideEffectReversible SideEffectKind = iota
	// SideEffectIrreversible covers external process execution, shell
	// invocation, and network calls, which no checkpoint can undo.
	SideEffectIrreversible
)

// SideEffect enumerates the classified operations of the execution
// boundary. The matrix is explicit: every operation maps to exactly one
// kind, one capability requirement, and one approval requirement.
type SideEffect int

const (
	// EffectFileWrite mutates a workspace file (reversible via rollback).
	EffectFileWrite SideEffect = iota
	// EffectFileDelete removes a workspace file (reversible via rollback).
	EffectFileDelete
	// EffectPatchApply stages and applies a patch (reversible via rollback).
	EffectPatchApply
	// EffectShellExec invokes an external process/shell (irreversible).
	EffectShellExec
	// EffectTestExec runs a test suite as a subprocess (irreversible:
	// external process execution even when workspace mutations roll back).
	EffectTestExec
	// EffectNetworkCall performs a network call (irreversible).
	EffectNetworkCall
)

// Kind returns the reversibility classification of the side effect.
func (e SideEffect) Kind() SideEffectKind {
	switch e {
	case EffectFileWrite, EffectFileDelete, EffectPatchApply:
		return SideEffectReversible
	default:
		return SideEffectIrreversible
	}
}

// RequiredCapability returns the explicit capability token that must be
// present in the scoped grant for the side effect.
func (e SideEffect) RequiredCapability() domain.CapabilityFlag {
	switch e {
	case EffectFileWrite:
		return domain.CapWrite
	case EffectFileDelete:
		return domain.CapWrite
	case EffectPatchApply:
		return domain.CapPatch
	case EffectShellExec:
		return domain.CapExecRestricted
	case EffectTestExec:
		return domain.CapTest
	case EffectNetworkCall:
		return domain.CapExecRestricted
	default:
		return domain.CapabilityFlag(0)
	}
}

// RequiresHumanApproval reports whether the side effect unconditionally
// requires explicit human approval/token verification. All irreversible
// actions do; reversible actions are governed by capability tokens plus
// the 8-clause formula's approval clause.
func (e SideEffect) RequiresHumanApproval() bool {
	return e.Kind() == SideEffectIrreversible
}

// AuthorizeSideEffect evaluates one side effect against an explicit
// capability grant and an approval token. It grants zero authority from
// execution modes: modes are strictly policy-surface filters and MUST
// NOT appear in this decision. Authority derives exclusively from the
// capability tokens and the approval token (the 8-clause formula inputs).
//
//   - Missing capability token → ErrCapabilityDenied (reversible and
//     irreversible alike).
//   - Irreversible action without HumanApproved → ErrApprovalRequired,
//     even when the capability token is present.
//   - Reversible mutation without a checkpoint → ErrNoCheckpoint, since
//     reversibility is only real when rollback is armed.
func AuthorizeSideEffect(
	caps domain.DomainCapabilitySet,
	approval ApprovalToken,
	hasCheckpoint bool,
	effect SideEffect,
) error {
	if !caps.Has(effect.RequiredCapability()) {
		return ErrCapabilityDenied
	}
	if effect.RequiresHumanApproval() && !approval.HumanApproved {
		return ErrApprovalRequired
	}
	if effect.Kind() == SideEffectReversible && !hasCheckpoint {
		return ErrNoCheckpoint
	}
	return nil
}

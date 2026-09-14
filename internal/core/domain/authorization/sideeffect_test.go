package authorization

import (
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain"
)

func TestSideEffectMatrixReversibleNeedsCapabilityAndCheckpoint(t *testing.T) {
	caps := domain.DomainCapabilitySet(domain.CapWrite)
	approval := ApprovalToken{HumanApproved: true}
	if err := AuthorizeSideEffect(caps, approval, false, EffectFileWrite); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("write without checkpoint = %v, want ErrNoCheckpoint", err)
	}
	if err := AuthorizeSideEffect(caps, approval, true, EffectFileWrite); err != nil {
		t.Fatalf("write with token+checkpoint = %v, want nil", err)
	}
	if err := AuthorizeSideEffect(domain.DomainCapabilitySet(0), approval, true, EffectFileWrite); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("write without token = %v, want ErrCapabilityDenied", err)
	}
}

func TestSideEffectMatrixIrreversibleRequiresHumanApproval(t *testing.T) {
	caps := domain.DomainCapabilitySet(domain.CapExecRestricted)
	pre := ApprovalToken{HumanApproved: false, BudgetIsPreApproval: true}
	if err := AuthorizeSideEffect(caps, pre, true, EffectShellExec); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("shell with pre-approval only = %v, want ErrApprovalRequired", err)
	}
	human := ApprovalToken{HumanApproved: true}
	if err := AuthorizeSideEffect(caps, human, true, EffectShellExec); err != nil {
		t.Fatalf("shell with human approval = %v, want nil", err)
	}
	if err := AuthorizeSideEffect(domain.DomainCapabilitySet(0), human, true, EffectShellExec); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("shell without token = %v, want ErrCapabilityDenied", err)
	}
	// Network calls are irreversible too.
	if err := AuthorizeSideEffect(caps, pre, true, EffectNetworkCall); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("network with pre-approval only = %v, want ErrApprovalRequired", err)
	}
}

func TestSideEffectPatchRequiresPatchToken(t *testing.T) {
	writeOnly := domain.DomainCapabilitySet(domain.CapWrite)
	approval := ApprovalToken{HumanApproved: true}
	if err := AuthorizeSideEffect(writeOnly, approval, true, EffectPatchApply); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("patch with write-only grant = %v, want ErrCapabilityDenied", err)
	}
	withPatch := domain.DomainCapabilitySet(domain.CapWrite | domain.CapPatch)
	if err := AuthorizeSideEffect(withPatch, approval, true, EffectPatchApply); err != nil {
		t.Fatalf("patch with patch grant = %v, want nil", err)
	}
}

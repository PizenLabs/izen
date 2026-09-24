package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/protocol"
)

func TestAdmissionRejectsFileMutationUnderReadOnlyContract(t *testing.T) {
	descriptor := protocol.Describe(protocol.DirectCompletion)
	gateway := NewAdmissionGateway(StandardAdmittedCapabilities())
	decision, err := gateway.Admit(ExecuteRequest{
		InteractionContract: protocol.DirectCompletion,
		Contract:            &descriptor,
		StagedSubTasks: []SubTaskScope{{
			ID:        "st-readonly",
			Operation: string(protocol.OperationFileMutate),
		}},
	}, t.TempDir(), strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation})

	if err == nil || decision.Allowed {
		t.Fatal("FILE_MUTATE crossed a READ_ONLY contract")
	}
	if !errors.Is(err, ErrAuthorityExceeded) {
		t.Fatalf("error = %v, want ErrAuthorityExceeded", err)
	}
	if !errors.Is(err, protocol.ErrAuthorityCeilingExceeded) {
		t.Fatalf("error = %v, want protocol authority-ceiling identity", err)
	}
	var typed *AuthorityExceededError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %T, want *AuthorityExceededError", err)
	}
	if typed.Operation != string(protocol.OperationFileMutate) || typed.Capability != protocol.CapabilityMutate {
		t.Fatalf("typed rejection = %+v, want FILE_MUTATE/mutate", typed)
	}
	if decision.Operation != protocol.OperationFileMutate {
		t.Fatalf("decision operation = %q, want FILE_MUTATE", decision.Operation)
	}
}

func TestAdmissionAuthorityCeilingWinsOverAllowedCapabilities(t *testing.T) {
	descriptor, err := protocol.NewContractDescriptor(protocol.AgenticLoop, protocol.DescriptorOptions{
		AuthorityCeiling: protocol.AuthorityReadOnly,
		AllowedCapabilities: []protocol.Capability{
			protocol.CapabilityRead,
			protocol.CapabilityMutate,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewAdmissionGateway(&AdmittedCapabilities{
		ReadOnly:        true,
		WorkspaceMutate: true,
	})
	decision, admitErr := gateway.Admit(ExecuteRequest{
		InteractionContract: protocol.AgenticLoop,
		Contract:            &descriptor,
		Operation:           string(protocol.OperationFileMutate),
	}, t.TempDir(), strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation})
	if admitErr == nil || decision.Allowed {
		t.Fatal("explicit capability list bypassed a READ_ONLY authority ceiling")
	}
	if !errors.Is(admitErr, ErrAuthorityExceeded) {
		t.Fatalf("error = %v, want ErrAuthorityExceeded", admitErr)
	}
}

func TestAdmissionRejectsShellUnderAgenticCapabilityCeiling(t *testing.T) {
	descriptor := protocol.Describe(protocol.AgenticLoop)
	gateway := NewAdmissionGateway(&AdmittedCapabilities{
		ReadOnly:        true,
		WorkspaceMutate: true,
		ShellSideEffect: true,
	})
	decision, err := gateway.Admit(ExecuteRequest{
		InteractionContract: protocol.AgenticLoop,
		Contract:            &descriptor,
		Command:             "go test ./...",
		StagedSubTasks: []SubTaskScope{{
			ID:        "st-shell",
			Operation: string(protocol.OperationShell),
		}},
	}, t.TempDir(), strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation})

	if err == nil || decision.Allowed {
		t.Fatal("SHELL_EXEC crossed the default AgenticLoop capability ceiling")
	}
	if !errors.Is(err, ErrAuthorityExceeded) {
		t.Fatalf("error = %v, want ErrAuthorityExceeded", err)
	}
	var typed *AuthorityExceededError
	if !errors.As(err, &typed) || typed.Capability != protocol.CapabilityShell {
		t.Fatalf("typed shell rejection = %+v", typed)
	}
}

func TestAdmissionRequiresContractAndRuntimeCapability(t *testing.T) {
	descriptor, err := protocol.NewContractDescriptor(protocol.AgenticLoop, protocol.DescriptorOptions{
		AuthorityCeiling: protocol.AuthorityExecute,
		AllowedCapabilities: []protocol.Capability{
			protocol.CapabilityRead,
			protocol.CapabilityMutate,
			protocol.CapabilityShell,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := ExecuteRequest{
		InteractionContract: protocol.AgenticLoop,
		Contract:            &descriptor,
		Operation:           string(protocol.OperationShell),
		Command:             "go test ./...",
	}

	// The contract permits shell execution, but the runtime admission grant
	// still denies it.  The two boundaries are conjunctive.
	gateway := NewAdmissionGateway(&AdmittedCapabilities{ReadOnly: true, ShellSideEffect: false})
	decision, admitErr := gateway.Admit(req, t.TempDir(), strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation})
	if admitErr == nil || decision.Allowed {
		t.Fatal("contract permission alone bypassed the runtime shell grant")
	}
	if !errors.Is(admitErr, ErrRiskScopeExceeded) {
		t.Fatalf("error = %v, want ErrRiskScopeExceeded", admitErr)
	}

	gateway.SetCapabilities(&AdmittedCapabilities{ReadOnly: true, ShellSideEffect: true})
	decision, admitErr = gateway.Admit(req, t.TempDir(), strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation})
	if admitErr != nil || !decision.Allowed {
		t.Fatalf("fully permitted shell action was rejected: decision=%+v err=%v", decision, admitErr)
	}
}

func TestAdmissionCannotTypeEraseMutationStrategy(t *testing.T) {
	descriptor := protocol.Describe(protocol.DirectCompletion)
	gateway := NewAdmissionGateway(StandardAdmittedCapabilities())
	_, err := gateway.Admit(ExecuteRequest{
		InteractionContract: protocol.DirectCompletion,
		Contract:            &descriptor,
		Operation:           string(protocol.OperationRead),
	}, t.TempDir(), strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation})
	if !errors.Is(err, ErrAuthorityExceeded) {
		t.Fatalf("READ label bypassed mutation strategy: %v", err)
	}
}

func TestAdmissionWithoutDescriptorDoesNotTypeEraseShellAction(t *testing.T) {
	gateway := NewAdmissionGateway(&AdmittedCapabilities{ReadOnly: true, ShellSideEffect: true})
	_, err := gateway.Admit(ExecuteRequest{
		Operation: string(protocol.OperationShell),
		Command:   "go test ./...",
	}, t.TempDir(), strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation})
	if !errors.Is(err, ErrAuthorityExceeded) {
		t.Fatalf("untyped shell action error = %v, want fail-closed authority rejection", err)
	}
}

func TestIntentGatewayThreadsContractDescriptorToRequest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gateway := NewIntentGateway(root)
	req, resolution, err := gateway.Gate(context.Background(), "$prompt change a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if req.InteractionContract != protocol.AgenticLoop || req.Contract == nil {
		t.Fatalf("authorized mutation contract = %q/%v, want agentic descriptor", req.InteractionContract, req.Contract)
	}
	if resolution.InteractionContract != req.InteractionContract || resolution.Contract == nil {
		t.Fatalf("resolution did not carry the same descriptor: %+v", resolution)
	}
	if err := req.Contract.Validate(); err != nil {
		t.Fatalf("threaded descriptor invalid: %v", err)
	}
}

func TestExecutorBindsContractAdmissionBeforeExecution(t *testing.T) {
	descriptor := protocol.Describe(protocol.DirectCompletion)
	executor := NewRuntimeExecutor(t.TempDir(), config.Default(), nil, nil, "")
	res, err := executor.Execute(context.Background(), ExecuteRequest{
		RequestID:           "contract-g4-executor",
		Mode:                "build",
		Prompt:              "change the file",
		InteractionContract: protocol.DirectCompletion,
		Contract:            &descriptor,
		Strategy: &strategy.ExecutionStrategyProfile{
			Strategy:      strategy.TargetedMutation,
			ModelRequired: true,
		},
		StagedSubTasks: []SubTaskScope{{
			ID:        "st-file",
			Operation: string(protocol.OperationFileMutate),
		}},
	})
	if err == nil {
		t.Fatal("executor dispatched a forbidden staged mutation")
	}
	if !errors.Is(err, ErrAuthorityExceeded) {
		t.Fatalf("executor error = %v, want ErrAuthorityExceeded", err)
	}
	if res == nil || res.Proof == nil {
		t.Fatal("contract rejection must still return an execution proof")
	}
	if res.PendingPatchID != "" || len(executor.PendingPatchIDs()) != 0 {
		t.Fatal("contract rejection leaked an approval surface")
	}
}

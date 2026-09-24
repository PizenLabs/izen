package autonomy

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
	"github.com/PizenLabs/izen/internal/protocol"
)

func TestG3DriverRejectsMutationUnderReadOnlyContractBeforeProvider(t *testing.T) {
	root, mock, adapter, _ := testHarness(t, nil)
	direct := protocol.Describe(protocol.DirectCompletion)
	driver := NewDriver(adapter, nil, WithContract(direct))

	_, err := driver.Run(context.Background(), "change bar to qux @note.txt")
	if err == nil {
		t.Fatal("read-only contract accepted a mutation objective")
	}
	if !errors.Is(err, protocol.ErrAuthorityCeilingExceeded) {
		t.Fatalf("error = %v, want authority ceiling rejection", err)
	}
	if mock.calls() != 0 {
		t.Fatalf("provider calls = %d, want zero", mock.calls())
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("workspace changed despite contract rejection: %q", got)
	}
}

func TestG3StagedDAGOperationIsCheckedAgainstContract(t *testing.T) {
	descriptor := protocol.Describe(protocol.AgenticLoop)
	dag := planner.NewExecutionDAG("repair", "note.txt", planner.SplitBoundedLines, "digest", 1000)
	if err := dag.AddTask(planner.SubTask{
		ID:              "st-1",
		Kind:            planner.SplitBoundedLines,
		Target:          "note.txt",
		Description:     "run a command",
		Operation:       string(protocol.OperationShell),
		Region:          planner.Region{StartLine: 1, EndLine: 1},
		EstimatedTokens: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStagedPlanContract(protocol.AgenticLoop, &descriptor, dag); !errors.Is(err, protocol.ErrAuthorityCeilingExceeded) {
		t.Fatalf("error = %v, want shell operation rejected by descriptor", err)
	}
}

func TestG3AlternateStagedScopesRetainOperationCeiling(t *testing.T) {
	descriptor := protocol.Describe(protocol.AgenticLoop)
	err := ValidateDispatchContract(autonomy.LoopRequest{
		Prompt:              "repair note.txt",
		InteractionContract: protocol.AgenticLoop,
		Contract:            &descriptor,
		StagedSubTasks: []execution.SubTaskScope{{
			ID:        "st-1",
			Operation: string(protocol.OperationShell),
		}},
	})
	if !errors.Is(err, protocol.ErrAuthorityCeilingExceeded) {
		t.Fatalf("error = %v, want shell operation rejected", err)
	}
}

func TestG3DispatchRejectsUnknownExplicitContract(t *testing.T) {
	err := ValidateDispatchContract(autonomy.LoopRequest{
		Prompt:              "change bar to qux @note.txt",
		InteractionContract: protocol.InteractionContract("unknown_contract"),
	})
	if !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("error = %v, want invalid-contract rejection", err)
	}
}

func TestG3DispatchRejectsMismatchedEnumAndDescriptor(t *testing.T) {
	direct := protocol.Describe(protocol.DirectCompletion)
	agentic := protocol.Describe(protocol.AgenticLoop)
	err := ValidateDispatchContract(autonomy.LoopRequest{
		Prompt:              "change bar to qux @note.txt",
		InteractionContract: protocol.DirectCompletion,
		Contract:            &agentic,
	})
	if !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("error = %v, want invalid-contract rejection", err)
	}
	if direct.Contract != protocol.DirectCompletion {
		t.Fatalf("unexpected descriptor mutation: %+v", direct)
	}
}

func TestG3ActiveContractIsDefensivelyCopied(t *testing.T) {
	descriptor := protocol.Describe(protocol.AgenticLoop)
	driver := NewDriver(nil, nil, WithContract(descriptor))
	got := driver.ActiveContract()
	if got == nil {
		t.Fatal("ActiveContract returned nil")
	}
	got.Archetype = protocol.ArchetypeVanillaWeb
	got.AllowedCapabilities = nil
	again := driver.ActiveContract()
	if again == nil || again.Archetype == protocol.ArchetypeVanillaWeb || len(again.AllowedCapabilities) == 0 {
		t.Fatalf("caller mutated driver's active descriptor: %+v", again)
	}
}

func TestG3ApprovalObservationCarriesProofContract(t *testing.T) {
	descriptor := protocol.Describe(protocol.AgenticLoop)
	adapter := &ExecutorAdapter{}
	observation := adapter.observe(autonomy.LoopRequest{}, &execution.ExecutionResult{
		RequestID: "held-1",
		Proof: &execution.ExecutionProof{
			InteractionContract: protocol.AgenticLoop,
			ContractDescriptor:  &descriptor,
		},
	})
	if observation.InteractionContract != protocol.AgenticLoop || observation.Contract == nil {
		t.Fatalf("approval observation lost contract metadata: %+v", observation)
	}
	observation.Contract.AllowedCapabilities = nil
	if descriptor.AllowedCapabilities == nil {
		t.Fatal("proof descriptor was aliased into the observation")
	}
}

func TestG3AdapterValidatesDirectLoopRequest(t *testing.T) {
	_, _, adapter, _ := testHarness(t, nil)
	direct := protocol.Describe(protocol.DirectCompletion)
	_, err := adapter.Execute(context.Background(), autonomy.LoopRequest{
		Prompt:              "change bar to qux @note.txt",
		Targets:             []string{"note.txt"},
		InteractionContract: protocol.DirectCompletion,
		Contract:            &direct,
	})
	if !errors.Is(err, protocol.ErrAuthorityCeilingExceeded) {
		t.Fatalf("error = %v, want authority ceiling rejection", err)
	}
}

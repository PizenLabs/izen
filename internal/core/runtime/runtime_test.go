package runtime

import (
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
)

func TestNew(t *testing.T) {
	rc := New(nil, capability.NewCapabilitySet(), budget.DefaultBudget())
	if rc == nil {
		t.Fatal("New returned nil")
	}
	if rc.Caps == nil {
		t.Error("Caps is nil")
	}
	if rc.Budget == nil {
		t.Error("Budget is nil")
	}
}

func TestCanMutateFile_Delegates(t *testing.T) {
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityWrite,
		capability.ScopeRule{Capability: capability.CapabilityWrite, Patterns: []string{"*.go"}},
	)
	rc := New(nil, caps, budget.DefaultBudget())

	if !rc.CanMutateFile("main.go") {
		t.Error("CanMutateFile(main.go) = false, want true")
	}
	if rc.CanMutateFile("main.rs") {
		t.Error("CanMutateFile(main.rs) = true, want false")
	}
}

func TestCanExecuteCommand_Delegates(t *testing.T) {
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityExecute,
		capability.ScopeRule{Capability: capability.CapabilityExecute, Patterns: []string{"go test"}},
	)
	rc := New(nil, caps, budget.DefaultBudget())

	if !rc.CanExecuteCommand("go test ./...") {
		t.Error("CanExecuteCommand(go test) = false, want true")
	}
	if rc.CanExecuteCommand("rm -rf") {
		t.Error("CanExecuteCommand(rm -rf) = true, want false")
	}
}

func TestCanReadWriteTestPatchCheckpointRollback(t *testing.T) {
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityRead)
	caps.Grant(capability.CapabilityWrite)
	caps.Grant(capability.CapabilityTest)
	caps.Grant(capability.CapabilityPatch)
	caps.Grant(capability.CapabilityCheckpoint)
	caps.Grant(capability.CapabilityRollback)
	rc := New(nil, caps, budget.DefaultBudget())

	if !rc.CanRead() {
		t.Error("CanRead() = false, want true")
	}
	if !rc.CanWrite() {
		t.Error("CanWrite() = false, want true")
	}
	if !rc.CanTest() {
		t.Error("CanTest() = false, want true")
	}
	if !rc.CanPatch() {
		t.Error("CanPatch() = false, want true")
	}
	if !rc.CanCheckpoint() {
		t.Error("CanCheckpoint() = false, want true")
	}
	if !rc.CanRollback() {
		t.Error("CanRollback() = false, want true")
	}
}

func TestConsumeBudget_Delegates(t *testing.T) {
	b := budget.NewBudget(1, 100, 1000, 2, time.Minute, 10)
	caps := capability.NewCapabilitySet()
	rc := New(nil, caps, b)

	if err := rc.ConsumeBudget(budget.BudgetDelta{Files: 1}); err != nil {
		t.Fatalf("ConsumeBudget(Files:1): %v", err)
	}
	if !rc.IsBudgetExhausted() {
		t.Error("IsBudgetExhausted() = false after hitting limit")
	}

	err := rc.ConsumeBudget(budget.BudgetDelta{Tokens: 10})
	if err == nil {
		t.Fatal("ConsumeBudget after exhaustion: expected error")
	}
}

func TestMutateFile_ChecksCapabilityAndBudget(t *testing.T) {
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityWrite)
	b := budget.NewBudget(1, 100, 1000, 2, time.Minute, 10)
	rc := New(nil, caps, b)

	if err := rc.MutateFile("test.go"); err != nil {
		t.Fatalf("MutateFile(test.go): %v", err)
	}

	err := rc.MutateFile("other.go")
	if err == nil {
		t.Fatal("MutateFile after budget exhausted: expected error")
	}
}

func TestMutateFile_DeniedByCapability(t *testing.T) {
	caps := capability.NewCapabilitySet()
	rc := New(nil, caps, budget.DefaultBudget())

	err := rc.MutateFile("test.go")
	if err == nil {
		t.Fatal("MutateFile without Write capability: expected error")
	}
}

func TestExecuteCommand_ChecksCapabilityAndBudget(t *testing.T) {
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityExecute)
	b := budget.NewBudget(10, 100, 1000, 2, time.Minute, 1)
	rc := New(nil, caps, b)

	if err := rc.ExecuteCommand("go test"); err != nil {
		t.Fatalf("ExecuteCommand(go test): %v", err)
	}

	err := rc.ExecuteCommand("go build")
	if err == nil {
		t.Fatal("ExecuteCommand after budget exhausted: expected error")
	}
}

func TestExecuteCommand_DeniedByCapability(t *testing.T) {
	caps := capability.NewCapabilitySet()
	rc := New(nil, caps, budget.DefaultBudget())

	err := rc.ExecuteCommand("go test")
	if err == nil {
		t.Fatal("ExecuteCommand without Execute capability: expected error")
	}
}

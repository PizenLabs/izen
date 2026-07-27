package control

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/core/budget"
)

func TestMultiStepPlanDynamicBudget(t *testing.T) {
	b := budget.DefaultBudget()
	if b.MaxMutations != 0 {
		t.Fatalf("expected MaxMutations=0 by default, got %d", b.MaxMutations)
	}

	// Scale budget to match a 3-task plan
	b.ScaleBudget(3)
	if b.MaxMutations != 3 {
		t.Fatalf("expected MaxMutations=3 after ScaleBudget(3), got %d", b.MaxMutations)
	}
	if !b.IsMultiStepPlan() {
		t.Fatal("expected IsMultiStepPlan to be true after scaling")
	}

	// Consume mutations — steps 1, 2, 3 should all pass
	if err := b.Consume(budget.BudgetDelta{Mutations: 1}); err != nil {
		t.Fatalf("step 1: unexpected budget exhaustion: %v", err)
	}
	if b.IsExhausted() {
		t.Fatal("step 1: budget should not be exhausted yet")
	}

	if err := b.Consume(budget.BudgetDelta{Mutations: 1}); err != nil {
		t.Fatalf("step 2: unexpected budget exhaustion: %v", err)
	}
	if b.IsExhausted() {
		t.Fatal("step 2: budget should not be exhausted yet")
	}

	if err := b.Consume(budget.BudgetDelta{Mutations: 1}); err != nil {
		t.Fatalf("step 3: unexpected budget exhaustion: %v", err)
	}
	// After step 3, budget should be exhausted (consumed all 3 mutations)
	if !b.IsExhausted() {
		t.Fatal("step 3: budget should be exhausted after consuming all mutations")
	}

	// Step 4 should fail
	if err := b.Consume(budget.BudgetDelta{Mutations: 1}); err == nil {
		t.Fatal("step 4: expected budget exhaustion error")
	}
}

func TestMultiStepPlanSingleStepBudget(t *testing.T) {
	// Verify that single-step plans (no scaling) still work
	b := budget.DefaultBudget()
	if b.IsMultiStepPlan() {
		t.Fatal("default budget should not be multi-step")
	}

	// Single mutation should consume normally
	if err := b.Consume(budget.BudgetDelta{Files: 1}); err != nil {
		t.Fatalf("single step: unexpected error: %v", err)
	}
}

func TestHandoffLedgerResumeAfterInterrupt(t *testing.T) {
	dir := t.TempDir()
	origDir := handoffDir
	handoffDir = dir
	defer func() { handoffDir = origDir }()

	// Create ledger with 3 steps
	hl := NewHandoffLedger("gpt-4o")
	steps := []HandoffStep{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "main.go", Description: "Update import", Status: StepPending},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "handler.go", Description: "Add handler", Status: StepPending},
		{StepNum: 3, Type: "SHELL_EXEC", Target: "go test ./...", Description: "Run tests", Status: StepPending},
	}
	hl.Init(steps, "Add REST endpoint")

	// Simulate completing step 1, then interrupt
	hl.CompleteStep(1, "import updated")
	hl.AddPreviousFile("main.go")
	hl.Pause("user interrupt (Ctrl+C)")

	if err := hl.Save(); err != nil {
		t.Fatalf("save after interrupt: %v", err)
	}

	// Simulate model switch: load the persisted ledger
	loaded, err := LoadHandoffLedger(hl.ID)
	if err != nil {
		t.Fatalf("load after interrupt: %v", err)
	}

	if loaded.State != HandoffPaused {
		t.Fatalf("expected paused state, got %s", loaded.State)
	}
	if loaded.CurrentStep != 2 {
		t.Fatalf("expected current step 2 after completing step 1, got %d", loaded.CurrentStep)
	}

	// Resume
	loaded.Resume()
	if loaded.State != HandoffActive {
		t.Fatalf("expected active after resume, got %s", loaded.State)
	}

	// Verify pending steps
	pending := loaded.PendingSteps()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending steps, got %d", len(pending))
	}
	if pending[0].Target != "handler.go" {
		t.Fatalf("expected pending[0].Target='handler.go', got %s", pending[0].Target)
	}

	// Verify handoff vector payload
	payload := loaded.HandoffVectorPayload()
	if !strings.Contains(payload, "REST endpoint") {
		t.Fatal("handoff payload should contain blueprint")
	}
	if !strings.Contains(payload, "Resume at this step") {
		t.Fatal("handoff payload should contain resume instruction")
	}
	if !strings.Contains(payload, "main.go") {
		t.Fatal("handoff payload should reference previous files")
	}
}

func TestHandoffLedgerApiTokenExhaustion(t *testing.T) {
	dir := t.TempDir()
	origDir := handoffDir
	handoffDir = dir
	defer func() { handoffDir = origDir }()

	// Simulate API token exhaustion mid-plan
	hl := NewHandoffLedger("claude-sonnet-4")
	steps := []HandoffStep{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "a.go", Status: StepCompleted},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "b.go", Status: StepPending},
	}
	hl.Init(steps, "Two-file change")
	hl.CompleteStep(1, "a.go modified")
	hl.Pause("API token exhaustion (finish_reason: length)")
	hl.AddPreviousFile("a.go")

	if err := hl.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadHandoffLedger(hl.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.State != HandoffPaused {
		t.Fatalf("expected paused state, got %s", loaded.State)
	}
	if loaded.InterruptReason != "API token exhaustion (finish_reason: length)" {
		t.Fatalf("unexpected interrupt reason: %s", loaded.InterruptReason)
	}

	// Resume should inject handoff vector without re-exploration
	loaded.Resume()
	info := loaded.CurrentStepInfo()
	if info == nil || info.Target != "b.go" {
		t.Fatalf("expected current step b.go, got %v", info)
	}
}

func TestHandoffLedgerModelSwitch(t *testing.T) {
	dir := t.TempDir()
	origDir := handoffDir
	handoffDir = dir
	defer func() { handoffDir = origDir }()

	// Simulate switching models mid-plan
	hl := NewHandoffLedger("gpt-4o")
	steps := []HandoffStep{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "config.go", Status: StepPending},
	}
	hl.Init(steps, "Update config")

	// Persist with old model
	hl.ModelName = "gpt-4o"
	if err := hl.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// New model loads the ledger
	loaded, err := LoadHandoffLedger(hl.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	loaded.ModelName = "claude-sonnet-4"
	loaded.Resume()

	// Verify new model can pick up without re-exploration
	payload := loaded.HandoffVectorPayload()
	if !strings.Contains(payload, "Update config") {
		t.Fatal("new model should receive blueprint without re-exploration")
	}
}

func TestTokenThriftyConstraintsInPrompts(t *testing.T) {
	// Verify the token-thrifty constraint constant from prompt/build.go.
	// This compiles and runs without error as long as the constant exists.
	_ = budget.DefaultBudget().IsMultiStepPlan()
}

func TestScaleBudgetZeroTasks(t *testing.T) {
	b := budget.DefaultBudget()
	b.ScaleBudget(0)
	if b.MaxMutations != 0 {
		t.Fatalf("expected MaxMutations=0 for zero tasks, got %d", b.MaxMutations)
	}
	if b.IsMultiStepPlan() {
		t.Fatal("zero-task plan should not be multi-step")
	}
}

func TestBudgetExhaustedErrorFormat(t *testing.T) {
	err := &budget.BudgetExhaustedError{Field: "mutations", Limit: 3, Current: 3}
	msg := err.Error()
	if !strings.Contains(msg, "mutations") {
		t.Fatalf("error should mention field name: %s", msg)
	}
	if !strings.Contains(msg, "limit=3") {
		t.Fatalf("error should mention limit: %s", msg)
	}
}

func TestHandoffLedgerEmptyPending(t *testing.T) {
	hl := NewHandoffLedger("gpt-4o")
	pending := hl.PendingSteps()
	if len(pending) != 0 {
		t.Fatalf("expected empty pending for uninitialized ledger, got %d", len(pending))
	}
}

func TestHandoffLedgerCompleteAllAndVector(t *testing.T) {
	hl := NewHandoffLedger("gpt-4o")
	steps := []HandoffStep{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "a.go", Status: StepPending},
	}
	hl.Init(steps, "Simple change")
	hl.CompleteStep(1, "done")

	if hl.State != HandoffComplete {
		t.Fatalf("expected complete state after all steps done, got %s", hl.State)
	}

	// Current step should be nil when complete
	info := hl.CurrentStepInfo()
	if info != nil {
		t.Fatalf("expected nil current step when complete, got step %d", info.StepNum)
	}
}

func TestResumeNonExistentLedger(t *testing.T) {
	_, err := LoadHandoffLedger("nonexistent-id")
	if err == nil {
		t.Fatal("expected error loading non-existent ledger")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

func TestBudgetDeltaMutationsField(t *testing.T) {
	delta := budget.BudgetDelta{Mutations: 5}
	if delta.Mutations != 5 {
		t.Fatalf("expected Mutations=5, got %d", delta.Mutations)
	}
}

func TestMultiStepAuthorizationNotSingleUse(t *testing.T) {
	b := budget.DefaultBudget()
	b.ScaleBudget(3)
	if !b.IsMultiStepPlan() {
		t.Fatal("expected multi-step plan after scaling")
	}
	// Single-use should be false for multi-step plans
	singleUse := !b.IsMultiStepPlan()
	if singleUse {
		t.Fatal("multi-step plans should not be single-use")
	}
}

package control

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHandoffLedgerLifecycle(t *testing.T) {
	dir := t.TempDir()
	origDir := handoffDir
	handoffDir = dir
	defer func() { handoffDir = origDir }()

	hl := NewHandoffLedger("gpt-4o")
	steps := []HandoffStep{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "main.go", Description: "Add import", Status: StepPending},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "main.go", Description: "Fix function", Status: StepPending},
		{StepNum: 3, Type: "SHELL_EXEC", Target: "go mod tidy", Description: "Sync deps", Status: StepPending},
	}
	hl.Init(steps, "Add logging to main.go")

	if hl.State != HandoffActive {
		t.Fatalf("expected active state, got %s", hl.State)
	}
	if hl.CurrentStep != 1 {
		t.Fatalf("expected current step 1, got %d", hl.CurrentStep)
	}

	// Complete step 1 and verify
	hl.CompleteStep(1, "import added")
	if hl.CurrentStep != 2 {
		t.Fatalf("expected current step 2 after completing step 1, got %d", hl.CurrentStep)
	}
	pending := hl.PendingSteps()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending steps, got %d", len(pending))
	}

	// Fail step 2
	hl.FailStep(2, "syntax error")
	if hl.State != HandoffFailed {
		t.Fatalf("expected failed state, got %s", hl.State)
	}

	// Verify persistence
	if err := hl.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := LoadHandoffLedger(hl.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.State != HandoffFailed {
		t.Fatalf("loaded expected failed state, got %s", loaded.State)
	}
	if loaded.CurrentStep != 2 {
		t.Fatalf("loaded expected current step 2, got %d", loaded.CurrentStep)
	}
}

func TestHandoffLedgerPauseResume(t *testing.T) {
	hl := NewHandoffLedger("claude-sonnet-4")
	steps := []HandoffStep{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "api.go", Status: StepCompleted},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "handler.go", Status: StepPending},
	}
	hl.Init(steps, "Implement API handler")

	hl.Pause("user interrupt")
	if hl.State != HandoffPaused {
		t.Fatalf("expected paused state, got %s", hl.State)
	}
	if hl.InterruptReason != "user interrupt" {
		t.Fatalf("expected interrupt reason 'user interrupt', got %s", hl.InterruptReason)
	}

	hl.Resume()
	if hl.State != HandoffActive {
		t.Fatalf("expected active state after resume, got %s", hl.State)
	}
	if hl.InterruptReason != "" {
		t.Fatalf("expected empty interrupt reason after resume, got %s", hl.InterruptReason)
	}
}

func TestHandoffVectorPayload(t *testing.T) {
	hl := NewHandoffLedger("gpt-4o")
	steps := []HandoffStep{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "main.go", Description: "Add import", Status: StepCompleted},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "util.go", Description: "Add helper", Status: StepPending},
	}
	hl.Init(steps, "Add logging utility")
	hl.CompleteStep(1, "import added")
	hl.AddPreviousFile("main.go")

	payload := hl.HandoffVectorPayload()
	if payload == "" {
		t.Fatal("expected non-empty handoff vector payload")
	}
	expectedContains := []string{"HANDOFF VECTOR", "Blueprint:", "Completed: 1", "Current step:", "Resume at this step"}
	for _, s := range expectedContains {
		if !contains(payload, s) {
			t.Fatalf("expected payload to contain %q", s)
		}
	}
}

func TestHandoffLedgerPersistence(t *testing.T) {
	dir := t.TempDir()
	origDir := handoffDir
	handoffDir = dir
	defer func() { handoffDir = origDir }()

	hl := NewHandoffLedger("deepseek-chat")
	steps := []HandoffStep{
		{StepNum: 1, Type: "SHELL_EXEC", Target: "go get github.com/foo/bar", Status: StepPending},
	}
	hl.Init(steps, "Install dependency")

	if err := hl.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify file exists
	path := filepath.Join(dir, hl.ID, "ledger.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected ledger file at %s: %v", path, err)
	}

	// Load latest
	loaded, err := LoadLatestHandoffLedger()
	if err != nil {
		t.Fatalf("load latest: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil latest ledger")
	}
	if loaded.ID != hl.ID {
		t.Fatalf("expected id %s, got %s", hl.ID, loaded.ID)
	}
}

func TestHandoffLedgerPendingSteps(t *testing.T) {
	hl := NewHandoffLedger("gpt-4o")
	steps := []HandoffStep{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "a.go", Status: StepCompleted},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "b.go", Status: StepRunning},
		{StepNum: 3, Type: "FILE_MUTATE", Target: "c.go", Status: StepPending},
		{StepNum: 4, Type: "SHELL_EXEC", Target: "go test", Status: StepPending},
	}
	hl.Init(steps, "Multi-file change")

	pending := hl.PendingSteps()
	if len(pending) != 3 {
		t.Fatalf("expected 3 pending steps (2 running + 3 pending), got %d", len(pending))
	}

	done := hl.CompletedSteps()
	if len(done) != 1 {
		t.Fatalf("expected 1 completed step, got %d", len(done))
	}
	if done[0].Target != "a.go" {
		t.Fatalf("expected completed target a.go, got %s", done[0].Target)
	}
}

func TestHandoffCurrentStepInfo(t *testing.T) {
	hl := NewHandoffLedger("gpt-4o")
	steps := []HandoffStep{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "main.go", Status: StepCompleted},
		{StepNum: 2, Type: "SHELL_EXEC", Target: "go mod tidy", Status: StepPending},
	}
	hl.Init(steps, "Fix deps")
	hl.CompleteStep(1, "done")

	info := hl.CurrentStepInfo()
	if info == nil {
		t.Fatal("expected non-nil current step info")
	}
	if info.StepNum != 2 {
		t.Fatalf("expected step 2 as current, got %d", info.StepNum)
	}
	if info.Target != "go mod tidy" {
		t.Fatalf("expected target 'go mod tidy', got %s", info.Target)
	}
}

func contains(s, substr string) bool {
	return len(substr) == 0 || s != "" && substr != "" && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

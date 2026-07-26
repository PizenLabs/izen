package budget

import (
	"testing"
	"time"
)

func TestNewBudget_Defaults(t *testing.T) {
	b := DefaultBudget()
	if b.MaxFiles != 10 {
		t.Errorf("MaxFiles = %d, want 10", b.MaxFiles)
	}
	if b.MaxDiffLines != 500 {
		t.Errorf("MaxDiffLines = %d, want 500", b.MaxDiffLines)
	}
	if b.MaxTokens != 8000 {
		t.Errorf("MaxTokens = %d, want 8000", b.MaxTokens)
	}
	if b.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", b.MaxAttempts)
	}
	if b.MaxShellCommands != 20 {
		t.Errorf("MaxShellCommands = %d, want 20", b.MaxShellCommands)
	}
	if b.IsExhausted() {
		t.Error("IsExhausted() = true for fresh budget")
	}
}

func TestConsume_Files_WithinBudget(t *testing.T) {
	b := NewBudget(5, 100, 1000, 2, time.Minute, 10)
	if err := b.Consume(BudgetDelta{Files: 3}); err != nil {
		t.Fatalf("Consume(Files:3): %v", err)
	}
	if b.IsExhausted() {
		t.Error("IsExhausted() = true after consuming within budget")
	}
	if b.RemainingFiles() != 2 {
		t.Errorf("RemainingFiles = %d, want 2", b.RemainingFiles())
	}
}

func TestConsume_Files_Exhausted(t *testing.T) {
	b := NewBudget(3, 100, 1000, 2, time.Minute, 10)
	if err := b.Consume(BudgetDelta{Files: 3}); err != nil {
		t.Fatalf("Consume(Files:3): %v", err)
	}
	if !b.IsExhausted() {
		t.Error("IsExhausted() = false after hitting limit")
	}
	err := b.Consume(BudgetDelta{Files: 1})
	if err == nil {
		t.Fatal("Consume(Files:1) after exhaustion: expected error")
	}
}

func TestConsume_DiffLines(t *testing.T) {
	b := NewBudget(10, 50, 1000, 2, time.Minute, 10)
	if err := b.Consume(BudgetDelta{DiffLines: 50}); err != nil {
		t.Fatalf("Consume(DiffLines:50): %v", err)
	}
	if b.RemainingDiffLines() != 0 {
		t.Errorf("RemainingDiffLines = %d, want 0", b.RemainingDiffLines())
	}
	err := b.Consume(BudgetDelta{DiffLines: 1})
	if err == nil {
		t.Fatal("Consume(DiffLines:1) after limit: expected error")
	}
}

func TestConsume_Tokens(t *testing.T) {
	b := NewBudget(10, 100, 2000, 2, time.Minute, 10)
	if err := b.Consume(BudgetDelta{Tokens: 1500}); err != nil {
		t.Fatalf("Consume(Tokens:1500): %v", err)
	}
	if b.RemainingTokens() != 500 {
		t.Errorf("RemainingTokens = %d, want 500", b.RemainingTokens())
	}
	if err := b.Consume(BudgetDelta{Tokens: 500}); err != nil {
		t.Fatalf("Consume(Tokens:500): %v", err)
	}
	err := b.Consume(BudgetDelta{Tokens: 1})
	if err == nil {
		t.Fatal("Consume(Tokens:1) after limit: expected error")
	}
}

func TestConsume_Attempts(t *testing.T) {
	b := NewBudget(10, 100, 1000, 3, time.Minute, 10)
	if err := b.Consume(BudgetDelta{Attempts: 2}); err != nil {
		t.Fatalf("Consume(Attempts:2): %v", err)
	}
	if b.RemainingAttempts() != 1 {
		t.Errorf("RemainingAttempts = %d, want 1", b.RemainingAttempts())
	}
	if err := b.Consume(BudgetDelta{Attempts: 1}); err != nil {
		t.Fatalf("Consume(Attempts:1): %v", err)
	}
	err := b.Consume(BudgetDelta{Attempts: 1})
	if err == nil {
		t.Fatal("Consume(Attempts:1) after limit: expected error")
	}
}

func TestConsume_ShellCommands(t *testing.T) {
	b := NewBudget(10, 100, 1000, 2, time.Minute, 5)
	for i := 0; i < 5; i++ {
		if err := b.Consume(BudgetDelta{ShellCmds: 1}); err != nil {
			t.Fatalf("Consume(ShellCmds:1) iteration %d: %v", i, err)
		}
	}
	if b.RemainingShellCommands() != 0 {
		t.Errorf("RemainingShellCommands = %d, want 0", b.RemainingShellCommands())
	}
	err := b.Consume(BudgetDelta{ShellCmds: 1})
	if err == nil {
		t.Fatal("Consume(ShellCmds:1) after limit: expected error")
	}
}

func TestConsume_MultipleDeltasInOneCall(t *testing.T) {
	b := NewBudget(5, 100, 1000, 3, time.Minute, 10)
	if err := b.Consume(BudgetDelta{Files: 1, DiffLines: 30, Tokens: 500}); err != nil {
		t.Fatalf("Consume(multi): %v", err)
	}
	if b.RemainingFiles() != 4 {
		t.Errorf("RemainingFiles = %d, want 4", b.RemainingFiles())
	}
	if b.RemainingDiffLines() != 70 {
		t.Errorf("RemainingDiffLines = %d, want 70", b.RemainingDiffLines())
	}
	if b.RemainingTokens() != 500 {
		t.Errorf("RemainingTokens = %d, want 500", b.RemainingTokens())
	}
}

func TestConsume_ExecutionTime(t *testing.T) {
	b := NewBudget(10, 100, 1000, 2, 50*time.Millisecond, 10)
	time.Sleep(60 * time.Millisecond)
	err := b.Consume(BudgetDelta{})
	if err == nil {
		t.Fatal("Consume after execution time limit: expected error")
	}
	if !b.IsExhausted() {
		t.Error("IsExhausted() = false after time exhaustion")
	}
}

func TestConsume_ExhaustedBudgetRejectsAll(t *testing.T) {
	b := NewBudget(1, 100, 1000, 2, time.Minute, 10)
	_ = b.Consume(BudgetDelta{Files: 1})
	// Budget is now exhausted.
	err := b.Consume(BudgetDelta{Tokens: 10})
	if err == nil {
		t.Fatal("Consume on exhausted budget: expected error")
	}
}

func TestReset(t *testing.T) {
	b := NewBudget(3, 100, 1000, 2, time.Minute, 10)
	_ = b.Consume(BudgetDelta{Files: 3})
	if !b.IsExhausted() {
		t.Fatal("budget should be exhausted")
	}
	b.Reset()
	if b.IsExhausted() {
		t.Error("IsExhausted() = true after Reset")
	}
	if b.RemainingFiles() != 3 {
		t.Errorf("RemainingFiles = %d, want 3 after Reset", b.RemainingFiles())
	}
}

func TestZeroLimits_Unlimited(t *testing.T) {
	b := NewBudget(0, 0, 0, 0, 0, 0)
	if err := b.Consume(BudgetDelta{Files: 1000}); err != nil {
		t.Fatalf("Consume with zero limits: %v", err)
	}
	if b.IsExhausted() {
		t.Error("IsExhausted() = true with zero limits (unlimited)")
	}
	if b.RemainingFiles() != 0 {
		t.Errorf("RemainingFiles = %d, want 0 (unlimited signals 0)", b.RemainingFiles())
	}
}

func TestBudgetExhaustedError_Error(t *testing.T) {
	e := &BudgetExhaustedError{Field: "files", Limit: 5, Current: 5}
	msg := e.Error()
	if msg == "" {
		t.Fatal("Error() returned empty string")
	}
}

func TestDefaultMicroBudget(t *testing.T) {
	mb := DefaultMicroBudget()
	if mb.MaxFiles != 2 {
		t.Errorf("MaxFiles = %d, want 2", mb.MaxFiles)
	}
	if mb.MaxDiffLines != 50 {
		t.Errorf("MaxDiffLines = %d, want 50", mb.MaxDiffLines)
	}
	if mb.MaxTokens != 2000 {
		t.Errorf("MaxTokens = %d, want 2000", mb.MaxTokens)
	}
	if mb.MaxAttempts != 1 {
		t.Errorf("MaxAttempts = %d, want 1", mb.MaxAttempts)
	}
	if !mb.CheckpointRequired {
		t.Error("CheckpointRequired = false, want true")
	}
}

func TestIsWithinMicroBudget_ExactFit(t *testing.T) {
	mb := DefaultMicroBudget()
	delta := BudgetDelta{
		Files:     2,
		DiffLines: 50,
		Tokens:    2000,
		Attempts:  1,
	}
	if !mb.IsWithinMicroBudget(delta, true) {
		t.Error("IsWithinMicroBudget(exact fit, hasCheckpoint=true) = false, want true")
	}
}

func TestIsWithinMicroBudget_ExceedsFiles(t *testing.T) {
	mb := DefaultMicroBudget()
	if mb.IsWithinMicroBudget(BudgetDelta{Files: 3}, true) {
		t.Error("IsWithinMicroBudget(Files:3) = true, want false")
	}
}

func TestIsWithinMicroBudget_ExceedsDiffLines(t *testing.T) {
	mb := DefaultMicroBudget()
	if mb.IsWithinMicroBudget(BudgetDelta{DiffLines: 51}, true) {
		t.Error("IsWithinMicroBudget(DiffLines:51) = true, want false")
	}
}

func TestIsWithinMicroBudget_ExceedsTokens(t *testing.T) {
	mb := DefaultMicroBudget()
	if mb.IsWithinMicroBudget(BudgetDelta{Tokens: 2001}, true) {
		t.Error("IsWithinMicroBudget(Tokens:2001) = true, want false")
	}
}

func TestIsWithinMicroBudget_ExceedsAttempts(t *testing.T) {
	mb := DefaultMicroBudget()
	if mb.IsWithinMicroBudget(BudgetDelta{Attempts: 2}, true) {
		t.Error("IsWithinMicroBudget(Attempts:2) = true, want false")
	}
}

func TestIsWithinMicroBudget_NoCheckpoint(t *testing.T) {
	mb := DefaultMicroBudget()
	if mb.IsWithinMicroBudget(BudgetDelta{Files: 1, DiffLines: 10, Tokens: 100, Attempts: 1}, false) {
		t.Error("IsWithinMicroBudget(hasCheckpoint=false) = true, want false")
	}
}

func TestIsWithinMicroBudget_CheckpointNotRequired(t *testing.T) {
	mb := MicroBudget{
		MaxFiles:           2,
		MaxDiffLines:       50,
		MaxTokens:          2000,
		MaxAttempts:        1,
		CheckpointRequired: false,
	}
	if !mb.IsWithinMicroBudget(BudgetDelta{Files: 1}, false) {
		t.Error("IsWithinMicroBudget(checkpoint not required, no checkpoint) = false, want true")
	}
}

func TestIsWithinMicroBudget_ZeroDelta(t *testing.T) {
	mb := DefaultMicroBudget()
	if !mb.IsWithinMicroBudget(BudgetDelta{}, true) {
		t.Error("IsWithinMicroBudget(zero delta, hasCheckpoint) = false, want true")
	}
}

func TestRemaining_UnlimitedReturnsZero(t *testing.T) {
	b := NewBudget(0, 0, 0, 0, 0, 0)
	if b.RemainingFiles() != 0 {
		t.Errorf("RemainingFiles with zero limit = %d, want 0", b.RemainingFiles())
	}
	if b.RemainingDiffLines() != 0 {
		t.Errorf("RemainingDiffLines with zero limit = %d, want 0", b.RemainingDiffLines())
	}
	if b.RemainingTokens() != 0 {
		t.Errorf("RemainingTokens with zero limit = %d, want 0", b.RemainingTokens())
	}
	if b.RemainingAttempts() != 0 {
		t.Errorf("RemainingAttempts with zero limit = %d, want 0", b.RemainingAttempts())
	}
	if b.RemainingShellCommands() != 0 {
		t.Errorf("RemainingShellCommands with zero limit = %d, want 0", b.RemainingShellCommands())
	}
}

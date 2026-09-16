package executor

import (
	"context"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/domain"
)

// TestResourceBudget_OrthogonalLimits exceeds StepTokens on turn 1 while
// TaskTokens remains valid. The turn yields a PARTIAL-equivalent (step
// overflow without task exhaustion) while aggregate task state persists
// across ResetStep.
func TestResourceBudget_OrthogonalLimits(t *testing.T) {
	t.Parallel()

	budget := domain.ResourceBudget{
		StepTokens: 100,
		TaskTokens: 1000,
		MaxFiles:   5,
		MaxLatency: time.Minute,
	}
	tr := NewBudgetTracker(budget)

	// Turn 1: exceed the single-turn ceiling while the aggregate stays valid.
	if exceeded := tr.RecordStepTokens(150); !exceeded {
		t.Fatal("RecordStepTokens(150) with StepTokens=100 must report step breach")
	}
	if !tr.StepOverflowed() {
		t.Error("StepOverflowed must be true after a step breach")
	}
	if tr.TaskOverflowed() {
		t.Error("TaskOverflowed must stay false when TaskTokens=1000 is still valid (PARTIAL turn, task persists)")
	}
	if tr.Overflowed() && tr.TaskOverflowed() {
		t.Error("generic overflow must not imply task exhaustion on a step-only breach")
	}
	usage := tr.Usage()
	if usage.StepTokens != 150 {
		t.Errorf("StepTokens usage = %d, want 150", usage.StepTokens)
	}
	if usage.TaskTokens != 150 {
		t.Errorf("TaskTokens usage = %d, want 150 (aggregate persists)", usage.TaskTokens)
	}

	// Next turn: per-turn state resets, aggregate task state persists.
	tr.ResetStep()
	if tr.StepOverflowed() {
		t.Error("ResetStep must clear the step-overflow flag")
	}
	if tr.TaskOverflowed() {
		t.Error("ResetStep must preserve task-overflow=false")
	}
	if got := tr.Usage(); got.StepTokens != 0 {
		t.Errorf("StepTokens after ResetStep = %d, want 0", got.StepTokens)
	}
	if got := tr.Usage(); got.TaskTokens != 150 {
		t.Errorf("TaskTokens after ResetStep = %d, want 150 (task state persists)", got.TaskTokens)
	}

	// Orthogonal axes: MaxFiles and MaxLatency enforce independently.
	if tr.RecordFiles(2) {
		t.Error("RecordFiles(2) with MaxFiles=5 must not overflow")
	}
	if tr.RecordFiles(4) {
		// 2+4=6 > 5 → overflow expected on this axis alone.
	} else {
		t.Error("RecordFiles breaching MaxFiles must report overflow")
	}
	if !tr.CheckExceeded() {
		t.Error("CheckExceeded must be true after a MaxFiles breach")
	}

	// MaxLatency is wall-clock only: a fresh tracker with a past start
	// exceeds, while token rates never derive a timeout.
	fast := NewBudgetTracker(domain.ResourceBudget{MaxLatency: time.Nanosecond})
	time.Sleep(2 * time.Millisecond)
	if !fast.CheckExceeded() {
		t.Error("MaxLatency must be enforced via wall-clock elapsed time")
	}
	ctx, cancel := fast.WrapContext(context.Background())
	defer cancel()
	select {
	case <-ctx.Done():
		// Wall-clock timeout fired.
	default:
		t.Error("WrapContext with expired MaxLatency must yield a done context")
	}
}

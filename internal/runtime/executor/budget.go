package executor

import (
	"context"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/core/domain"
)

// BudgetTracker tracks memory usage, shell process wall-clock time, and
// workspace patch diff sizes against domain.ResourceBudget.
// It automatically cancels execution context upon budget overflow.
//
// Two-tier token model: StepTokens bounds a single turn (PARTIAL on breach,
// task persists); TaskTokens bounds the aggregate task (exhaustion ends the
// task). MaxLatency is enforced exclusively via wall-clock context timeouts
// (WrapContext + Elapsed/CheckExceeded using time.Since) — never derived
// from token rates.
type BudgetTracker struct {
	mu           sync.Mutex
	budget       domain.ResourceBudget
	usage        domain.BudgetUsage
	start        time.Time
	cancel       context.CancelFunc
	overflow     bool
	stepOverflow bool
	taskOverflow bool
}

// NewBudgetTracker creates a tracker for the given budget.
func NewBudgetTracker(budget domain.ResourceBudget) *BudgetTracker {
	return &BudgetTracker{budget: budget, start: time.Now()}
}

// WrapContext returns a child context that is cancelled when the budget
// overflows or the MaxLatency timeout expires. The caller must call the
// returned cancel func when execution completes.
//
// MaxLatency is enforced SOLELY as a wall-clock context timeout via
// context.WithTimeout. It is never derived from token rates, request counts,
// or any throughput heuristic.
func (b *BudgetTracker) WrapContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if b == nil {
		return ctx, func() {}
	}
	// MaxLatency timeout
	if b.budget.MaxLatency > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.budget.MaxLatency)
		b.mu.Lock()
		// combine cancels: store outer cancel so Record can cancel on overflow
		prev := b.cancel
		b.cancel = func() {
			cancel()
			if prev != nil {
				prev()
			}
		}
		b.mu.Unlock()
		return ctx, func() {
			cancel()
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	prev := b.cancel
	b.cancel = func() {
		cancel()
		if prev != nil {
			prev()
		}
	}
	b.mu.Unlock()
	return ctx, cancel
}

// RecordFiles records a file mutation.
func (b *BudgetTracker) RecordFiles(n int) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage.Files += n
	if b.budget.MaxFiles > 0 && b.usage.Files > b.budget.MaxFiles {
		b.overflow = true
		if b.cancel != nil {
			b.cancel()
		}
		return true
	}
	return false
}

// RecordDiffLines records diff lines produced.
func (b *BudgetTracker) RecordDiffLines(n int) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage.DiffLines += n
	if b.budget.MaxDiffLines > 0 && b.usage.DiffLines > b.budget.MaxDiffLines {
		b.overflow = true
		if b.cancel != nil {
			b.cancel()
		}
		return true
	}
	return false
}

// RecordTokens records token usage.
func (b *BudgetTracker) RecordTokens(input, output int) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage.InputTokens += input
	b.usage.OutputTokens += output
	total := b.usage.InputTokens + b.usage.OutputTokens
	maxTotal := b.budget.MaxInputTokens + b.budget.MaxOutputTokens
	if maxTotal > 0 && total > maxTotal {
		b.overflow = true
		if b.cancel != nil {
			b.cancel()
		}
		return true
	}
	if b.budget.MaxRequests > 0 && b.usage.Requests > b.budget.MaxRequests {
		b.overflow = true
		if b.cancel != nil {
			b.cancel()
		}
		return true
	}
	return false
}

// RecordShell records a shell invocation.
func (b *BudgetTracker) RecordShell() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage.ShellCmds++
	if b.budget.MaxShellCommands > 0 && b.usage.ShellCmds > b.budget.MaxShellCommands {
		b.overflow = true
		if b.cancel != nil {
			b.cancel()
		}
		return true
	}
	return false
}

// RecordStepTokens records n tokens against the single-turn StepTokens limit
// and the aggregate TaskTokens limit. A step breach sets the step-overflow
// flag (PARTIAL turn; task state persists) WITHOUT setting the task-overflow
// flag; a task breach sets both and cancels execution. ResetStep clears the
// per-turn counter for the next turn while preserving aggregate task usage.
func (b *BudgetTracker) RecordStepTokens(n int) (stepExceeded bool) {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage.StepTokens += n
	b.usage.TaskTokens += n
	if b.budget.StepTokens > 0 && b.usage.StepTokens > b.budget.StepTokens {
		b.stepOverflow = true
		b.overflow = true
		stepExceeded = true
		// Step breach does NOT cancel the task context: the turn yields
		// PARTIAL while the task persists. Only a task-level breach cancels.
	}
	if b.budget.TaskTokens > 0 && b.usage.TaskTokens > b.budget.TaskTokens {
		b.taskOverflow = true
		b.overflow = true
		if b.cancel != nil {
			b.cancel()
		}
	}
	return stepExceeded
}

// RecordTaskTokens records n tokens directly against the aggregate TaskTokens
// limit (e.g. for non-step accounting). It returns true on task exhaustion.
func (b *BudgetTracker) RecordTaskTokens(n int) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage.TaskTokens += n
	if b.budget.TaskTokens > 0 && b.usage.TaskTokens > b.budget.TaskTokens {
		b.taskOverflow = true
		b.overflow = true
		if b.cancel != nil {
			b.cancel()
		}
		return true
	}
	return false
}

// ResetStep clears the per-turn StepTokens counter and step-overflow flag for
// the next turn. Aggregate TaskTokens usage and task-overflow persist.
func (b *BudgetTracker) ResetStep() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage.StepTokens = 0
	b.stepOverflow = false
}

// StepOverflowed reports whether the current turn breached StepTokens.
func (b *BudgetTracker) StepOverflowed() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stepOverflow
}

// TaskOverflowed reports whether aggregate usage breached TaskTokens.
func (b *BudgetTracker) TaskOverflowed() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.taskOverflow
}

// Usage returns a snapshot of current usage.
func (b *BudgetTracker) Usage() domain.BudgetUsage {
	if b == nil {
		return domain.BudgetUsage{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usage
}

// Overflowed reports whether any budget was exceeded.
func (b *BudgetTracker) Overflowed() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflow
}

// Elapsed returns wall-clock time since tracker creation.
func (b *BudgetTracker) Elapsed() time.Duration {
	if b == nil {
		return 0
	}
	return time.Since(b.start)
}

// CheckExceeded reports whether the current usage already exceeds the budget.
// MaxLatency is compared against wall-clock elapsed time only.
func (b *BudgetTracker) CheckExceeded() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.budget.MaxFiles > 0 && b.usage.Files > b.budget.MaxFiles {
		return true
	}
	if b.budget.MaxDiffLines > 0 && b.usage.DiffLines > b.budget.MaxDiffLines {
		return true
	}
	if b.budget.MaxShellCommands > 0 && b.usage.ShellCmds > b.budget.MaxShellCommands {
		return true
	}
	if b.budget.StepTokens > 0 && b.usage.StepTokens > b.budget.StepTokens {
		return true
	}
	if b.budget.TaskTokens > 0 && b.usage.TaskTokens > b.budget.TaskTokens {
		return true
	}
	if b.budget.MaxLatency > 0 && time.Since(b.start) > b.budget.MaxLatency {
		return true
	}
	return b.overflow
}

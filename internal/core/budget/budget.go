package budget

import (
	"fmt"
	"time"
)

// BudgetExhaustedError signals that a mutation budget has been exceeded and
// execution must stop immediately.
type BudgetExhaustedError struct {
	Field   string
	Limit   int64
	Current int64
}

func (e *BudgetExhaustedError) Error() string {
	return fmt.Sprintf(
		"mutation budget exhausted: %s limit=%d current=%d",
		e.Field, e.Limit, e.Current,
	)
}

// BudgetDelta represents a consumption delta against one or more budget
// counters.
type BudgetDelta struct {
	Files     int
	DiffLines int
	Tokens    int
	Attempts  int
	ShellCmds int
	Mutations int
}

// MutationBudget tracks resource consumption for a mutation operation and
// enforces hard limits.
type MutationBudget struct {
	MaxFiles         int
	MaxDiffLines     int
	MaxTokens        int
	MaxAttempts      int
	MaxExecutionTime time.Duration
	MaxShellCommands int
	MaxMutations     int

	currentFiles     int
	currentDiffLines int
	currentTokens    int
	currentAttempts  int
	currentShellCmds int
	currentMutations int
	startTime        time.Time
	exhausted        bool
}

// NewMutationBudget creates a MutationBudget with the given limits.
func NewBudget(maxFiles, maxDiffLines, maxTokens, maxAttempts int,
	maxExecTime time.Duration, maxShellCmds int) *MutationBudget {

	return &MutationBudget{
		MaxFiles:         maxFiles,
		MaxDiffLines:     maxDiffLines,
		MaxTokens:        maxTokens,
		MaxAttempts:      maxAttempts,
		MaxExecutionTime: maxExecTime,
		MaxShellCommands: maxShellCmds,
		MaxMutations:     0,
		startTime:        time.Now(),
	}
}

// DefaultMutationBudget returns a budget with sensible default limits.
func DefaultBudget() *MutationBudget {
	return NewBudget(
		10,            // MaxFiles
		500,           // MaxDiffLines
		8000,          // MaxTokens
		3,             // MaxAttempts
		5*time.Minute, // MaxExecutionTime
		20,            // MaxShellCommands
	)
}

// IsMultiStepPlan reports whether the budget has been scaled for a multi-step
// plan (MaxMutations > 0). When true, the authorization token should allow
// sequential steps without being consumed as single-use.
func (b *MutationBudget) IsMultiStepPlan() bool {
	return b != nil && b.MaxMutations > 0
}

// ScaleBudget adjusts MaxMutations to match the total number of plan tasks.
// This allows multi-step plans to proceed sequentially without exhausting
// the budget after a single task.
func (b *MutationBudget) ScaleBudget(taskCount int) {
	if b != nil && taskCount > 0 {
		b.MaxMutations = taskCount
	}
}

// Consume attempts to apply the given delta. If any counter would exceed its
// limit, the budget is marked exhausted and a BudgetExhaustedError is returned.
func (b *MutationBudget) Consume(delta BudgetDelta) error {
	if b.exhausted {
		return &BudgetExhaustedError{
			Field: "budget", Limit: 0, Current: 0,
		}
	}

	if err := b.consumeFile(delta.Files); err != nil {
		return err
	}
	if err := b.consumeDiffLines(delta.DiffLines); err != nil {
		return err
	}
	if err := b.consumeTokens(delta.Tokens); err != nil {
		return err
	}
	if err := b.consumeAttempts(delta.Attempts); err != nil {
		return err
	}
	if err := b.consumeShellCmds(delta.ShellCmds); err != nil {
		return err
	}
	if err := b.consumeMutations(delta.Mutations); err != nil {
		return err
	}

	if b.MaxExecutionTime > 0 && time.Since(b.startTime) > b.MaxExecutionTime {
		b.exhausted = true
		return &BudgetExhaustedError{
			Field:   "execution_time",
			Limit:   int64(b.MaxExecutionTime.Seconds()),
			Current: int64(time.Since(b.startTime).Seconds()),
		}
	}

	return nil
}

func (b *MutationBudget) consumeMutations(n int) error {
	if n <= 0 || b.MaxMutations <= 0 {
		return nil
	}
	next := b.currentMutations + n
	if next > b.MaxMutations {
		b.exhausted = true
		return &BudgetExhaustedError{Field: "mutations", Limit: int64(b.MaxMutations), Current: int64(b.currentMutations)}
	}
	b.currentMutations = next
	if b.currentMutations >= b.MaxMutations {
		b.exhausted = true
	}
	return nil
}

func (b *MutationBudget) consumeFile(n int) error {
	if n <= 0 {
		return nil
	}
	next := b.currentFiles + n
	if b.MaxFiles > 0 && next > b.MaxFiles {
		b.exhausted = true
		return &BudgetExhaustedError{Field: "files", Limit: int64(b.MaxFiles), Current: int64(b.currentFiles)}
	}
	b.currentFiles = next
	if b.MaxFiles > 0 && b.currentFiles >= b.MaxFiles {
		b.exhausted = true
	}
	return nil
}

func (b *MutationBudget) consumeDiffLines(n int) error {
	if n <= 0 {
		return nil
	}
	next := b.currentDiffLines + n
	if b.MaxDiffLines > 0 && next > b.MaxDiffLines {
		b.exhausted = true
		return &BudgetExhaustedError{Field: "diff_lines", Limit: int64(b.MaxDiffLines), Current: int64(b.currentDiffLines)}
	}
	b.currentDiffLines = next
	if b.MaxDiffLines > 0 && b.currentDiffLines >= b.MaxDiffLines {
		b.exhausted = true
	}
	return nil
}

func (b *MutationBudget) consumeTokens(n int) error {
	if n <= 0 {
		return nil
	}
	next := b.currentTokens + n
	if b.MaxTokens > 0 && next > b.MaxTokens {
		b.exhausted = true
		return &BudgetExhaustedError{Field: "tokens", Limit: int64(b.MaxTokens), Current: int64(b.currentTokens)}
	}
	b.currentTokens = next
	if b.MaxTokens > 0 && b.currentTokens >= b.MaxTokens {
		b.exhausted = true
	}
	return nil
}

func (b *MutationBudget) consumeAttempts(n int) error {
	if n <= 0 {
		return nil
	}
	next := b.currentAttempts + n
	if b.MaxAttempts > 0 && next > b.MaxAttempts {
		b.exhausted = true
		return &BudgetExhaustedError{Field: "attempts", Limit: int64(b.MaxAttempts), Current: int64(b.currentAttempts)}
	}
	b.currentAttempts = next
	if b.MaxAttempts > 0 && b.currentAttempts >= b.MaxAttempts {
		b.exhausted = true
	}
	return nil
}

func (b *MutationBudget) consumeShellCmds(n int) error {
	if n <= 0 {
		return nil
	}
	next := b.currentShellCmds + n
	if b.MaxShellCommands > 0 && next > b.MaxShellCommands {
		b.exhausted = true
		return &BudgetExhaustedError{Field: "shell_commands", Limit: int64(b.MaxShellCommands), Current: int64(b.currentShellCmds)}
	}
	b.currentShellCmds = next
	if b.MaxShellCommands > 0 && b.currentShellCmds >= b.MaxShellCommands {
		b.exhausted = true
	}
	return nil
}

// IsExhausted returns true if the budget has been exhausted.
func (b *MutationBudget) IsExhausted() bool { return b.exhausted }

// RemainingFiles returns the remaining file budget (0 if unlimited).
func (b *MutationBudget) RemainingFiles() int {
	if b.MaxFiles <= 0 {
		return 0
	}
	r := b.MaxFiles - b.currentFiles
	if r < 0 {
		return 0
	}
	return r
}

// RemainingDiffLines returns the remaining diff line budget (0 if unlimited).
func (b *MutationBudget) RemainingDiffLines() int {
	if b.MaxDiffLines <= 0 {
		return 0
	}
	r := b.MaxDiffLines - b.currentDiffLines
	if r < 0 {
		return 0
	}
	return r
}

// RemainingTokens returns the remaining token budget (0 if unlimited).
func (b *MutationBudget) RemainingTokens() int {
	if b.MaxTokens <= 0 {
		return 0
	}
	r := b.MaxTokens - b.currentTokens
	if r < 0 {
		return 0
	}
	return r
}

// RemainingAttempts returns the remaining attempt budget (0 if unlimited).
func (b *MutationBudget) RemainingAttempts() int {
	if b.MaxAttempts <= 0 {
		return 0
	}
	r := b.MaxAttempts - b.currentAttempts
	if r < 0 {
		return 0
	}
	return r
}

// RemainingShellCommands returns the remaining shell command budget (0 if
// unlimited).
func (b *MutationBudget) RemainingShellCommands() int {
	if b.MaxShellCommands <= 0 {
		return 0
	}
	r := b.MaxShellCommands - b.currentShellCmds
	if r < 0 {
		return 0
	}
	return r
}

// RemainingMutations returns the remaining mutation budget (0 if unlimited).
func (b *MutationBudget) RemainingMutations() int {
	if b.MaxMutations <= 0 {
		return 0
	}
	r := b.MaxMutations - b.currentMutations
	if r < 0 {
		return 0
	}
	return r
}

// RemainingTime returns the remaining execution time budget.
func (b *MutationBudget) RemainingTime() time.Duration {
	if b.MaxExecutionTime <= 0 {
		return 0
	}
	r := b.MaxExecutionTime - time.Since(b.startTime)
	if r < 0 {
		return 0
	}
	return r
}

// Reset clears all counters but keeps the same limits.
func (b *MutationBudget) Reset() {
	b.currentFiles = 0
	b.currentDiffLines = 0
	b.currentTokens = 0
	b.currentAttempts = 0
	b.currentShellCmds = 0
	b.currentMutations = 0
	b.startTime = time.Now()
	b.exhausted = false
}

// MicroBudget defines the strict limits for a micro-plan ($hot) pre-approval.
// If a proposed mutation fits entirely within these bounds, it is considered
// pre-approved and bypasses the AWAITING_APPROVAL lifecycle state.
type MicroBudget struct {
	MaxFiles           int
	MaxDiffLines       int
	MaxTokens          int
	MaxAttempts        int
	CheckpointRequired bool
}

// DefaultMicroBudget returns the standard micro-plan limits.
func DefaultMicroBudget() MicroBudget {
	return MicroBudget{
		MaxFiles:           2,
		MaxDiffLines:       50,
		MaxTokens:          2000,
		MaxAttempts:        1,
		CheckpointRequired: true,
	}
}

// IsWithinMicroBudget checks whether a proposed mutation (described by its
// BudgetDelta and whether a checkpoint exists) fits within the micro-plan
// limits. If it fits, the mutation is pre-approved and can bypass the
// AWAITING_APPROVAL lifecycle state, transitioning directly to AUTHORIZED.
func (mb MicroBudget) IsWithinMicroBudget(delta BudgetDelta, hasCheckpoint bool) bool {
	if delta.Files > mb.MaxFiles {
		return false
	}
	if delta.DiffLines > mb.MaxDiffLines {
		return false
	}
	if delta.Tokens > mb.MaxTokens {
		return false
	}
	if delta.Attempts > mb.MaxAttempts {
		return false
	}
	if mb.CheckpointRequired && !hasCheckpoint {
		return false
	}
	return true
}

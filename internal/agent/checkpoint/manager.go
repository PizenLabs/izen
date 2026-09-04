//go:build ignore

package checkpoint

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const (
	DefaultCompactionThreshold = 0.75
	DefaultMaxTokens           = 128_000
)

type SubTaskSnapshot struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Summary   string    `json:"summary"`
	TokenCost int       `json:"token_cost"`
	CreatedAt time.Time `json:"created_at"`
}

type ExecutionOutputSnapshot struct {
	Step       string `json:"step"`
	OutputHash string `json:"output_hash,omitempty"`
	Summary    string `json:"summary"`
	TokenCost  int    `json:"token_cost"`
}

type ASTDiffSnapshot struct {
	File      string `json:"file"`
	Summary   string `json:"summary"`
	Churn     int    `json:"churn"`
	TokenCost int    `json:"token_cost"`
}

type EnvironmentSnapshot struct {
	RuntimeContext  string           `json:"runtime_context"`
	SystemRules     []string         `json:"system_rules"`
	Capabilities    map[string]bool  `json:"capabilities"`
	RemainingBudget map[string]int64 `json:"remaining_budget"`
}

type CheckpointState struct {
	TotalTokensConsumed int                       `json:"total_tokens_consumed"`
	MaxTokens           int                       `json:"max_tokens"`
	TurnCount           int                       `json:"turn_count"`
	Compacted           bool                      `json:"compacted"`
	SubTasks            []SubTaskSnapshot         `json:"sub_tasks"`
	ExecutionOutputs    []ExecutionOutputSnapshot `json:"execution_outputs"`
	ASTDiffs            []ASTDiffSnapshot         `json:"ast_diffs"`
	Environment         EnvironmentSnapshot       `json:"environment"`
	CheckpointSum       string                    `json:"checkpoint_summary,omitempty"`
	CreatedAt           time.Time                 `json:"created_at"`
	UpdatedAt           time.Time                 `json:"updated_at"`
}

type TokenTracker struct {
	TotalInput  int
	TotalOutput int
	MaxTokens   int
}

func (t *TokenTracker) Usage() int {
	return t.TotalInput + t.TotalOutput
}

func (t *TokenTracker) Ratio() float64 {
	if t.MaxTokens <= 0 {
		return 0
	}
	return float64(t.Usage()) / float64(t.MaxTokens)
}

func (t *TokenTracker) ShouldCompact(threshold float64) bool {
	return t.Ratio() >= threshold
}

type CheckpointManager struct {
	mu                  sync.RWMutex
	state               CheckpointState
	tracker             TokenTracker
	compactionThreshold float64
}

func NewCheckpointManager() *CheckpointManager {
	return &CheckpointManager{
		state: CheckpointState{
			MaxTokens: DefaultMaxTokens,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		tracker: TokenTracker{
			MaxTokens: DefaultMaxTokens,
		},
		compactionThreshold: DefaultCompactionThreshold,
	}
}

func NewCheckpointManagerWithBudget(maxTokens int) *CheckpointManager {
	cm := NewCheckpointManager()
	cm.state.MaxTokens = maxTokens
	cm.tracker.MaxTokens = maxTokens
	return cm
}

func (cm *CheckpointManager) State() CheckpointState {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.state
}

func (cm *CheckpointManager) Tracker() TokenTracker {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.tracker
}

func (cm *CheckpointManager) RecordTokens(inputTokens, outputTokens int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.tracker.TotalInput += inputTokens
	cm.tracker.TotalOutput += outputTokens
	cm.state.TotalTokensConsumed = cm.tracker.Usage()
	cm.state.UpdatedAt = time.Now()

	if cm.tracker.ShouldCompact(cm.compactionThreshold) && !cm.state.Compacted {
		cm.compactLocked()
	}
}

func (cm *CheckpointManager) RecordSubTask(id, status, summary string, tokenCost int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.state.SubTasks = append(cm.state.SubTasks, SubTaskSnapshot{
		ID:        id,
		Status:    status,
		Summary:   summary,
		TokenCost: tokenCost,
		CreatedAt: time.Now(),
	})
	cm.state.UpdatedAt = time.Now()
}

func (cm *CheckpointManager) RecordExecutionOutput(step, summary string, tokenCost int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.state.ExecutionOutputs = append(cm.state.ExecutionOutputs, ExecutionOutputSnapshot{
		Step:      step,
		Summary:   summary,
		TokenCost: tokenCost,
	})
	cm.state.UpdatedAt = time.Now()
}

func (cm *CheckpointManager) RecordASTDiff(file, summary string, churn, tokenCost int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.state.ASTDiffs = append(cm.state.ASTDiffs, ASTDiffSnapshot{
		File:      file,
		Summary:   summary,
		Churn:     churn,
		TokenCost: tokenCost,
	})
	cm.state.UpdatedAt = time.Now()
}

func (cm *CheckpointManager) SetEnvironment(env EnvironmentSnapshot) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.state.Environment = env
	cm.state.UpdatedAt = time.Now()
}

func (cm *CheckpointManager) IncrementTurn() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.state.TurnCount++
	cm.state.UpdatedAt = time.Now()
}

func (cm *CheckpointManager) ShouldCompact() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.tracker.ShouldCompact(cm.compactionThreshold) && !cm.state.Compacted
}

func (cm *CheckpointManager) compactLocked() {
	completedSubTasks := make([]SubTaskSnapshot, 0)
	for _, st := range cm.state.SubTasks {
		if st.Status != "completed" && st.Status != "failed" {
			completedSubTasks = append(completedSubTasks, st)
		}
	}

	outputTotal := 0
	for _, o := range cm.state.ExecutionOutputs {
		outputTotal += o.TokenCost
	}

	diffTotal := 0
	diffFiles := len(cm.state.ASTDiffs)
	for _, d := range cm.state.ASTDiffs {
		diffTotal += d.TokenCost
	}

	type compactedSubTask struct {
		Count   int    `json:"count"`
		Summary string `json:"summary"`
	}

	type compactedOutput struct {
		Count int    `json:"count"`
		Total string `json:"total"`
	}

	meta := struct {
		CompactedAt      time.Time        `json:"compacted_at"`
		TurnCount        int              `json:"turn_count"`
		TotalTokens      int              `json:"total_tokens"`
		CompactedTasks   compactedSubTask `json:"compacted_tasks"`
		CompactedOutputs compactedOutput  `json:"compacted_outputs"`
		CompactedDiffs   int              `json:"compacted_diff_files"`
	}{
		CompactedAt: time.Now(),
		TurnCount:   cm.state.TurnCount,
		TotalTokens: cm.state.TotalTokensConsumed,
		CompactedTasks: compactedSubTask{
			Count:   len(cm.state.SubTasks),
			Summary: summarizeCompletedTasks(cm.state.SubTasks),
		},
		CompactedOutputs: compactedOutput{
			Count: len(cm.state.ExecutionOutputs),
			Total: fmt.Sprintf("%d tokens", outputTotal),
		},
		CompactedDiffs: diffFiles,
	}

	b, _ := json.Marshal(meta)

	cm.state.CheckpointSum = string(b)
	cm.state.SubTasks = completedSubTasks
	cm.state.ExecutionOutputs = nil
	cm.state.ASTDiffs = nil
	cm.state.Compacted = true
	cm.state.TotalTokensConsumed = cm.tracker.Usage()
}

func summarizeCompletedTasks(tasks []SubTaskSnapshot) string {
	if len(tasks) == 0 {
		return "no tasks"
	}
	completed := 0
	failed := 0
	pending := 0
	totalTokens := 0
	for _, t := range tasks {
		totalTokens += t.TokenCost
		switch t.Status {
		case "completed":
			completed++
		case "failed":
			failed++
		default:
			pending++
		}
	}
	return fmt.Sprintf("%d completed, %d failed, %d pending (%d total tokens)", completed, failed, pending, totalTokens)
}

func (cm *CheckpointManager) Snapshot() ([]byte, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return json.Marshal(cm.state)
}

func (cm *CheckpointManager) Restore(data []byte) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	var state CheckpointState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("checkpoint: restore: %w", err)
	}

	cm.state = state
	cm.tracker = TokenTracker{
		TotalInput:  0,
		TotalOutput: 0,
		MaxTokens:   state.MaxTokens,
	}
	cm.state.UpdatedAt = time.Now()
	return nil
}

func (cm *CheckpointManager) RestoreWithTokens(data []byte, inputTokens, outputTokens int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	var state CheckpointState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("checkpoint: restore: %w", err)
	}

	cm.state = state
	cm.tracker = TokenTracker{
		TotalInput:  inputTokens,
		TotalOutput: outputTokens,
		MaxTokens:   state.MaxTokens,
	}
	cm.state.TotalTokensConsumed = inputTokens + outputTokens
	cm.state.UpdatedAt = time.Now()
	return nil
}

func (cm *CheckpointManager) Reset() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.state = CheckpointState{
		MaxTokens: cm.state.MaxTokens,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	cm.tracker = TokenTracker{
		MaxTokens: cm.state.MaxTokens,
	}
}

func (cm *CheckpointManager) CheckpointSummary() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if cm.state.CheckpointSum != "" {
		return cm.state.CheckpointSum
	}

	return fmt.Sprintf(
		"turn=%d tokens=%d/%d tasks=%d outputs=%d compacted=%v",
		cm.state.TurnCount,
		cm.state.TotalTokensConsumed,
		cm.state.MaxTokens,
		len(cm.state.SubTasks),
		len(cm.state.ExecutionOutputs),
		cm.state.Compacted,
	)
}

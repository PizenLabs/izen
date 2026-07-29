package checkpoint

import (
	"encoding/json"
	"testing"
)

// ── Token Tracking ────────────────────────────────────────────────────────

func TestTokenTracker_InitialState(t *testing.T) {
	tt := TokenTracker{MaxTokens: DefaultMaxTokens}
	if tt.Usage() != 0 {
		t.Errorf("expected 0 usage, got %d", tt.Usage())
	}
	if tt.Ratio() != 0 {
		t.Errorf("expected 0 ratio, got %f", tt.Ratio())
	}
}

func TestTokenTracker_Usage(t *testing.T) {
	tt := TokenTracker{TotalInput: 1000, TotalOutput: 2000, MaxTokens: 128000}
	if tt.Usage() != 3000 {
		t.Errorf("expected 3000 usage, got %d", tt.Usage())
	}
}

func TestTokenTracker_Ratio(t *testing.T) {
	tt := TokenTracker{TotalInput: 64000, TotalOutput: 32000, MaxTokens: 128000}
	if tt.Ratio() != 0.75 {
		t.Errorf("expected 0.75 ratio, got %f", tt.Ratio())
	}
}

func TestTokenTracker_Ratio_ZeroMax(t *testing.T) {
	tt := TokenTracker{MaxTokens: 0}
	if tt.Ratio() != 0 {
		t.Errorf("expected 0 ratio for zero max, got %f", tt.Ratio())
	}
}

func TestTokenTracker_ShouldCompact(t *testing.T) {
	tests := []struct {
		name      string
		input     int
		output    int
		maxTokens int
		threshold float64
		want      bool
	}{
		{"below threshold", 1000, 2000, 128000, 0.75, false},
		{"at threshold", 48000, 48000, 128000, 0.75, true},
		{"above threshold", 100000, 0, 128000, 0.75, true},
		{"zero threshold hits always", 1, 0, 128000, 0.0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := TokenTracker{
				TotalInput:  tt.input,
				TotalOutput: tt.output,
				MaxTokens:   tt.maxTokens,
			}
			if got := tracker.ShouldCompact(tt.threshold); got != tt.want {
				t.Errorf("ShouldCompact() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ── CheckpointManager ─────────────────────────────────────────────────────

func TestNewCheckpointManager(t *testing.T) {
	cm := NewCheckpointManager()
	if cm == nil {
		t.Fatal("NewCheckpointManager returned nil")
	}

	state := cm.State()
	if state.MaxTokens != DefaultMaxTokens {
		t.Errorf("expected MaxTokens %d, got %d", DefaultMaxTokens, state.MaxTokens)
	}
	if state.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
	if state.Compacted {
		t.Error("expected initially not compacted")
	}
}

func TestNewCheckpointManagerWithBudget(t *testing.T) {
	cm := NewCheckpointManagerWithBudget(256000)
	if cm.State().MaxTokens != 256000 {
		t.Errorf("expected 256000 max tokens, got %d", cm.State().MaxTokens)
	}
}

func TestRecordTokens(t *testing.T) {
	cm := NewCheckpointManager()
	cm.RecordTokens(5000, 3000)

	state := cm.State()
	if state.TotalTokensConsumed != 8000 {
		t.Errorf("expected 8000 tokens consumed, got %d", state.TotalTokensConsumed)
	}

	tracker := cm.Tracker()
	if tracker.TotalInput != 5000 {
		t.Errorf("expected 5000 input, got %d", tracker.TotalInput)
	}
	if tracker.TotalOutput != 3000 {
		t.Errorf("expected 3000 output, got %d", tracker.TotalOutput)
	}
}

func TestRecordTokens_Accumulates(t *testing.T) {
	cm := NewCheckpointManager()
	cm.RecordTokens(1000, 2000)
	cm.RecordTokens(3000, 4000)

	state := cm.State()
	if state.TotalTokensConsumed != 10000 {
		t.Errorf("expected 10000 tokens consumed, got %d", state.TotalTokensConsumed)
	}
}

func TestRecordSubTask(t *testing.T) {
	cm := NewCheckpointManager()
	cm.RecordSubTask("task-1", "completed", "refactored auth module", 1500)

	state := cm.State()
	if len(state.SubTasks) != 1 {
		t.Fatalf("expected 1 sub task, got %d", len(state.SubTasks))
	}
	if state.SubTasks[0].ID != "task-1" {
		t.Errorf("expected task ID 'task-1', got %q", state.SubTasks[0].ID)
	}
	if state.SubTasks[0].Status != "completed" {
		t.Errorf("expected status 'completed', got %q", state.SubTasks[0].Status)
	}
	if state.SubTasks[0].TokenCost != 1500 {
		t.Errorf("expected 1500 token cost, got %d", state.SubTasks[0].TokenCost)
	}
}

func TestRecordMultipleSubTasks(t *testing.T) {
	cm := NewCheckpointManager()
	cm.RecordSubTask("task-1", "completed", "first", 100)
	cm.RecordSubTask("task-2", "in-progress", "second", 200)
	cm.RecordSubTask("task-3", "pending", "third", 300)

	state := cm.State()
	if len(state.SubTasks) != 3 {
		t.Fatalf("expected 3 sub tasks, got %d", len(state.SubTasks))
	}
}

func TestRecordExecutionOutput(t *testing.T) {
	cm := NewCheckpointManager()
	cm.RecordExecutionOutput("build", "compilation successful", 500)

	state := cm.State()
	if len(state.ExecutionOutputs) != 1 {
		t.Fatalf("expected 1 execution output, got %d", len(state.ExecutionOutputs))
	}
	if state.ExecutionOutputs[0].Step != "build" {
		t.Errorf("expected step 'build', got %q", state.ExecutionOutputs[0].Step)
	}
}

func TestRecordASTDiff(t *testing.T) {
	cm := NewCheckpointManager()
	cm.RecordASTDiff("main.go", "refactored function signature", 10, 300)

	state := cm.State()
	if len(state.ASTDiffs) != 1 {
		t.Fatalf("expected 1 AST diff, got %d", len(state.ASTDiffs))
	}
	if state.ASTDiffs[0].File != "main.go" {
		t.Errorf("expected file 'main.go', got %q", state.ASTDiffs[0].File)
	}
}

func TestSetEnvironment(t *testing.T) {
	cm := NewCheckpointManager()
	env := EnvironmentSnapshot{
		RuntimeContext: "go 1.26",
		SystemRules:    []string{"no secrets", "no network"},
		Capabilities:   map[string]bool{"write": true},
	}
	cm.SetEnvironment(env)

	state := cm.State()
	if state.Environment.RuntimeContext != "go 1.26" {
		t.Errorf("expected runtime context 'go 1.26', got %q", state.Environment.RuntimeContext)
	}
	if len(state.Environment.SystemRules) != 2 {
		t.Errorf("expected 2 system rules, got %d", len(state.Environment.SystemRules))
	}
}

func TestIncrementTurn(t *testing.T) {
	cm := NewCheckpointManager()
	if cm.State().TurnCount != 0 {
		t.Errorf("expected initial turn count 0, got %d", cm.State().TurnCount)
	}

	cm.IncrementTurn()
	if cm.State().TurnCount != 1 {
		t.Errorf("expected turn count 1, got %d", cm.State().TurnCount)
	}

	cm.IncrementTurn()
	if cm.State().TurnCount != 2 {
		t.Errorf("expected turn count 2, got %d", cm.State().TurnCount)
	}
}

// ── Compaction ─────────────────────────────────────────────────────────────

func TestShouldCompact_BelowThreshold(t *testing.T) {
	cm := NewCheckpointManagerWithBudget(128000)
	cm.RecordTokens(10000, 10000)

	if cm.ShouldCompact() {
		t.Error("expected ShouldCompact to be false below 75%")
	}
}

func TestShouldCompact_AtThreshold(t *testing.T) {
	cm := NewCheckpointManagerWithBudget(128000)
	cm.RecordTokens(48000, 48000)

	state := cm.State()
	if !state.Compacted {
		t.Error("expected auto-compaction at 75% threshold")
	}
}

func TestShouldCompact_AfterCompaction(t *testing.T) {
	cm := NewCheckpointManagerWithBudget(128000)
	cm.RecordTokens(96000, 0)

	if !cm.State().Compacted {
		t.Error("expected auto-compaction to trigger")
	}

	if cm.ShouldCompact() {
		t.Error("expected ShouldCompact to be false after compaction")
	}
}

func TestCompaction_ClearsOutputsAndDiffs(t *testing.T) {
	cm := NewCheckpointManagerWithBudget(1000)

	cm.RecordSubTask("task-1", "completed", "task one", 100)
	cm.RecordSubTask("task-2", "completed", "task two", 100)
	cm.RecordExecutionOutput("build", "build output", 200)
	cm.RecordASTDiff("main.go", "diff", 5, 150)
	cm.RecordTokens(750, 0)

	state := cm.State()
	if !state.Compacted {
		t.Fatal("expected compaction to have occurred")
	}

	if len(state.ExecutionOutputs) != 0 {
		t.Errorf("expected 0 execution outputs after compaction, got %d", len(state.ExecutionOutputs))
	}
	if len(state.ASTDiffs) != 0 {
		t.Errorf("expected 0 AST diffs after compaction, got %d", len(state.ASTDiffs))
	}
}

func TestCompaction_PreservesActiveSubTasks(t *testing.T) {
	cm := NewCheckpointManagerWithBudget(1000)

	cm.RecordSubTask("completed-1", "completed", "done", 100)
	cm.RecordSubTask("failed-1", "failed", "failed", 100)
	cm.RecordSubTask("active-1", "in-progress", "active", 50)
	cm.RecordSubTask("pending-1", "pending", "pending", 50)
	cm.RecordTokens(750, 0)

	state := cm.State()
	if !state.Compacted {
		t.Fatal("expected compaction to have occurred")
	}

	if len(state.SubTasks) != 2 {
		t.Errorf("expected 2 active sub tasks (in-progress, pending), got %d", len(state.SubTasks))
	}
}

func TestCompaction_GeneratesSummary(t *testing.T) {
	cm := NewCheckpointManagerWithBudget(1000)

	cm.RecordSubTask("task-1", "completed", "first", 200)
	cm.RecordSubTask("task-2", "completed", "second", 300)
	cm.RecordExecutionOutput("test", "output", 100)
	cm.RecordTokens(750, 0)

	state := cm.State()
	if state.CheckpointSum == "" {
		t.Fatal("expected checkpoint summary after compaction")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(state.CheckpointSum), &parsed); err != nil {
		t.Fatalf("checkpoint summary is not valid JSON: %v", err)
	}

	if _, ok := parsed["compacted_at"]; !ok {
		t.Error("expected compacted_at in summary")
	}
	if _, ok := parsed["compacted_tasks"]; !ok {
		t.Error("expected compacted_tasks in summary")
	}
}

// ── Snapshot Serialisation/Deserialisation ─────────────────────────────────

func TestSnapshotAndRestore(t *testing.T) {
	cm := NewCheckpointManager()

	cm.RecordTokens(5000, 3000)
	cm.RecordSubTask("task-1", "completed", "done", 200)
	cm.RecordExecutionOutput("build", "ok", 100)
	cm.IncrementTurn()

	data, err := cm.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	restored := NewCheckpointManager()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	if restored.State().TotalTokensConsumed != 8000 {
		t.Errorf("expected 8000 tokens, got %d", restored.State().TotalTokensConsumed)
	}
	if restored.State().TurnCount != 1 {
		t.Errorf("expected 1 turn, got %d", restored.State().TurnCount)
	}
	if len(restored.State().SubTasks) != 1 {
		t.Errorf("expected 1 sub task, got %d", len(restored.State().SubTasks))
	}
	if len(restored.State().ExecutionOutputs) != 1 {
		t.Errorf("expected 1 execution output, got %d", len(restored.State().ExecutionOutputs))
	}
}

func TestSnapshotAndRestoreWithTokens(t *testing.T) {
	cm := NewCheckpointManager()
	cm.RecordSubTask("task-1", "completed", "done", 200)
	cm.IncrementTurn()

	data, err := cm.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	restored := NewCheckpointManager()
	if err := restored.RestoreWithTokens(data, 10000, 5000); err != nil {
		t.Fatalf("RestoreWithTokens failed: %v", err)
	}

	state := restored.State()
	if state.TotalTokensConsumed != 15000 {
		t.Errorf("expected 15000 tokens, got %d", state.TotalTokensConsumed)
	}

	tracker := restored.Tracker()
	if tracker.TotalInput != 10000 {
		t.Errorf("expected 10000 input, got %d", tracker.TotalInput)
	}
	if tracker.TotalOutput != 5000 {
		t.Errorf("expected 5000 output, got %d", tracker.TotalOutput)
	}
}

func TestRestore_InvalidData(t *testing.T) {
	cm := NewCheckpointManager()
	err := cm.Restore([]byte("{invalid json"))
	if err == nil {
		t.Error("expected error from invalid JSON restore")
	}
}

func TestGetCheckpointSummary(t *testing.T) {
	cm := NewCheckpointManager()
	summary := cm.CheckpointSummary()

	if summary == "" {
		t.Error("expected non-empty checkpoint summary")
	}
}

func TestCheckpointSummary_AfterCompaction(t *testing.T) {
	cm := NewCheckpointManagerWithBudget(1000)
	cm.RecordTokens(750, 0)
	summary := cm.CheckpointSummary()

	if summary == "" {
		t.Error("expected non-empty checkpoint summary after compaction")
	}
}

func TestReset(t *testing.T) {
	cm := NewCheckpointManager()
	cm.RecordTokens(10000, 5000)
	cm.RecordSubTask("task-1", "completed", "done", 200)
	cm.IncrementTurn()

	cm.Reset()

	state := cm.State()
	if state.TotalTokensConsumed != 0 {
		t.Errorf("expected 0 tokens after reset, got %d", state.TotalTokensConsumed)
	}
	if state.TurnCount != 0 {
		t.Errorf("expected 0 turns after reset, got %d", state.TurnCount)
	}
	if len(state.SubTasks) != 0 {
		t.Errorf("expected 0 sub tasks after reset, got %d", len(state.SubTasks))
	}
	if state.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be reset")
	}
}

func TestReset_PreservesMaxTokens(t *testing.T) {
	cm := NewCheckpointManagerWithBudget(256000)
	cm.RecordTokens(10000, 5000)
	cm.Reset()

	if cm.State().MaxTokens != 256000 {
		t.Errorf("expected MaxTokens to be preserved after reset, got %d", cm.State().MaxTokens)
	}
}

// ── Concurrency Safety ─────────────────────────────────────────────────────

func TestConcurrentAccess(t *testing.T) {
	cm := NewCheckpointManager()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			cm.RecordTokens(10, 20)
			cm.RecordSubTask("t", "completed", "task", 50)
			cm.IncrementTurn()
		}
		close(done)
	}()

	go func() {
		for i := 0; i < 50; i++ {
			cm.State()
			cm.Tracker()
			cm.ShouldCompact()
			cm.CheckpointSummary()
		}
	}()

	<-done
	state := cm.State()
	if state.TurnCount != 100 {
		t.Errorf("expected 100 turns, got %d", state.TurnCount)
	}
}

// ── Edge Cases ────────────────────────────────────────────────────────────

func TestEmptyCompaction(t *testing.T) {
	cm := NewCheckpointManagerWithBudget(100)
	cm.RecordTokens(100, 0)

	if !cm.State().Compacted {
		t.Error("expected compaction with no tasks/outputs/diffs to succeed")
	}
}

func TestCheckpointSummary_Empty(t *testing.T) {
	cm := NewCheckpointManager()
	summary := cm.CheckpointSummary()

	if summary == "" {
		t.Error("expected non-empty even when empty")
	}
}

func TestTrackerStandard(t *testing.T) {
	tt := TokenTracker{TotalInput: 50000, TotalOutput: 30000, MaxTokens: DefaultMaxTokens}
	if tt.Usage() != 80000 {
		t.Errorf("expected 80000 usage, got %d", tt.Usage())
	}
	expectedRatio := float64(80000) / float64(DefaultMaxTokens)
	if tt.Ratio() != expectedRatio {
		t.Errorf("expected %f ratio, got %f", expectedRatio, tt.Ratio())
	}
}

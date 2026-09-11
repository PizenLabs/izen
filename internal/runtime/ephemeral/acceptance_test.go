package ephemeral

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/runtime/durable"
)

func testStore(t *testing.T, taskID string) *durable.TaskStore {
	t.Helper()
	dir := t.TempDir()
	s := durable.NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.CreateTask(taskID, "migrate auth module", []string{"auth/"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Checkpoint(taskID, "cp-1"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	return s
}

func twoWorkers() *WorkerRouter {
	return NewWorkerRouter([]WorkerDescriptor{
		{ID: "worker-a", Provider: "openai", ContextTokens: 128000, Capabilities: []string{"code-edit"}, Healthy: true, CostPerStep: 1},
		{ID: "worker-b", Provider: "anthropic", ContextTokens: 200000, Capabilities: []string{"code-edit"}, StrictStructuredOutput: true, Healthy: true, CostPerStep: 2},
	})
}

func baseContext(taskID string, worker WorkerDescriptor) FailureContext {
	return FailureContext{
		TaskID:               taskID,
		Step:                 StepDefinition{ID: "step-1", Goal: "rewrite login handler", RequiredCaps: []string{"code-edit"}, MinContextTokens: 32000},
		ConditionFingerprint: "pre-digest-1",
		CurrentWorker:        worker,
		PlanValid:            true,
		TargetValid:          true,
		EvidenceValid:        true,
		CurrentStepClear:     true,
		Objective:            "migrate auth module",
		Constraints:          []string{"no API changes"},
		CompletedSteps:       []string{"step-0"},
		PendingSteps:         []string{"step-2"},
		Evidence:             []EvidenceSummary{{Kind: "test", Subject: "auth", Detail: "pass"}},
		AllowedTools:         []string{"read", "edit"},
		CheckpointID:         "cp-1",
	}
}

func ledgerString(t *testing.T, s *durable.TaskStore) string {
	t.Helper()
	data, err := os.ReadFile(s.LedgerPath())
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return string(data)
}

// Criterion 1: simulated TOKEN_LIMIT mid-stream terminates Worker A,
// derives ResumeContract, assigns Worker B from CurrentStep without
// sending conversation history.
func TestTokenLimitHandoffToWorkerB(t *testing.T) {
	s := testStore(t, "t-tok")
	eng := NewRecoveryEngine(s, twoWorkers(), 3, 10)
	workerA := WorkerDescriptor{ID: "worker-a", Provider: "openai", ContextTokens: 128000, Capabilities: []string{"code-edit"}, Healthy: true}

	fc := baseContext("t-tok", workerA)
	fc.ProviderErr = ProviderError{FinishReason: "length", Provider: "openai"}

	out, err := eng.HandleFailure(fc)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if out.Reason != FailureTokenLimit {
		t.Fatalf("reason=%q want TOKEN_LIMIT", out.Reason)
	}
	if out.Action != ActionResume {
		t.Fatalf("action=%q want RESUME", out.Action)
	}
	if out.TargetWorker.ID != "worker-b" {
		t.Fatalf("target=%q want worker-b (equal-or-higher context)", out.TargetWorker.ID)
	}
	if out.Contract == nil {
		t.Fatal("nil resume contract")
	}
	c := out.Contract
	if c.Capsule.TaskID != "t-tok" {
		t.Fatalf("capsule task=%q want t-tok (identity preserved)", c.Capsule.TaskID)
	}
	if c.Capsule.CurrentStep.ID != "step-1" {
		t.Fatalf("resume step=%q want step-1", c.Capsule.CurrentStep.ID)
	}
	if c.CheckpointID != "cp-1" {
		t.Fatalf("checkpoint=%q want cp-1 (failover preserves checkpoint)", c.CheckpointID)
	}
	if c.ResumeReason != FailureTokenLimit {
		t.Fatalf("resume reason=%q want TOKEN_LIMIT", c.ResumeReason)
	}
	// Transcript independence: the contract is capsule-only. Serialized
	// JSON must contain no transcript-shaped fields.
	raw, _ := json.Marshal(c)
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{"transcript", "messages", "history", "conversation", "prompt_log"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("contract leaks transcript field %q: %s", forbidden, raw)
		}
	}
	// Ledger-first: FAILURE_CLASSIFIED precedes WORKER_HANDOFF.
	ledger := ledgerString(t, s)
	failIdx := strings.Index(ledger, string(durable.EventFailureClassified))
	handIdx := strings.Index(ledger, string(durable.EventWorkerHandoff))
	if failIdx < 0 {
		t.Fatal("FAILURE_CLASSIFIED missing from ledger")
	}
	if handIdx < 0 {
		t.Fatal("WORKER_HANDOFF missing from ledger")
	}
	if failIdx > handIdx {
		t.Fatal("recovery attempted before classified failure was recorded")
	}
	// Identity preserved in store.
	st, _ := s.State("t-tok")
	if st.ID != "t-tok" || st.LastCheckpointID != "cp-1" {
		t.Fatalf("state-driven failover violated: %+v", st)
	}
	// Budget decremented exactly once.
	if out.BudgetLeft != 2 {
		t.Fatalf("budget=%d want 2 after one transition from 3", out.BudgetLeft)
	}
}

// Criterion 2: HTTP 429/503 triggers deterministic failover to an alternate
// model adapter while preserving exact task state.
func TestRateLimitAndUnavailableFailover(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		reason FailureReason
	}{
		{"rate-limited", 429, FailureRateLimited},
		{"unavailable", 503, FailureProviderUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t, "t-fail")
			eng := NewRecoveryEngine(s, twoWorkers(), 3, 10)
			workerA := WorkerDescriptor{ID: "worker-a", Provider: "openai", ContextTokens: 128000, Capabilities: []string{"code-edit"}, Healthy: true}
			fc := baseContext("t-fail", workerA)
			fc.ProviderErr = ProviderError{HTTPStatus: tc.status, Err: errors.New("upstream error"), Provider: "openai"}

			before, _ := s.State("t-fail")
			out, err := eng.HandleFailure(fc)
			if err != nil {
				t.Fatalf("handle: %v", err)
			}
			if out.Reason != tc.reason {
				t.Fatalf("reason=%q want %q", out.Reason, tc.reason)
			}
			if out.Action != ActionResume {
				t.Fatalf("action=%q want RESUME", out.Action)
			}
			// Alternate adapter: must leave the failed provider.
			if out.TargetWorker.Provider == "openai" {
				t.Fatalf("failover stayed on failed provider: %+v", out.TargetWorker)
			}
			if out.TargetWorker.ID != "worker-b" {
				t.Fatalf("target=%q want worker-b", out.TargetWorker.ID)
			}
			// Exact task state preserved: snapshot equality on the
			// identity-bearing fields (failover must not modify state).
			after, _ := s.State("t-fail")
			if after.ID != before.ID || after.Intent != before.Intent ||
				after.LastCheckpointID != before.LastCheckpointID ||
				strings.Join(after.ActiveTargetScope, ",") != strings.Join(before.ActiveTargetScope, ",") {
				t.Fatalf("task state modified by failover: before=%+v after=%+v", before, after)
			}
		})
	}
}

// Criterion 3: a step failing twice under identical preconditions moves
// from RESUME to RE_PLAN.
func TestDoubleFailureEscalatesToRePlan(t *testing.T) {
	s := testStore(t, "t-replan")
	eng := NewRecoveryEngine(s, twoWorkers(), 5, 10)
	workerA := WorkerDescriptor{ID: "worker-a", Provider: "openai", ContextTokens: 128000, Capabilities: []string{"code-edit"}, Healthy: true}

	fc := baseContext("t-replan", workerA)
	fc.ProviderErr = ProviderError{Err: errors.New("connection reset by peer"), Provider: "ollama"}

	first, err := eng.HandleFailure(fc)
	if err != nil {
		t.Fatalf("first handle: %v", err)
	}
	if first.Action != ActionResume {
		t.Fatalf("first action=%q want RESUME", first.Action)
	}
	// Identical conditions: same step + same fingerprint.
	second, err := eng.HandleFailure(fc)
	if err != nil {
		t.Fatalf("second handle: %v", err)
	}
	if second.Action != ActionRePlan {
		t.Fatalf("second action=%q want RE_PLAN after 2 identical failures", second.Action)
	}
	if second.Contract != nil {
		t.Fatal("RE_PLAN must not produce a resume contract")
	}
}

// Criterion 4: budget exhaustion triggers ESCALATE and halts the loop
// (task PAUSED, no further routing).
func TestBudgetExhaustionEscalatesAndPauses(t *testing.T) {
	s := testStore(t, "t-budget")
	eng := NewRecoveryEngine(s, twoWorkers(), 2, 10)
	workerA := WorkerDescriptor{ID: "worker-a", Provider: "openai", ContextTokens: 128000, Capabilities: []string{"code-edit"}, Healthy: true}

	mk := func(fp string) FailureContext {
		fc := baseContext("t-budget", workerA)
		fc.ConditionFingerprint = fp // distinct conditions avoid the RE_PLAN rule
		fc.ProviderErr = ProviderError{Err: errors.New("connection reset by peer")}
		return fc
	}
	if _, err := eng.HandleFailure(mk("cond-1")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := eng.HandleFailure(mk("cond-2")); err != nil {
		t.Fatalf("second: %v", err)
	}
	third, err := eng.HandleFailure(mk("cond-3"))
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	if third.Action != ActionEscalate {
		t.Fatalf("action=%q want ESCALATE on exhausted budget", third.Action)
	}
	if !third.Paused {
		t.Fatal("exhausted task must be PAUSED")
	}
	if third.Contract != nil {
		t.Fatal("ESCALATE must not produce a resume contract (loop halted)")
	}
	st, _ := s.State("t-budget")
	if st.Status != durable.TaskPaused {
		t.Fatalf("status=%q want PAUSED", st.Status)
	}
	// A fourth failure stays escalated: no auto-recovery loop.
	fourth, err := eng.HandleFailure(mk("cond-4"))
	if err != nil {
		t.Fatalf("fourth: %v", err)
	}
	if fourth.Action != ActionEscalate || !fourth.Paused {
		t.Fatalf("loop not halted: %+v", fourth)
	}
}

package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/continuation"
	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	"github.com/PizenLabs/izen/internal/problem"
	"github.com/PizenLabs/izen/internal/problemsurface"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/executor"
	"github.com/PizenLabs/izen/internal/understanding"
)

// TestContinuationIntegration_RealExecutionPath traces the required Phase 4
// observable path (§20):
//
//	Task → ProblemSolvingPlan → bounded step → existing scheduler →
//	existing execution → outcome/evidence → continuation → next bounded step
func TestContinuationIntegration_RealExecutionPath(t *testing.T) {
	// 1. Human Intent → Project Understanding → Problem Surface → ProblemSolvingPlan
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.24\n")
	mustWrite(t, filepath.Join(root, "internal/worker/worker.go"), "package worker\nfunc Run(){}\n")
	mustWrite(t, filepath.Join(root, "internal/worker/pool.go"), "package worker\nfunc Pool(){}\n")
	u := understanding.Derive(root)
	if !u.Valid() {
		t.Fatalf("understanding invalid")
	}
	ps := problemsurface.Derive("investigate and fix memory leak in worker", nil, u)
	if ps.Status == problemsurface.StatusUnresolved {
		t.Fatalf("problem surface unresolved: %s", ps.UnresolvedReason)
	}
	plan := problem.Derive("investigate and fix memory leak in worker", u, ps)
	if plan.Status == problem.StatusUnresolved {
		t.Fatalf("plan unresolved: %s", plan.UnresolvedReason)
	}
	if len(plan.Steps) == 0 {
		t.Fatal("plan has no steps")
	}

	// 2. Bounded step via existing scheduler
	sched := NewStepScheduler()
	spec := TaskSpec{
		Objective:           "investigate and fix memory leak in worker",
		Targets:             targetsFromPlan(plan),
		TotalEstimatedSize:  1200,
		RequestedStepBudget: 600,
		TaskRemainingBudget: 2000,
		Type:                StepTypeMutation,
		StateFingerprints:   map[string]string{},
		DiskSizes:           map[string]int64{},
		Operations:          map[string]string{},
		EstimatedSizes:      map[string]int{},
	}
	for _, tgt := range spec.Targets {
		abs := filepath.Join(root, tgt)
		info, _ := os.Stat(abs)
		sz := int64(50)
		if info != nil {
			sz = info.Size()
		}
		spec.StateFingerprints[tgt] = "fp-" + tgt
		spec.DiskSizes[tgt] = sz
		spec.Operations[tgt] = "MODIFY"
		spec.EstimatedSizes[tgt] = 300
	}
	steps := sched.Schedule(spec)
	if len(steps) == 0 {
		t.Fatal("scheduler produced no steps")
	}
	// First bounded step
	first := steps[0]

	// 3. Existing execution (via staging) — simulate a partial output (finish_reason=length)
	state := &durable.TaskState{
		ID:                "task-integration",
		Intent:            spec.Objective,
		ActiveTargetScope: append([]string(nil), spec.Targets...),
		Status:            durable.TaskRunning,
		CurrentStepID:     first.ID,
	}
	spec.State = state
	spec.StepIndex = 0
	// Use executor staging path: length truncation yields PARTIAL
	worker := func(_ context.Context, _ ExecutionStep, _ ContextSlice, buf *executor.ProposalStagingBuffer) (StreamResult, error) {
		buf.AppendString("package worker\nfunc broken(")
		return StreamResult{FinishReason: "length", ObservedTokens: 980}, nil
	}
	sink := &integrationSink{path: filepath.Join(root, first.Targets[0])}
	result, err := sched.RunNext(context.Background(), spec, state, worker, evidence.VerdictPassed, sink)
	if err != nil && !strings.Contains(err.Error(), "halted") {
		// partial is not error, but zero-delta may halt on second partial; first should succeed
		t.Fatalf("RunNext: %v", err)
	}
	if result.Outcome != StepOutcomePartial {
		t.Fatalf("want partial from length truncation, got %s", result.Outcome)
	}
	if sink.calls != 0 {
		t.Fatalf("partial must not have mutated workspace")
	}

	// 4. Observation / evidence → continuation → next bounded step proposal
	obs := []continuation.Observation{
		{Kind: continuation.KindObservation, Subject: first.Targets[0], Detail: "truncated at length, durable state preserved", EvidenceDigest: "ev-001", StateFingerprint: first.StateFingerprint},
		{Kind: continuation.KindExecutionResult, Subject: first.Targets[0], Detail: "no verification yet", EvidenceDigest: "ev-001", StateFingerprint: first.StateFingerprint},
	}
	taskSnap := TaskStateSnapshot{
		Objective:        spec.Objective,
		StateFingerprint: first.StateFingerprint,
		EvidenceDigest:   "ev-001",
		CompletedSteps:   []string{},
		PendingSteps:     pendingIDs(plan),
	}
	decision := DeriveContinuation(taskSnap, spec, result, obs, spec.Targets, false)
	if decision.Action != continuation.ActionContinue {
		t.Fatalf("continuation after partial should be CONTINUE, got %s reason %s", decision.Action, decision.Reason)
	}
	if decision.NextStep == nil {
		t.Fatal("CONTINUE must carry NextStep")
	}
	// 5. Next bounded step re-enters existing scheduler
	nextSpec, ok := ContinuationToTaskSpec(spec, decision)
	if !ok {
		t.Fatal("ContinuationToTaskSpec failed to materialize next spec")
	}
	nextSteps := sched.Schedule(nextSpec)
	if len(nextSteps) == 0 {
		t.Fatal("scheduler produced no next steps from continuation proposal")
	}
	// The next step must be bounded and within original scope
	for _, n := range nextSteps {
		for _, tgt := range n.Targets {
			if !containsStr(spec.Targets, tgt) {
				t.Fatalf("continuation expanded scope: %q not in original %v", tgt, spec.Targets)
			}
		}
		if n.StepBudget > 1024 && spec.RequestedStepBudget <= 1024 {
			t.Fatalf("per-step budget %d exceeds provider ceiling", n.StepBudget)
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func targetsFromPlan(p problem.ProblemSolvingPlan) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range p.Steps {
		for _, r := range s.References {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}

func pendingIDs(p problem.ProblemSolvingPlan) []string {
	out := make([]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		out = append(out, s.ID)
	}
	return out
}

func containsStr(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

type integrationSink struct {
	path  string
	calls int
}

func (s *integrationSink) Apply(proposal string) (int, error) {
	s.calls++
	if proposal == "" {
		return 0, nil
	}
	if err := os.WriteFile(s.path, []byte(proposal), 0600); err != nil {
		return 0, err
	}
	return 1, nil
}

package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
	wschk "github.com/PizenLabs/izen/internal/workspace/checkpoint"
)

// waitCount polls until count() reaches want or the deadline expires. The
// event bus delivers asynchronously, so assertions must wait for delivery.
func waitCount(t *testing.T, mu *sync.Mutex, count func() int, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := count()
		mu.Unlock()
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %d events, got %d", want, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestExecuteBuildLoop_FullSelfHealingCycle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "worker.go"), []byte("package worker\n\nfunc Join(x int, y int) string { return \"ok\" }\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e)
	mgr := wschk.NewManager(dir)
	ex.WithCheckpointManager(mgr)

	approve(t, e, "worker.go", 1)

	calls := 0
	ex.WithVerifyCompilation(func(ctx context.Context, _ ...string) (bool, string, error) {
		calls++
		if calls == 1 {
			return false, "worker.go:3:2: cannot use x (type int) as type string in argument to join\n", nil
		}
		return true, "", nil
	})

	var feedbacks []string
	res, err := ex.ExecuteBuildLoop(t.Context(), []FileMutation{
		{File: "worker.go", Content: "package worker\n\nfunc Join(x int, y int) string { return x }\n", TaskID: 1},
	}, func(feedback string, attempt int) ([]FileMutation, error) {
		feedbacks = append(feedbacks, feedback)
		return []FileMutation{
			{File: "worker.go", Content: "package worker\n\nfunc Join(x int, y int) string { return \"fixed\" }\n", TaskID: 1},
		}, nil
	})

	if err != nil {
		t.Fatalf("ExecuteBuildLoop: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}
	if res.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", res.Attempts)
	}
	if len(res.Failures) != 1 {
		t.Errorf("failures = %d, want 1", len(res.Failures))
	}
	if len(feedbacks) != 1 {
		t.Fatalf("generator called %d times, want 1", len(feedbacks))
	}
	// The feedback context must detail what broke, what was attempted, and the
	// classification the self-healing loop derived.
	for _, want := range []string{"TYPE_MISMATCH", "1. WHAT BROKE", "worker.go:3:2", "2. WHAT WAS ATTEMPTED", "FILE: worker.go", "3. INSTRUCTIONS", "Do NOT repeat the same mistake"} {
		if !strings.Contains(feedbacks[0], want) {
			t.Errorf("feedback missing %q:\n%s", want, feedbacks[0])
		}
	}
	if got := mustRead(t, filepath.Join(dir, "worker.go")); !strings.Contains(got, `"fixed"`) {
		t.Errorf("workspace not updated with corrected patch: %q", got)
	}
	if mgr.Open() != 0 {
		t.Errorf("expected all checkpoints committed, Open() = %d", mgr.Open())
	}
	if e.RecoveryCount() != 0 {
		t.Errorf("engine recovery count not reset, got %d", e.RecoveryCount())
	}
}

func TestExecuteBuildLoop_RetriesExhaustedCleanRollback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // v1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e)
	mgr := wschk.NewManager(dir)
	ex.WithCheckpointManager(mgr)
	ex.WithMaxSelfHealingRetries(2) // 3 total attempts

	approve(t, e, "a.go", 1)

	ex.WithVerifyCompilation(func(ctx context.Context, _ ...string) (bool, string, error) {
		return false, "a.go:2:3: syntax error: unexpected EOF\n", nil
	})

	genCalls := 0
	res, err := ex.ExecuteBuildLoop(t.Context(), []FileMutation{
		{File: "a.go", Content: "package a\nfunc broken( {", TaskID: 1},
	}, func(feedback string, attempt int) ([]FileMutation, error) {
		genCalls++
		return []FileMutation{{File: "a.go", Content: "package a\nfunc broken2( {", TaskID: 1}}, nil
	})

	if err == nil {
		t.Fatal("expected error when retries exhausted")
	}
	if !strings.Contains(err.Error(), "self-healing exhausted") {
		t.Errorf("error = %q, want exhaustion marker", err)
	}
	if res.Success {
		t.Fatal("expected failure")
	}
	if res.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", res.Attempts)
	}
	if len(res.Failures) != 3 {
		t.Errorf("failures = %d, want 3", len(res.Failures))
	}
	if genCalls != 2 {
		t.Errorf("generator calls = %d, want 2 (retries only)", genCalls)
	}
	// Clean rollback: workspace returns byte-exact to the original content and
	// no checkpoint is left open.
	if got := mustRead(t, filepath.Join(dir, "a.go")); got != "package a // v1\n" {
		t.Errorf("workspace not cleanly rolled back: %q", got)
	}
	if mgr.Open() != 0 {
		t.Errorf("expected all checkpoints consumed, Open() = %d", mgr.Open())
	}
	// Every attempt must carry a classification and a feedback context.
	for _, af := range res.Failures {
		if af.Classification.Category.String() == "" {
			t.Error("attempt failure missing classification")
		}
		if af.Feedback == "" {
			t.Error("attempt failure missing feedback context")
		}
	}
	report := res.Report()
	for _, want := range []string{"build failed after 3 attempt(s)", "attempt 1 [SYNTAX_ERROR]", "attempt 2 [SYNTAX_ERROR]", "attempt 3 [SYNTAX_ERROR]"} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}

func TestExecuteBuildLoop_EmitsSelfHealingEvents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // v1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	bus := events.NewBus(16)
	defer bus.Close()
	e := NewEngine().WithEventBus(bus)
	ex := NewExecutor(dir, e)
	mgr := wschk.NewManager(dir)
	ex.WithCheckpointManager(mgr)
	ex.WithMaxSelfHealingRetries(1) // 2 total attempts, both fail

	approve(t, e, "a.go", 1)

	var mu sync.Mutex
	var attempts, exhausted, failed []events.DomainEvent
	bus.Subscribe(events.EventSelfHealingAttempt, func(ev events.DomainEvent) {
		mu.Lock()
		attempts = append(attempts, ev)
		mu.Unlock()
	})
	bus.Subscribe(events.EventSelfHealingExhausted, func(ev events.DomainEvent) {
		mu.Lock()
		exhausted = append(exhausted, ev)
		mu.Unlock()
	})
	bus.Subscribe(events.EventExecutionFailed, func(ev events.DomainEvent) {
		mu.Lock()
		failed = append(failed, ev)
		mu.Unlock()
	})

	ex.WithVerifyCompilation(func(ctx context.Context, _ ...string) (bool, string, error) {
		return false, "a.go:2:3: syntax error: unexpected EOF\n", nil
	})

	_, err := ex.ExecuteBuildLoop(t.Context(), []FileMutation{
		{File: "a.go", Content: "package a\nfunc broken( {", TaskID: 1},
	}, func(feedback string, attempt int) ([]FileMutation, error) {
		return []FileMutation{{File: "a.go", Content: "package a\nfunc broken2( {", TaskID: 1}}, nil
	})
	if err == nil {
		t.Fatal("expected exhaustion error")
	}

	waitCount(t, &mu, func() int { return len(attempts) }, 2)
	waitCount(t, &mu, func() int { return len(exhausted) }, 1)
	waitCount(t, &mu, func() int { return len(failed) }, 1)

	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 {
		t.Errorf("self-healing attempt events = %d, want 2", len(attempts))
	}
	if p, ok := attempts[0].Payload().(events.SelfHealingAttemptPayload); ok {
		if p.Retry != 1 || p.File != "a.go" {
			t.Errorf("first attempt payload = %+v", p)
		}
	} else {
		t.Errorf("attempt payload = %T", attempts[0].Payload())
	}
	if p, ok := exhausted[0].Payload().(events.SelfHealingExhaustedPayload); ok {
		if p.Attempts != 2 {
			t.Errorf("exhausted attempts = %d, want 2", p.Attempts)
		}
	} else {
		t.Errorf("exhausted payload = %T", exhausted[0].Payload())
	}
	if p, ok := failed[0].Payload().(events.ExecutionFailedPayload); ok {
		if p.Classification != events.FailureRecoverable || p.Stage != "build.selfheal" {
			t.Errorf("execution failed payload = %+v", p)
		}
	} else {
		t.Errorf("failed payload = %T", failed[0].Payload())
	}
}

func TestExecuteBuildLoop_NoGeneratorSingleGuardedAttempt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // v1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e)
	mgr := wschk.NewManager(dir)
	ex.WithCheckpointManager(mgr)

	approve(t, e, "a.go", 1)

	ex.WithVerifyCompilation(func(ctx context.Context, _ ...string) (bool, string, error) {
		return false, "a.go:2: syntax error\n", nil
	})

	res, err := ex.ExecuteBuildLoop(t.Context(), []FileMutation{
		{File: "a.go", Content: "package a\nfunc broken( {", TaskID: 1},
	}, nil)
	if err == nil {
		t.Fatal("expected failure without generator")
	}
	if res.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no generator → no retry)", res.Attempts)
	}
	if len(res.Failures) != 1 {
		t.Errorf("failures = %d, want 1", len(res.Failures))
	}
	if got := mustRead(t, filepath.Join(dir, "a.go")); got != "package a // v1\n" {
		t.Errorf("workspace not rolled back: %q", got)
	}
}

func TestExecuteBuildLoop_VerifyErrorAborts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // v1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e)
	ex.WithMaxSelfHealingRetries(3)

	approve(t, e, "a.go", 1)

	ex.WithVerifyCompilation(func(ctx context.Context, _ ...string) (bool, string, error) {
		return false, "", errors.New("go: not installed")
	})

	_, err := ex.ExecuteBuildLoop(t.Context(), []FileMutation{
		{File: "a.go", Content: "package a // v2\n", TaskID: 1},
	}, func(feedback string, attempt int) ([]FileMutation, error) {
		t.Fatal("generator must not run after a hard verification error")
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "go: not installed") {
		t.Fatalf("error = %v, want verification error", err)
	}
}

func TestExecuteBuildLoop_ApplyErrorNoRetryWithoutApprovalScope(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // v1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e)
	ex.WithMaxSelfHealingRetries(1)

	// No proposal approved: every mutation is rejected by the guardrail.
	ex.WithVerifyCompilation(func(ctx context.Context, _ ...string) (bool, string, error) {
		return true, "", nil
	})

	res, err := ex.ExecuteBuildLoop(t.Context(), []FileMutation{
		{File: "a.go", Content: "package a // v2\n", TaskID: 1},
	}, func(feedback string, attempt int) ([]FileMutation, error) {
		return []FileMutation{{File: "a.go", Content: "package a // v3\n", TaskID: 1}}, nil
	})
	if err == nil {
		t.Fatal("expected apply failure")
	}
	if !res.Success {
		t.Logf("report:\n%s", res.Report())
	}
	// The workspace must never be touched by unapproved mutations.
	if got := mustRead(t, filepath.Join(dir, "a.go")); got != "package a // v1\n" {
		t.Errorf("unapproved mutation touched disk: %q", got)
	}
}

func TestExecuteBuildLoop_MultipleFilesSuccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a // v1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e)
	mgr := wschk.NewManager(dir)
	ex.WithCheckpointManager(mgr)

	approve(t, e, "a.go", 1)
	approve(t, e, "b.go", 2)

	ex.WithVerifyCompilation(func(ctx context.Context, _ ...string) (bool, string, error) {
		return true, "", nil
	})

	res, err := ex.ExecuteBuildLoop(t.Context(), []FileMutation{
		{File: "a.go", Content: "package a // v2\n", TaskID: 1},
		{File: "b.go", Content: "package a // v3\n", TaskID: 2},
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteBuildLoop: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, report:\n%s", res.Report())
	}
	if got := mustRead(t, filepath.Join(dir, "a.go")); got != "package a // v2\n" {
		t.Errorf("a.go = %q", got)
	}
	if got := mustRead(t, filepath.Join(dir, "b.go")); got != "package a // v3\n" {
		t.Errorf("b.go = %q", got)
	}
	if mgr.Open() != 0 {
		t.Errorf("expected all checkpoints committed, Open() = %d", mgr.Open())
	}
}

func TestExecuteBuildLoop_NoCheckpointManagerBackwardsCompatible(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // v1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e) // no checkpoint manager wired

	approve(t, e, "a.go", 1)

	ex.WithVerifyCompilation(func(ctx context.Context, _ ...string) (bool, string, error) {
		return true, "", nil
	})

	res, err := ex.ExecuteBuildLoop(t.Context(), []FileMutation{
		{File: "a.go", Content: "package a // v2\n", TaskID: 1},
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteBuildLoop without manager: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success without manager, report:\n%s", res.Report())
	}
	if got := mustRead(t, filepath.Join(dir, "a.go")); got != "package a // v2\n" {
		t.Errorf("a.go = %q", got)
	}
}

func TestDefaultConfigMaxSelfHealingRetries(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxSelfHealingRetries != DefaultMaxSelfHealingRetries {
		t.Fatalf("default retries = %d, want %d", cfg.MaxSelfHealingRetries, DefaultMaxSelfHealingRetries)
	}
	e := NewEngine()
	ex := NewExecutor("/tmp", e)
	if ex.maxSelfHealingRetries != DefaultMaxSelfHealingRetries {
		t.Fatalf("executor default retries = %d, want %d", ex.maxSelfHealingRetries, DefaultMaxSelfHealingRetries)
	}
	ex.WithMaxSelfHealingRetries(5)
	if ex.maxSelfHealingRetries != 5 {
		t.Fatalf("WithMaxSelfHealingRetries did not apply: %d", ex.maxSelfHealingRetries)
	}
	ex.WithConfig(Config{MaxSelfHealingRetries: 7})
	if ex.maxSelfHealingRetries != 7 {
		t.Fatalf("WithConfig did not apply: %d", ex.maxSelfHealingRetries)
	}
}

func TestTargetFilesDeduplicates(t *testing.T) {
	files := targetFiles([]FileMutation{
		{File: "a.go"},
		{File: "b.go"},
		{File: "a.go"},
		{File: ""},
	})
	if len(files) != 2 || files[0] != "a.go" || files[1] != "b.go" {
		t.Fatalf("targetFiles = %v, want [a.go b.go]", files)
	}
}

func TestAttemptDiffRendersPatches(t *testing.T) {
	d := attemptDiff([]FileMutation{
		{File: "a.go", Content: "package a\n"},
	})
	if !strings.Contains(d, "FILE: a.go") || !strings.Contains(d, "package a") {
		t.Fatalf("attempt diff = %q", d)
	}
	if d2 := attemptDiff(nil); d2 != "" {
		t.Fatalf("empty diff = %q, want empty", d2)
	}
}

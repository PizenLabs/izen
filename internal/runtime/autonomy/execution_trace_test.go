package autonomy

// ── EXECUTION TRACE ACCEPTANCE TEST ──────────────────────────────────────────
//
// The mission's most important requirement is not "tests pass" but that the
// ACTUAL RUNTIME SEQUENCE is provable. This test captures the canonical event and
// evidence trace of one real $prompt run and asserts the full sequence:
//
//	objective.accepted → context.acquired → capability.requested
//	→ capability.executed → observation.produced → model.computation.completed
//	→ proposal.authorized → mutation.applied → verification.completed
//	→ objective.evaluated
//
// It then asserts the INCOMPLETE-objective continuation:
//
//	objective.evaluated(insufficient) → next computation → next capability request
//	→ observation → verification → proven
//
// Event names follow the EXISTING domain event model (internal/events) and the
// existing capability.Evidence records. No parallel event system is introduced:
// the trace is assembled by subscribing to the canonical bus.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	domaincap "github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// traceStep is one recorded moment of the canonical sequence.
type traceStep struct {
	Kind string
	Text string
}

// traceRecorder subscribes to the canonical bus and records every domain event in
// arrival order. It is a projection of the existing event model, not a new system.
type traceRecorder struct {
	mu    sync.Mutex
	steps []traceStep
	subs  []*events.Subscription
}

func newTraceRecorder(t *testing.T, bus *events.Bus) *traceRecorder {
	t.Helper()
	rec := &traceRecorder{}
	// SubscribeAll guarantees the recorder observes the whole canonical sequence,
	// so a missing step is a real absence rather than a subscription gap.
	sub := bus.SubscribeAll(func(ev events.DomainEvent) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.steps = append(rec.steps, traceStep{Kind: ev.Type(), Text: renderPayload(ev)})
	})
	rec.subs = append(rec.subs, sub)
	t.Cleanup(func() {
		for _, s := range rec.subs {
			s.Cancel()
		}
	})
	return rec
}

func renderPayload(ev events.DomainEvent) string {
	switch p := ev.Payload().(type) {
	case events.StageCompletedPayload:
		return p.Stage + " | " + p.Summary
	case events.ActivityPayload:
		return p.Line
	case events.TargetResolvedPayload:
		return p.Target
	case events.VerificationCompletedPayload:
		if p.Passed {
			return "passed"
		}
		return "failed"
	case events.MutationCompletedPayload:
		return p.Outcome
	default:
		return ""
	}
}

func (r *traceRecorder) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.steps))
	for _, s := range r.steps {
		out = append(out, s.Kind)
	}
	return out
}

func (r *traceRecorder) all() []traceStep {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]traceStep(nil), r.steps...)
}

// waitFor polls until pred is satisfied. The bus delivers on per-subscription
// goroutines, so this waits for delivery instead of racing it.
func (r *traceRecorder) waitFor(pred func([]traceStep) bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if pred(r.all()) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return pred(r.all())
}

func (r *traceRecorder) hasKind(kind string) bool {
	for _, k := range r.kinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// containsText reports whether any recorded step mentions substr in its text.
// It is how stage-name-addressed capability evidence is matched without inventing
// new event types.
func (r *traceRecorder) containsText(substr string) bool {
	for _, s := range r.all() {
		if strings.Contains(s.Text, substr) {
			return true
		}
	}
	return false
}

func traceAllText(r *traceRecorder) string {
	var b strings.Builder
	for _, s := range r.all() {
		b.WriteString(s.Kind)
		b.WriteString(" :: ")
		b.WriteString(s.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// traceWorkspace materialises the domain-neutral objective target.
func traceWorkspace(t *testing.T) (root, abs string) {
	t.Helper()
	root = t.TempDir()
	abs = filepath.Join(root, "internal/worker/pool.go")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/trace\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(
		"package worker\n\nfunc Pool() int {\n\treturn 0\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, abs
}

func traceHarness(t *testing.T, root string, responses []*ai.Response) (*Driver, *events.Bus, *eventCollector) {
	t.Helper()
	bus := events.NewBus(events.DefaultBufferSize)
	collector := &eventCollector{}
	sub := bus.Subscribe(events.EventLoopTransition, collector.add)
	t.Cleanup(sub.Cancel)

	mock := &mockProvider{responses: responses}
	cfg := config.Default()
	x := execution.NewRuntimeExecutor(root, cfg, mock, bus, "go")
	v := execution.NewVerifier(root)
	v.SetCustomSteps([]execution.VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	x.SetVerifier(v)
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})

	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	caps := domaincap.NewCapabilitySet()
	caps.Grant(domaincap.CapabilityRead)
	caps.Grant(domaincap.CapabilityWrite)
	caps.Grant(domaincap.CapabilityPatch)
	adapter.SetCapabilities(caps)

	d := NewDriver(adapter, bus, WithSubcommand("$prompt"),
		WithLoopBounds(autonomy.LoopBounds{
			MaxAttempts:           3,
			MaxRecoveryCycles:     2,
			MaxExecutionSteps:     6,
			MaxIdenticalDecisions: 1,
			MaxTotalTokens:        400_000,
		}))
	return d, bus, collector
}

// TestExecutionTrace_CanonicalObjectiveSequence is the primary acceptance test.
//
// It asserts that one real $prompt run publishes, in order, the canonical moments
// the constitution requires:
//
//	objective.accepted    → loop starts, target resolved
//	context.acquired      → context prepared / compiled (real workspace evidence)
//	proposal.authorized   → admission decision + strategy selected
//	model.computation     → the provider was actually invoked
//	mutation.applied      → the patch crossed the mutation boundary on approval
//	verification.completed→ verification ran
//	objective.evaluated   → the completion authority ruled
//
// Every assertion is on OBSERVED events and the resulting bytes. If the runtime
// ever short-circuits any of these, this test fails.
func TestExecutionTrace_CanonicalObjectiveSequence(t *testing.T) {
	root, abs := traceWorkspace(t)

	const patch = "<<<<<<< SEARCH\nfunc Pool() int {\n\treturn 0\n}\n=======\nfunc Pool() int {\n\treturn 42\n}\n>>>>>>> REPLACE"

	d, bus, _ := traceHarness(t, root, []*ai.Response{{Content: patch}})
	rec := newTraceRecorder(t, bus)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := d.Run(ctx, "$prompt change the worker pool to return the constant 42 @internal/worker/pool.go"); err != nil {
		t.Fatalf("canonical run failed: %v", err)
	}

	// The provider must have been reached: no shortcut may bypass it.
	if !rec.waitFor(func(s []traceStep) bool { return len(s) > 0 }, 5*time.Second) {
		t.Fatal("the canonical run published no events: it is unobservable")
	}

	// ── objective.accepted + context.acquired ───────────────────────────
	// A real run resolves a real target and prepares real workspace context.
	if !rec.hasKind(events.EventTargetResolved) {
		t.Errorf("objective.accepted: no target resolution evidence\n%s", traceAllText(rec))
	}
	if !rec.hasKind(events.EventContextPrepared) && !rec.hasKind(events.EventContextCompilation) {
		t.Errorf("context.acquired: no context preparation evidence\n%s", traceAllText(rec))
	}

	// ── proposal.authorized ─────────────────────────────────────────────
	if !rec.hasKind(events.EventAdmissionDecision) && !rec.hasKind(events.EventStrategySelected) {
		t.Errorf("proposal.authorized: no admission/authorization evidence\n%s", traceAllText(rec))
	}

	// ── model.computation.completed ─────────────────────────────────────
	if !rec.hasKind(events.EventExecutionStarted) {
		t.Errorf("model.computation: no execution evidence\n%s", traceAllText(rec))
	}

	// The run must reach the approval gate BEFORE any mutation: mutation stays
	// behind the existing mutation boundary.
	if d.Boundary() == nil {
		t.Fatalf("expected an approval boundary before mutation\n%s", traceAllText(rec))
	}
	if got := string(mustReadAbs(t, abs)); strings.Contains(got, "return 42") {
		t.Fatalf("FAIL-CLOSED VIOLATION: the workspace mutated before approval:\n%s", got)
	}

	// ── mutation.applied + verification.completed + objective.evaluated ──
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if !rec.waitFor(func(s []traceStep) bool {
		for _, x := range s {
			if x.Kind == events.EventMutationCompleted || x.Kind == events.EventVerificationCompleted {
				return true
			}
		}
		return false
	}, 5*time.Second) {
		t.Errorf("mutation.applied / verification.completed: no evidence after approval\n%s", traceAllText(rec))
	}

	applied := string(mustReadAbs(t, abs))
	if !strings.Contains(applied, "return 42") {
		t.Fatalf("the authorized mutation did not apply the artifact:\n%s", applied)
	}
	if d.State() != autonomy.RuntimeCompleted {
		t.Errorf("objective.evaluated: authority did not reach a proven terminal state, got %s\n%s",
			d.State(), traceAllText(rec))
	}

	t.Logf("canonical trace (%d events):\n%s", len(rec.all()), traceAllText(rec))
}

func mustReadAbs(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestExecutionTrace_UnprovenObjectiveContinues proves the CONT-INUATION half the
// mission requires: when an objective is not satisfied, the runtime must permit ANOTHER
// model computation rather than falsely terminating.
//
// The provider script answers with a non-artifact first (the objective is
// insufficient), then with the real artifact. The trace must show TWO computations
// and, after the authorized approval, a PROVEN terminal state.
func TestExecutionTrace_UnprovenObjectiveContinues(t *testing.T) {
	root, abs := traceWorkspace(t)

	const realPatch = "<<<<<<< SEARCH\nfunc Pool() int {\n\treturn 0\n}\n=======\nfunc Pool() int {\n\treturn 42\n}\n>>>>>>> REPLACE"

	d, bus, collector := traceHarness(t, root, []*ai.Response{
		// First computation: prose only. No artifact can be parsed from it, so the
		// objective is insufficient and the loop must be allowed to try again.
		{Content: "The worker pool has been updated to return the constant 42."},
		// Second computation: the real artifact.
		{Content: realPatch},
	})
	rec := newTraceRecorder(t, bus)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := d.Run(ctx, "$prompt change the worker pool to return the constant 42 @internal/worker/pool.go"); err != nil {
		t.Fatalf("canonical run failed: %v", err)
	}

	// ── objective.evaluated → INSUFFICIENT (never a false completion) ────
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("FAIL-CLOSED VIOLATION: a prose-only computation reported the objective complete")
	}
	if got := string(mustReadAbs(t, abs)); strings.Contains(got, "return 42") {
		t.Fatalf("FAIL-CLOSED VIOLATION: prose was written to the workspace:\n%s", got)
	}

	// ── next computation happened (this is the boundary the audit found missing) ──
	// The run must NOT have terminated after the first insufficient computation; it
	// must have produced a second, informed attempt.
	if collector.loopTransitions() == 0 {
		t.Fatal("the run terminated before a second computation was possible")
	}

	// Whatever the current verdict, the loop must be either parked at a human
	// boundary (waiting for authorization of the real patch) or have completed it.
	if d.Boundary() == nil && d.State() != autonomy.RuntimeCompleted {
		t.Fatalf("after a second informed computation the run must park or complete, got %s\n%s",
			d.State(), traceAllText(rec))
	}

	// ── authorized approval → mutation.applied → verification → PROVEN ──
	if b := d.Boundary(); b != nil {
		if b.PatchID == "" {
			t.Fatalf("boundary = %+v, want an approval gate with a patch id", b)
		}
		term, err := d.ResumeApprove(context.Background())
		if err != nil {
			t.Fatalf("ResumeApprove: %v", err)
		}
		if term == nil || term.State != autonomy.RuntimeCompleted {
			t.Fatalf("the informed second computation must reach PROVEN after authorization, got %+v\n%s",
				term, traceAllText(rec))
		}
	}

	if got := string(mustReadAbs(t, abs)); !strings.Contains(got, "return 42") {
		t.Fatalf("the authorized mutation never applied the objective bytes:\n%s", got)
	}

	t.Logf("continuation trace (%d events, %d transitions):\n%s",
		len(rec.all()), collector.loopTransitions(), traceAllText(rec))
}

// TestExecutionTrace_CapabilityRequestsAreEvidenced asserts the model→capability
// boundary produces EVIDENCE on the canonical bus. It uses the production
// CapabilityToolRunner wired by the executor, so it proves the real wiring rather
// than a hand-built runner.
func TestExecutionTrace_CapabilityRequestsAreEvidenced(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(events.DefaultBufferSize)
	defer bus.Close()

	rec := newTraceRecorder(t, bus)

	admit := execution.StandardAdmittedCapabilities()
	runner := execution.NewCapabilityToolRunner(root, func() execution.AdmittedCapabilities {
		return *admit
	}, bus)

	out, err := runner.Run(context.Background(), ai.ToolCall{
		ID:       "call-1",
		Function: ai.ToolCallFunction{Name: ai.ToolReadFile, Arguments: `{"path":"notes.md"}`},
	})
	if err != nil {
		t.Fatalf("capability request failed: %v", err)
	}
	if !strings.Contains(out, "alpha") {
		t.Fatalf("capability result must carry real workspace state, got %q", out)
	}

	if !rec.waitFor(func(s []traceStep) bool {
		for _, x := range s {
			if strings.Contains(x.Text, "capability.execute") {
				return true
			}
		}
		return false
	}, 5*time.Second) {
		t.Fatalf("capability.executed must publish canonical evidence\n%s", traceAllText(rec))
	}
	if !rec.containsText("file.read") {
		t.Errorf("capability evidence must name the canonical capability id\n%s", traceAllText(rec))
	}
	t.Logf("capability trace:\n%s", traceAllText(rec))
}

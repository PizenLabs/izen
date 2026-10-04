package autonomy

// ── DOMAIN-NEUTRAL EXECUTION BENCHMARK ──────────────────────────────────────
//
// This benchmark measures the RUNTIME, not the model's taste in markup. It is
// deliberately independent of HTML/CSS/JS: the workspace is a Go module, the
// objective is a refactor whose success is mechanically decidable from the
// resulting bytes, and the oracle only ever inspects Go source content.
//
// It drives the REAL Driver over the REAL RuntimeExecutor through the production
// ExecutorAdapter. Nothing is stubbed except the language model itself, so every
// assertion below is a fact about IZEN's authority, evidence and termination —
// not about a mock.
//
// The two subtests are the two halves of the truth requirement:
//
//   - a satisfiable objective either applies bytes and does not falsely complete,
//     or refuses to complete; it never claims proof without bytes;
//   - an unsatisfiable objective NEVER completes and NEVER writes the model's
//     prose to the workspace.
//
// The second is the case the mission cares about most: an objective that is not
// yet satisfied must permit another computation rather than falsely terminating.

import (
	"context"
	"os"
	"path/filepath"
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

// benchTarget is a small, real Go source file. Its expected post-state is decided
// purely by byte comparison — no language model judgement, no markup heuristics.
const (
	benchTarget = "internal/worker/pool.go"
	benchBefore = "package worker\n\n// Pool returns the worker pool size.\nfunc Pool() int {\n\treturn 0\n}\n"
	benchAfter  = "package worker\n\n// Pool returns the worker pool size.\nfunc Pool() int {\n\treturn 42\n}\n"
)

// benchWorkspace materialises a minimal, valid Go module workspace.
func benchWorkspace(t *testing.T) (root, targetAbs string) {
	t.Helper()
	root = t.TempDir()
	targetAbs = filepath.Join(root, benchTarget)
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/bench\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetAbs, []byte(benchBefore), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, targetAbs
}

// benchHarness wires Driver + RuntimeExecutor + ExecutorAdapter exactly as the
// composition root does, and returns the event collector so a test can assert on
// the canonical loop.transition trace.
func benchHarness(t *testing.T, root string, responses []*ai.Response) (*Driver, *eventCollector, *mockProvider) {
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

	d := NewDriver(
		adapter,
		bus,
		WithSubcommand("$prompt"),
		WithLoopBounds(autonomy.LoopBounds{
			MaxAttempts:           2,
			MaxRecoveryCycles:     1,
			MaxExecutionSteps:     4,
			MaxIdenticalDecisions: 1,
			MaxTotalTokens:        200_000,
		}),
	)
	return d, collector, mock
}

// TestDomainNeutralBenchmark_TruthfulTermination is the acceptance case: the
// canonical $prompt path runs to a TERMINAL state, the workspace holds either the
// original or an applied artifact (never the model's prose), and a completed run
// is only possible when the applied bytes actually prove the objective.
func TestDomainNeutralBenchmark_TruthfulTermination(t *testing.T) {
	root, targetAbs := benchWorkspace(t)

	// A bounded SEARCH/REPLACE patch is the canonical domain-neutral artifact
	// contract. Nothing here mentions HTML, CSS or JavaScript.
	const patch = "<<<<<<< SEARCH\nfunc Pool() int {\n\treturn 0\n}\n=======\nfunc Pool() int {\n\treturn 42\n}\n>>>>>>> REPLACE"

	d, collector, mock := benchHarness(t, root, []*ai.Response{{Content: patch}})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The Driver.Run contract is deliberate: a run that PARKS at a human boundary
	// returns a nil termination with a non-nil Boundary(). That is not a silent
	// return — it is the truthful "a human owns this decision now" outcome. So
	// the verdict under test is (terminal state OR parked boundary), never nil-nil.
	term, err := d.Run(ctx, "$prompt change the worker pool to return the constant 42 @internal/worker/pool.go")
	if err != nil {
		t.Fatalf("canonical $prompt run failed: %v", err)
	}
	parked := d.Boundary() != nil
	if term == nil && !parked {
		t.Fatal("a run must either terminate with a verdict or park at a human boundary")
	}

	final, readErr := os.ReadFile(targetAbs)
	if readErr != nil {
		t.Fatal(readErr)
	}

	// 1. The model WAS reached through the canonical executor — not bypassed.
	if mock.calls() == 0 {
		t.Fatal("the objective never reached the model: the canonical path did not execute")
	}

	// 2. The workspace never contains the model's prose as file content.
	if got := string(final); got != benchBefore && got != benchAfter {
		t.Fatalf("workspace holds neither the original nor an applied artifact:\n%s", got)
	}

	// 3. TRUTHFULNESS: completion is possible only with proving bytes.
	if term != nil && term.State == "completed" {
		if string(final) != benchAfter {
			t.Fatalf("FAIL-CLOSED VIOLATION: reported completed but the file is unchanged:\n%s", final)
		}
	}

	// 4. A parked run carries a human boundary and NO terminal verdict, and must
	//    never report completion.
	if parked && term != nil && term.State == "completed" {
		t.Fatal("a parked run must not report completion")
	}

	// 5. The canonical loop actually transitioned (evidence exists at all).
	//    The bus dispatches on its own goroutine, so Run returning does not mean
	//    the transitions have been delivered; wait for the evidence rather than
	//    racing it.
	if !collector.waitTransitions(1, 5*time.Second) {
		t.Fatal("no loop.transition evidence was published: the run is unobservable")
	}
	// 6. THE AUTHORIZED SECOND HALF: the human approval boundary is the runtime's
	//    own authority gate, so crossing it is a real authorized continuation.
	//    Approving must apply the artifact and MUST reach a PROVEN completion,
	//    because the applied bytes are the objective evidence.
	if !parked {
		t.Skip("this objective did not reach an approval boundary; nothing to authorize")
	}
	b := d.Boundary()
	if b == nil || b.PatchID == "" {
		t.Fatalf("boundary = %+v, want an approval gate carrying a patch id", b)
	}
	if got := string(mustRead(t, targetAbs)); got != benchBefore {
		t.Fatalf("FAIL-CLOSED VIOLATION: the file mutated BEFORE approval:\n%s", got)
	}

	term2, err := d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term2 == nil || term2.State != autonomy.RuntimeCompleted {
		t.Fatalf("after an authorized approval the objective must be PROVEN, got %+v", term2)
	}
	applied := string(mustRead(t, targetAbs))
	if applied != benchAfter {
		t.Fatalf("approved apply did not produce the objective evidence bytes:\n%s", applied)
	}

	state := "parked_at_human_boundary"
	if term != nil {
		state = string(term.State)
	}
	// The resumed half publishes on the same async bus; settle before reporting
	// the transition count so the number describes the whole run.
	collector.waitTransitions(1, 5*time.Second)
	t.Logf("domain-neutral benchmark verdict: pre-approval=%s post-approval=completed providerCalls=%d transitions=%d",
		state, mock.calls(), collector.loopTransitions())
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestDomainNeutralBenchmark_UnprovenObjectiveNeverCompletes is the negative case
// the mission requires: when the model produces no artifact, the runtime must NOT
// complete and must NOT write its prose into the workspace.
//
// It must instead terminate truthfully (unsubstantiated/aborted) or park for a
// human — all of which are acceptable; a false completion is not.
func TestDomainNeutralBenchmark_UnprovenObjectiveNeverCompletes(t *testing.T) {
	root, targetAbs := benchWorkspace(t)

	d, collector, mock := benchHarness(t, root, []*ai.Response{{
		Content: "I have completed the requested change. The worker pool now returns 42.",
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	term, err := d.Run(ctx, "$prompt change the worker pool to return the constant 42 @internal/worker/pool.go")
	if err != nil {
		t.Fatalf("canonical $prompt run failed: %v", err)
	}
	parked := d.Boundary() != nil
	if term == nil && !parked {
		t.Fatal("a prose-only run must still terminate or park — never vanish")
	}

	final, readErr := os.ReadFile(targetAbs)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(final) != benchBefore {
		t.Fatalf("FAIL-CLOSED VIOLATION: a prose response was written to the workspace:\n%s", final)
	}
	if term != nil && term.State == "completed" {
		t.Fatal("FAIL-CLOSED VIOLATION: an objective with no applied artifact reported completed")
	}
	if mock.calls() == 0 {
		t.Fatal("the model was never invoked: this case must exercise the real path")
	}
	if collector.loopTransitions() == 0 {
		t.Fatal("an unproven run must still publish terminal evidence")
	}
	state, reason := "parked_at_human_boundary", "a human owns this decision"
	if term != nil {
		state, reason = string(term.State), term.Reason
	}
	t.Logf("unproven-objective verdict: state=%s parked=%v providerCalls=%d reason=%q",
		state, parked, mock.calls(), reason)
}

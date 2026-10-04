package kernel_test

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/runtime/kernel"
)

// These tests pin the property the whole kernel design rests on: execution truth
// is a pure fold over a durable event log. If that is true, the same log always
// rebuilds the same state, and a log is sufficient to reconstruct an execution
// without trusting any in-memory structure that happened to be alive at the time.

// TestReplayRebuildsIdenticalState is the central determinism test.
//
// It runs the same execution twice, compares the two settled states field by
// field, and then reconstructs the state from the event log alone and compares
// that too. Three independent routes to the same answer is what "deterministic
// where authority is required" has to mean in practice.
func TestReplayRebuildsIdenticalState(t *testing.T) {
	first := runAndSettle(t, "replay-1", "notes.md")
	second := runAndSettle(t, "replay-2", "notes.md")

	if first.Revision != second.Revision {
		t.Errorf("two identical executions settled at revisions %d and %d; "+
			"an identical event sequence must produce an identical revision", first.Revision, second.Revision)
	}
	if first.Outcome != second.Outcome {
		t.Errorf("two identical executions settled differently: %s and %s", first.Outcome, second.Outcome)
	}
	if first.Outcome != kernel.OutcomeProven {
		t.Fatalf("fixture did not PROVEN, so the comparison proves nothing: %s", first.Reason)
	}

	// Reconstruct from the log. Fold starts from the admitted state and replays
	// every event; the result must equal the state the engine held.
	engine := newTestEngine(t, alwaysPass, mutating("notes.md", "hello"))
	spec := createSpec("replay-3", "notes.md", "hello")
	if err := engine.Open(spec, fullGrant(t, "g", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	engine.Run(context.Background())

	settled := engine.State()
	if !settled.Status.Terminal() {
		t.Fatal("the engine did not settle")
	}

	// Replay starts from the admitted state, which is the only defined starting
	// point for a log. Folding onto the settled state would be replaying events
	// that already happened, which the reducer correctly refuses.
	admitted := kernel.Initial(spec, fullGrant(t, "g", "notes.md"))
	rebuilt, err := kernel.Fold(admitted, engine.Log().Events())
	if err != nil {
		t.Fatalf("Fold failed on a log the engine itself produced: %v", err)
	}

	if rebuilt.Revision != settled.Revision {
		t.Errorf("replayed revision = %d, want %d", rebuilt.Revision, settled.Revision)
	}
	if rebuilt.Terminal.Outcome != settled.Terminal.Outcome {
		t.Errorf("replayed outcome = %q, want %q", rebuilt.Terminal.Outcome, settled.Terminal.Outcome)
	}
	if len(rebuilt.Evidence) != len(settled.Evidence) {
		t.Fatalf("replayed evidence count = %d, want %d", len(rebuilt.Evidence), len(settled.Evidence))
	}
	for i := range rebuilt.Evidence {
		got, want := rebuilt.Evidence[i], settled.Evidence[i]
		if got.Kind != want.Kind || got.Target != want.Target || got.Step != want.Step || got.Revision != want.Revision {
			t.Errorf("replayed evidence %d = %+v, want %+v", i, got, want)
		}
	}
	if !sameStringSlice(rebuilt.MutatedTargets(), settled.MutatedTargets()) {
		t.Errorf("replayed mutated targets = %v, want %v", rebuilt.MutatedTargets(), settled.MutatedTargets())
	}
	if rebuilt.Mutation != settled.Mutation {
		t.Errorf("replayed mutation axis = %q, want %q", rebuilt.Mutation, settled.Mutation)
	}
	if rebuilt.Verify != settled.Verify {
		t.Errorf("replayed verify axis = %q, want %q", rebuilt.Verify, settled.Verify)
	}
	if rebuilt.Budget.StepsInvoked != settled.Budget.StepsInvoked {
		t.Errorf("replayed steps charged = %d, want %d", rebuilt.Budget.StepsInvoked, settled.Budget.StepsInvoked)
	}
}

// TestFoldRejectsAnInvalidLog proves a log that cannot be folded is reported as
// such rather than folded as far as possible.
//
// A partial fold that returns a plausible-looking state is the most dangerous
// failure mode for a durable log, because the caller cannot tell it is wrong.
func TestFoldRejectsAnInvalidLog(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, mutating("notes.md", "hello"))
	spec := createSpec("fold-1", "notes.md", "hello")
	if err := engine.Open(spec, fullGrant(t, "g", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	engine.Run(context.Background())
	admitted := kernel.Initial(spec, fullGrant(t, "g", "notes.md"))
	events := engine.Log().Events()

	tests := []struct {
		name   string
		mutate func([]kernel.Event) []kernel.Event
	}{
		{
			name: "out of order start",
			mutate: func(e []kernel.Event) []kernel.Event {
				if len(e) < 2 {
					return e
				}
				e[0], e[1] = e[1], e[0]
				return e
			},
		},
		{
			name: "dropped middle event",
			mutate: func(e []kernel.Event) []kernel.Event {
				if len(e) < 2 {
					return e
				}
				return append(append([]kernel.Event{}, e[:1]...), e[2:]...)
			},
		},
		{
			name: "verification verdict without a started check",
			mutate: func(e []kernel.Event) []kernel.Event {
				out := make([]kernel.Event, 0, len(e))
				for _, ev := range e {
					if ev.Kind == kernel.EventVerificationStarted {
						continue
					}
					out = append(out, ev)
				}
				return out
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := kernel.Fold(admitted, tc.mutate(append([]kernel.Event(nil), events...))); err == nil {
				t.Fatal("Fold accepted an invalid log; " +
					"a log that cannot be folded is not a record of an execution")
			}
		})
	}
}

// TestFoldOfAnEmptyLogIsIdentity proves folding nothing changes nothing, which is
// what makes a log resumable from any point.
func TestFoldOfAnEmptyLogIsIdentity(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, mutating("notes.md", "hello"))
	spec := createSpec("fold-empty", "notes.md", "hello")
	if err := engine.Open(spec, fullGrant(t, "g", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	before := engine.State()
	after, err := kernel.Fold(before, nil)
	if err != nil {
		t.Fatalf("Fold(nil): %v", err)
	}
	if after.Revision != before.Revision || after.Status != before.Status {
		t.Errorf("folding an empty log changed the state: %+v -> %+v", before, after)
	}
}

// TestEventLogSerialisationIsStable proves the durable form is diffable.
//
// A log whose bytes differ between two identical executions could not be
// checksummed, and a checksum over a log is what makes tampering detectable.
func TestEventLogSerialisationIsStable(t *testing.T) {
	// Both runs use the SAME execution id, so every byte of every record must
	// match. If they do not, the log cannot be checksummed and a checksum over it
	// would not detect tampering.
	encode := func() []string {
		engine := newTestEngine(t, alwaysPass, mutating("notes.md", "hello"))
		spec := createSpec("stable-id", "notes.md", "hello")
		if err := engine.Open(spec, fullGrant(t, "g", "notes.md")); err != nil {
			t.Fatalf("Open: %v", err)
		}
		engine.Run(context.Background())
		events := engine.Log().Events()
		out := make([]string, 0, len(events))
		for _, ev := range events {
			raw, err := ev.JSON()
			if err != nil {
				t.Fatalf("encode %s: %v", ev.Kind, err)
			}
			out = append(out, string(raw))
		}
		return out
	}

	a, b := encode(), encode()
	if len(a) != len(b) {
		t.Fatalf("two identical executions produced %d and %d events", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("event %d serialised differently:\n  %s\n  %s", i, a[i], b[i])
		}
	}
}

// TestSummaryReportsEachAxis proves the one-line state summary stays truthful
// and keeps the axes separate.
func TestSummaryReportsEachAxis(t *testing.T) {
	state := runAndSettle(t, "summary-1", "notes.md").State
	summary := state.Summary()
	for _, want := range []string{
		"provider=" + string(kernel.ProviderDone),
		"artifact=" + string(kernel.ArtifactNone),
		"mutation=" + string(kernel.MutationApplied),
		"verify=" + string(kernel.VerifyPassed),
		"outcome=" + string(kernel.OutcomeProven),
	} {
		if !contains(summary, want) {
			t.Errorf("summary missing %q\nsummary: %s", want, summary)
		}
	}
}

// runAndSettle runs a real create execution and returns its settled result.
func runAndSettle(t *testing.T, executionID, target string) kernel.Result {
	t.Helper()
	engine := newTestEngine(t, alwaysPass, mutating(target, "hello"))
	if err := engine.Open(createSpec(executionID, target, "hello"), fullGrant(t, "g", target)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return engine.Run(context.Background())
}

// createSpec builds a minimal satisfiable CREATE program.
func createSpec(executionID, target, content string) kernel.Spec {
	return kernel.Spec{
		ExecutionID: executionID,
		Objective:   "create " + target,
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{target},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     target,
			Args:       map[string]string{"content": content},
		}},
	}
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// TestTailTruncationIsNotDetectable documents a real limitation rather than
// pretending it does not exist.
//
// An interior hole in the log is detectable: the revision sequence has a gap, and
// Fold refuses. A MISSING TAIL is not, because nothing in the surviving prefix
// says how long the log was supposed to be. A log truncated immediately after the
// last durable record is indistinguishable from a log of an execution that simply
// had not finished yet — which is in fact the correct reading.
//
// This is why the log is a write-ahead record rather than a sealed artifact. A
// reader who needs to prove an execution COMPLETED must read the terminal event
// itself, not merely the absence of an error. Closing this gap would mean sealing
// each log with a signed terminator; that is a deliberate future step, recorded
// here so nobody assumes the guarantee already exists.
func TestTailTruncationIsNotDetectable(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, mutating("notes.md", "hello"))
	spec := createSpec("tail-1", "notes.md", "hello")
	if err := engine.Open(spec, fullGrant(t, "g", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	settled := engine.Run(context.Background())
	if !settled.Proves() {
		t.Fatalf("fixture did not PROVEN: %s", settled.Reason)
	}

	events := engine.Log().Events()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	truncated := events[:len(events)-1] // drop the terminal event

	admitted := kernel.Initial(spec, fullGrant(t, "g", "notes.md"))
	rebuilt, err := kernel.Fold(admitted, truncated)
	if err != nil {
		t.Skipf("a truncated log was rejected, so the limitation no longer applies: %v", err)
	}
	// The honest expectation: the truncated log folds, and the state is NOT
	// settled. A reader must notice the absence of terminal truth rather than
	// inheriting a completion.
	if rebuilt.Status.Terminal() {
		t.Fatal("a truncated log replayed as settled; a reader could mistake it for a completed execution")
	}
}

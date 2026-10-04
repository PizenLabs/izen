package kernelbridge_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/kernelbridge"
	"github.com/PizenLabs/izen/runtime/kernel"
)

// TestObserve_ProvesPresentTarget drives the complete chain the kernel exists to
// guarantee — event → state → evidence → verification → PROVEN — through the
// bridge a legacy caller actually uses.
//
// Each assertion below is a separate link in that chain. Asserting only the final
// outcome would let every intermediate be removed and the test would still pass,
// which is exactly the kind of "it works" claim that hides a broken runtime.
func TestObserve_ProvesPresentTarget(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "present.go"), "package present\n")

	obs := kernelbridge.Observe(context.Background(), root, []string{"present.go"})

	// ── Terminal truth ──────────────────────────────────────────────────
	if !obs.Proven() {
		t.Fatalf("outcome = %s (%s): %s; want PROVEN", obs.Outcome, obs.Class, obs.Reason)
	}
	if obs.Outcome != kernel.OutcomeProven {
		t.Errorf("Outcome = %s; want %s", obs.Outcome, kernel.OutcomeProven)
	}
	if !obs.Exists("present.go") {
		t.Errorf("Exists(present.go) = false; want true on a PROVEN observation")
	}
	if obs.Absent("present.go") {
		t.Errorf("Absent(present.go) = true; want false for an existing target")
	}

	// ── Verification was a real pass, not a skip ─────────────────────────
	if obs.Verify != kernel.VerifyPassed {
		t.Errorf("Verify axis = %s; want %s (a contract requiring verification must not settle on a skip)",
			obs.Verify, kernel.VerifyPassed)
	}

	// ── Evidence: a named observation about the exact target ─────────────
	found := false
	for _, e := range obs.Evidence {
		if e.Kind != kernel.EvidenceFilePresent {
			continue
		}
		found = true
		if e.Target != "present.go" {
			t.Errorf("evidence target = %q; want %q", e.Target, "present.go")
		}
		if e.Capability != kernel.FileExists {
			t.Errorf("evidence capability = %q; want %q", e.Capability, kernel.FileExists)
		}
		if e.Step == "" {
			t.Error("evidence carries no step attribution")
		}
	}
	if !found {
		t.Fatalf("no FILE_PRESENT evidence in %d records: %v", len(obs.Evidence), obs.Evidence)
	}

	// ── State: the four axes, reported separately ───────────────────────
	//
	// The provider axis reads DONE because the invocation settled. That is a
	// transport fact and nothing more: the artifact and mutation axes stay empty,
	// which is exactly why a settled invocation cannot on its own authorize a
	// completion claim.
	state := obs.State
	if state.Status != kernel.StatusSettled {
		t.Errorf("state status = %s; want %s", state.Status, kernel.StatusSettled)
	}
	if state.Provider != kernel.ProviderDone {
		t.Errorf("provider axis = %s; want %s (the capability invocation settled)", state.Provider, kernel.ProviderDone)
	}
	if state.Artifact != kernel.ArtifactNone {
		t.Errorf("artifact axis = %s; want %s (a workspace observation produces no artifact)",
			state.Artifact, kernel.ArtifactNone)
	}
	if state.Mutation != kernel.MutationNone {
		t.Errorf("mutation axis = %s; want %s (an observation must never claim a write)",
			state.Mutation, kernel.MutationNone)
	}

	// ── Events: the ordered chain, with no gap and no repeat ────────────
	assertEventChain(t, obs.Events, "present.go")
}

// TestObserve_ProvesAbsentTarget is the case the old os.Stat path could not
// express. "The target does not exist" is a real observation, so it carries real
// evidence and reaches PROVEN — the contract was to report the workspace
// truthfully, and a truthful report of absence satisfies it.
//
// What it must never become is UNSUBSTANTIATED, which is what would happen if
// absence were treated as a missing fact.
func TestObserve_ProvesAbsentTarget(t *testing.T) {
	root := t.TempDir()

	obs := kernelbridge.Observe(context.Background(), root, []string{"nowhere.go"})

	if !obs.Proven() {
		t.Fatalf("outcome = %s (%s): %s; a truthful report of absence must be PROVEN",
			obs.Outcome, obs.Class, obs.Reason)
	}
	if !obs.Absent("nowhere.go") {
		t.Error("Absent(nowhere.go) = false; want true — the target was looked for and not found")
	}
	if obs.Exists("nowhere.go") {
		t.Error("Exists(nowhere.go) = true; want false")
	}

	var sawAbsent bool
	for _, e := range obs.Evidence {
		if e.Kind == kernel.EvidenceFileAbsent && e.Target == "nowhere.go" {
			sawAbsent = true
		}
	}
	if !sawAbsent {
		t.Errorf("no FILE_ABSENT evidence for nowhere.go; absence must be named, not inferred from silence")
	}
	if obs.Verify != kernel.VerifyPassed {
		t.Errorf("Verify axis = %s; want %s", obs.Verify, kernel.VerifyPassed)
	}
}

// TestObserve_UnprovenTargetIsNotAbsent is the fail-closed guarantee: when the
// runtime cannot answer, it must not answer "no".
func TestObserve_UnprovenTargetIsNotAbsent(t *testing.T) {
	// A workspace root that does not exist cannot build a capability surface,
	// so the observation never reaches adjudication.
	obs := kernelbridge.Observe(context.Background(), filepath.Join(t.TempDir(), "no-such-root"), []string{"x.go"})

	if obs.Proven() {
		t.Fatalf("Proven = true for an unusable workspace root; outcome = %s", obs.Outcome)
	}
	if obs.Outcome.Terminal() != true {
		t.Errorf("Outcome = %s; want a terminal closed-vocabulary outcome", obs.Outcome)
	}
	if obs.Exists("x.go") {
		t.Error("Exists(x.go) = true for an unproven observation")
	}
	if obs.Absent("x.go") {
		t.Error("Absent(x.go) = true for an unproven observation: an unanswered question was reported as a negative answer")
	}
}

// TestObserve_RefusesTargetEscapingTheWorkspace proves the grant is a real
// authority rather than a formality: a target outside the root is refused by the
// capability's confinement, and the refusal never becomes a "no".
func TestObserve_RefusesTargetEscapingTheWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "escape.go")
	writeFile(t, outside, "package escape\n")

	obs := kernelbridge.Observe(context.Background(), root, []string{"../escape.go"})

	if obs.Exists("../escape.go") || obs.Absent("../escape.go") {
		t.Fatalf("an out-of-workspace target produced a presence verdict: %+v", obs.Presence)
	}
	if obs.Proven() {
		t.Errorf("Proven = true for a target outside the workspace root; outcome = %s (%s): %s",
			obs.Outcome, obs.Class, obs.Reason)
	}
}

// TestObserve_BoundsTheTargetSet keeps an oversized request from turning into a
// program nobody bounded.
func TestObserve_BoundsTheTargetSet(t *testing.T) {
	root := t.TempDir()
	targets := make([]string, kernelbridge.MaxObservedTargets+1)
	for i := range targets {
		// Distinct paths, so the set is not collapsed by canonicalisation before
		// it ever reaches the declared bound.
		targets[i] = filepath.Join("d", fmt.Sprintf("%04d", i), "f.keep")
	}

	obs := kernelbridge.Observe(context.Background(), root, targets)

	if obs.Outcome != kernel.OutcomeBudgetExhausted {
		t.Errorf("Outcome = %s; want %s for an oversized target set", obs.Outcome, kernel.OutcomeBudgetExhausted)
	}
	if obs.Proven() {
		t.Error("Proven = true for an oversized target set")
	}
}

// TestObserve_EmptyTargetSetProvesNothing pins the rule that "all of nothing
// exists" is not evidence.
func TestObserve_EmptyTargetSetProvesNothing(t *testing.T) {
	obs := kernelbridge.Observe(context.Background(), t.TempDir(), nil)

	if obs.Proven() {
		t.Error("Proven = true for an empty target set")
	}
	if obs.Outcome != kernel.OutcomeUnsubstantiated {
		t.Errorf("Outcome = %s; want %s", obs.Outcome, kernel.OutcomeUnsubstantiated)
	}
}

// TestObserve_CanonicalisesTargets proves evidence, contract and lookup all speak
// one spelling of a path, so a caller that asked about "./a.go" and a contract
// keyed on "a.go" still agree.
func TestObserve_CanonicalisesTargets(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "package a\n")

	obs := kernelbridge.Observe(context.Background(), root, []string{"./a.go", "a.go"})

	if !obs.Exists("a.go") || !obs.Exists("./a.go") {
		t.Fatalf("canonical lookups disagreed: %+v", obs.Presence)
	}
	if len(obs.Targets) != 1 {
		t.Errorf("Targets = %v; want the duplicate collapsed to one", obs.Targets)
	}
}

// TestObserve_ExecutionIDIsDeterministic keeps a log line correlatable with the
// resolution that produced it, without the bridge keeping any state.
func TestObserve_ExecutionIDIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "package a\n")

	first := kernelbridge.Observe(context.Background(), root, []string{"a.go"})
	second := kernelbridge.Observe(context.Background(), root, []string{"a.go"})
	other := kernelbridge.Observe(context.Background(), root, []string{"b.go"})

	if first.ExecutionID != second.ExecutionID {
		t.Errorf("execution id is not deterministic: %q vs %q", first.ExecutionID, second.ExecutionID)
	}
	if first.ExecutionID == other.ExecutionID {
		t.Errorf("different questions share execution id %q", first.ExecutionID)
	}
	if first.ExecutionID == "" {
		t.Error("execution id is empty; an execution nobody can name cannot be audited")
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// assertEventChain checks the ordered spine of one execution: the lifecycle
// opens, every declared step is announced and accounted for, evidence is
// produced, verification runs and settles, and the execution closes.
func assertEventChain(t *testing.T, events []kernel.Event, target string) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("event log is empty; an execution with no events cannot be replayed")
	}

	wantKinds := []kernel.EventKind{
		kernel.EventExecutionStarted,
		kernel.EventStepStarted,
		kernel.EventCapabilityInvoked,
		kernel.EventEvidenceProduced,
		kernel.EventVerificationStarted,
		kernel.EventVerificationPassed,
		kernel.EventExecutionFinished,
	}

	positions := make(map[kernel.EventKind]int, len(wantKinds))
	var order []kernel.EventKind
	for i, e := range events {
		if _, seen := positions[e.Kind]; !seen {
			positions[e.Kind] = i
			order = append(order, e.Kind)
		}
	}

	for _, want := range wantKinds {
		if _, ok := positions[want]; !ok {
			t.Errorf("event log has no %s event; chain was %v", want, order)
		}
	}

	// The spine must be monotonic: verification may only start after evidence
	// exists, and the execution may only finish after verification settles.
	assertOrdered(t, events, positions,
		kernel.EventExecutionStarted,
		kernel.EventStepStarted,
		kernel.EventEvidenceProduced,
		kernel.EventVerificationStarted,
		kernel.EventVerificationPassed,
		kernel.EventExecutionFinished,
	)

	// Revisions must be strictly increasing by one: a gap means a consumer would
	// silently skip a transition, and a repeat means the same transition was
	// folded twice.
	for i, e := range events {
		if e.ExecutionID == "" {
			t.Errorf("event %d (%s) carries no execution id", i, e.Kind)
		}
		if i > 0 && e.Revision != events[i-1].Revision+1 {
			t.Errorf("revision jumped from %d to %d at %s; the log is not contiguous",
				events[i-1].Revision, e.Revision, e.Kind)
		}
	}

	// The step's target must be the exact path that was asked about, on every
	// event that names one.
	for i, e := range events {
		if e.Target != "" && e.Target != target {
			t.Errorf("event %d (%s) targets %q; want %q", i, e.Kind, e.Target, target)
		}
	}
}

func assertOrdered(t *testing.T, events []kernel.Event, positions map[kernel.EventKind]int, chain ...kernel.EventKind) {
	t.Helper()
	prev := -1
	prevKind := kernel.EventKind("")
	for _, kind := range chain {
		at, ok := positions[kind]
		if !ok {
			continue
		}
		if at <= prev {
			t.Errorf("%s occurs at position %d, at or before %s at %d: the chain is out of order",
				kind, at, prevKind, prev)
			return
		}
		prev, prevKind = at, kind
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if !strings.HasSuffix(path, ".go") {
		t.Fatalf("test fixture %q is not a .go file; the capability set is chosen per path", path)
	}
}

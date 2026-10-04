package autonomy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// ledgerEvents decodes the journal on disk. The wiring tests assert against
// the FILE, not against in-memory accessors: a transition the driver claimed
// to record but never wrote must fail here.
func ledgerEvents(t *testing.T, path string) []durable.LedgerEvent {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var out []durable.LedgerEvent
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev durable.LedgerEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode ledger line %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func eventTypes(events []durable.LedgerEvent) []durable.EventType {
	out := make([]durable.EventType, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.EventType)
	}
	return out
}

func hasEvent(events []durable.LedgerEvent, typ durable.EventType) bool {
	for _, ev := range events {
		if ev.EventType == typ {
			return true
		}
	}
	return false
}

func openLedger(t *testing.T, root string) *durable.TaskStore {
	t.Helper()
	store := durable.NewTaskStore(root)
	if err := store.Open(); err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	return store
}

// TestDriverLedgerRecordsTheRealRunLifecycle is the production-wiring proof:
// with a ledger bound, a real driver run — execute, park at the approval gate,
// human approves — leaves a durable record whose shape matches what actually
// happened, and the task ends up retired only once the objective really
// completed.
func TestDriverLedgerRecordsTheRealRunLifecycle(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	store := openLedger(t, root)
	d := NewDriver(a, nil, WithLedger(store, "session-1"))

	term, err := d.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated early: %+v, want parked at approval", term)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}

	taskID := d.ledgerTaskID("change bar to qux @note.txt")
	events := ledgerEvents(t, store.LedgerPath())
	if len(events) == 0 {
		t.Fatal("a bound ledger recorded nothing for a real run")
	}
	if events[0].EventType != durable.EventTaskCreated {
		t.Fatalf("first journal event = %s, want TASK_CREATED (types: %v)", events[0].EventType, eventTypes(events))
	}
	if events[0].TaskID != taskID {
		t.Fatalf("TASK_CREATED task id = %q, want %q", events[0].TaskID, taskID)
	}
	for i, ev := range events {
		if ev.Sequence != uint64(i+1) {
			t.Fatalf("event[%d] sequence = %d, want %d — the run's own journal is not monotonic", i, ev.Sequence, i+1)
		}
	}
	// The parked run is not finished: it must still be recoverable.
	st, ok := store.State(taskID)
	if !ok {
		t.Fatalf("no durable task %q after the run", taskID)
	}
	if st.Status != durable.TaskPaused {
		t.Fatalf("parked task status = %s, want PAUSED", st.Status)
	}
	if _, ok := store.MostRecentRecoverable(); !ok {
		t.Fatal("a run parked at a human boundary must remain recoverable")
	}
	if !hasEvent(events, durable.EventTaskPaused) {
		t.Fatalf("journal has no TASK_PAUSED (types: %v)", eventTypes(events))
	}
	if !hasEvent(events, durable.EventCheckpointCreated) {
		t.Fatalf("journal has no CHECKPOINT_CREATED at the loop boundary (types: %v)", eventTypes(events))
	}

	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if d.State() != autonomy.RuntimeCompleted {
		t.Fatalf("state after approve = %s, want completed", d.State())
	}

	events = ledgerEvents(t, store.LedgerPath())
	if !hasEvent(events, durable.EventExecutionCommitted) {
		t.Fatalf("a completed objective has no EXECUTION_COMMITTED (types: %v)", eventTypes(events))
	}
	st, ok = store.State(taskID)
	if !ok || st.Status != durable.TaskCompleted {
		t.Fatalf("task after approval = %+v (ok=%v), want COMPLETED", st, ok)
	}
	// A finished objective must NOT be offered back to the next process as
	// unfinished work.
	if _, ok := store.MostRecentRecoverable(); ok {
		t.Fatal("a completed objective must not remain recoverable")
	}
	if rec := store.RecoverableTasks(); len(rec) != 0 {
		t.Fatalf("RecoverableTasks = %+v, want none after completion", rec)
	}
}

// TestDriverLedgerAbortsAreTerminalNotRecoverable proves the other terminal
// boundary: a human rejection is a real, classified termination, not unfinished
// work the next process should offer back.
func TestDriverLedgerAbortsAreTerminalNotRecoverable(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	store := openLedger(t, root)
	d := NewDriver(a, nil, WithLedger(store, "session-1"))

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := d.ResumeReject(context.Background(), "not wanted"); err != nil {
		t.Fatalf("ResumeReject: %v", err)
	}
	events := ledgerEvents(t, store.LedgerPath())
	if !hasEvent(events, durable.EventFailureClassified) {
		t.Fatalf("an aborted run recorded no FAILURE_CLASSIFIED (types: %v)", eventTypes(events))
	}
	st, ok := store.State(d.ledgerTaskID("change bar to qux @note.txt"))
	if !ok || st.Status != durable.TaskFailed {
		t.Fatalf("task after rejection = %+v (ok=%v), want FAILED", st, ok)
	}
	if _, ok := store.MostRecentRecoverable(); ok {
		t.Fatal("a failed objective must not remain recoverable")
	}
}

// TestDriverLedgerContinuesAnInterruptedTaskInsteadOfDuplicatingIt is the §15
// contract: re-running the SAME objective after an interruption continues the
// SAME durable record rather than minting a competing one that erases the
// interrupted run's history.
func TestDriverLedgerContinuesAnInterruptedTaskInsteadOfDuplicatingIt(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	store := openLedger(t, root)
	d := NewDriver(a, nil, WithLedger(store, "session-1"))
	const objective = "change bar to qux @note.txt"

	if _, err := d.Run(context.Background(), objective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	taskID := d.ledgerTaskID(objective)
	first, ok := store.State(taskID)
	if !ok {
		t.Fatalf("no durable task after the first run")
	}

	// Simulate a process that died mid-flight: reopen the journal as a fresh
	// store and start the same objective again.
	reopened := openLedger(t, root)
	d2 := NewDriver(a, nil, WithLedger(reopened, "session-1"))
	if _, err := d2.Run(context.Background(), objective); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := d2.ledgerTaskID(objective); got != taskID {
		t.Fatalf("second run used task %q, want the continued %q", got, taskID)
	}
	second, ok := reopened.State(taskID)
	if !ok {
		t.Fatal("the continued task vanished from the journal")
	}
	if second.ID != first.ID {
		t.Fatalf("a second durable task was minted for the same objective: %q vs %q", second.ID, first.ID)
	}
	// One task, not two.
	if rec := reopened.RecoverableTasks(); len(rec) != 1 {
		t.Fatalf("RecoverableTasks = %+v, want exactly one continued task", rec)
	}
}

// TestDriverLedgerSessionIdentityKeepsObjectivesApart proves the task key is
// scoped by session: the same objective in two sessions is two records, never
// one interleaved mess.
func TestDriverLedgerSessionIdentityKeepsObjectivesApart(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	store := openLedger(t, root)
	const objective = "change bar to qux @note.txt"

	first := NewDriver(a, nil, WithLedger(store, "session-a"))
	second := NewDriver(a, nil, WithLedger(store, "session-b"))
	if first.ledgerTaskID(objective) == second.ledgerTaskID(objective) {
		t.Fatal("two sessions produced the same durable task id")
	}
	if NewDriver(a, nil, WithLedger(store, "session-a")).ledgerTaskID(objective) != first.ledgerTaskID(objective) {
		t.Fatal("the task id is not stable for the same session and objective")
	}
}

// TestDriverLedgerProvenanceMirrorsTheUIGrant proves the durable task inherits
// the grant the human bound — from the same parser the IntentGateway and the
// UI use — and never a provenance value invented by the driver.
func TestDriverLedgerProvenanceMirrorsTheUIGrant(t *testing.T) {
	d := NewDriver(nil, nil)
	for _, tc := range []struct {
		objective string
		want      domain.ScopeProvenance
	}{
		{"$prompt change bar to qux @note.txt", domain.ScopeDynamic},
		{"$hot change bar to qux @note.txt", domain.ScopeDeclared},
		{"change bar to qux @note.txt", domain.ScopeNone},
	} {
		t.Run(tc.objective, func(t *testing.T) {
			if got := d.ledgerProvenance(tc.objective); got != tc.want {
				t.Fatalf("ledgerProvenance(%q) = %v, want %v", tc.objective, got, tc.want)
			}
		})
	}
}

// TestDriverLedgerRecordsTheGrantOnTheDurableTask closes the loop on the
// provenance question: the grant the UI bound is what lands in the journal,
// not merely what a helper returns.
func TestDriverLedgerRecordsTheGrantOnTheDurableTask(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	store := openLedger(t, root)
	d := NewDriver(a, nil, WithLedger(store, "session-1"))

	const objective = "$prompt change bar to qux @note.txt"
	if _, err := d.Run(context.Background(), objective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	st, ok := store.State(d.ledgerTaskID(objective))
	if !ok {
		t.Fatal("no durable task after the run")
	}
	if st.ScopeProvenance != domain.ScopeDynamic {
		t.Fatalf("durable task provenance = %v, want ScopeDynamic ($prompt)", st.ScopeProvenance)
	}
	if st.Intent != objective {
		t.Fatalf("durable task intent = %q, want the objective", st.Intent)
	}
}

// TestDriverWithoutLedgerBehavesExactlyAsBefore proves the binding is inert
// when absent: the run's observable behaviour is unchanged and no journal is
// created at all.
func TestDriverWithoutLedgerBehavesExactlyAsBefore(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	// Explicitly binding a nil store must be indistinguishable from never
	// calling WithLedger at all.
	d := NewDriver(a, nil, WithLedger(nil, "session-1"))

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}
	if d.ledgerTask != "" {
		t.Fatalf("a nil-ledger driver claimed task %q", d.ledgerTask)
	}
	if readTarget(t, root, "note.txt") != sampleOriginal {
		t.Fatal("file mutated before approval")
	}
	term, err := d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination after approve = %+v, want completed", term)
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", mock.calls())
	}
	if _, err := os.Stat(filepath.Join(root, ".izen", "runtime", "ledger.ndjson")); !os.IsNotExist(err) {
		t.Fatalf("a nil-ledger driver must not create a journal (stat err = %v)", err)
	}
}

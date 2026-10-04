package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/app"
	"github.com/PizenLabs/izen/internal/ir"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// The assertions below read the ledger FILE and the snapshot FILE, never log
// strings: a transition the command claimed to record but never wrote must fail
// here.

func readLedgerEvents(t *testing.T, dir string) []durable.LedgerEvent {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".izen", "runtime", "ledger.ndjson"))
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

func readSnapshot(t *testing.T, dir string) durable.SnapshotFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".izen", "runtime", "snapshot.json"))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snap durable.SnapshotFile
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return snap
}

func ledgerTypes(events []durable.LedgerEvent) []durable.EventType {
	out := make([]durable.EventType, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.EventType)
	}
	return out
}

func hasLedgerEvent(events []durable.LedgerEvent, typ durable.EventType) bool {
	for _, ev := range events {
		if ev.EventType == typ {
			return true
		}
	}
	return false
}

func payloadString(ev durable.LedgerEvent, key string) string {
	v, _ := ev.Payload[key].(string)
	return v
}

func ledgerEventFor(t *testing.T, events []durable.LedgerEvent, taskID string, typ durable.EventType) durable.LedgerEvent {
	t.Helper()
	for _, ev := range events {
		if ev.TaskID == taskID && ev.EventType == typ {
			return ev
		}
	}
	t.Fatalf("no %s for task %q (types: %v)", typ, taskID, ledgerTypes(events))
	return durable.LedgerEvent{}
}

func writeWorkspaceFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestRunLedgerFailedCycleIsNotSuccess is the truthfulness boundary for the
// headless path: a cycle that errored (here: the provider produced no choices)
// must leave a task that is NOT retired as complete, carrying the real reason,
// and must remain recoverable so the next process still sees the attempt.
func TestRunLedgerFailedCycleIsNotSuccess(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "main.go", "package main\n")

	ledger, err := newRunLedger(dir, "say hello", nil)
	if err != nil {
		t.Fatalf("newRunLedger: %v", err)
	}
	ledger.Dispatch()
	cause := errors.New("app: generation: openrouter: no choices")
	ledger.Fail("pipeline_error", cause)
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := readLedgerEvents(t, dir)
	if len(events) == 0 {
		t.Fatal("a headless cycle recorded nothing in the durable ledger")
	}
	if events[0].EventType != durable.EventTaskCreated {
		t.Fatalf("first journal event = %s, want TASK_CREATED (types: %v)", events[0].EventType, ledgerTypes(events))
	}
	taskID := events[0].TaskID

	// Monotonic, gap-free sequence for this journal.
	for i, ev := range events {
		if ev.Sequence != uint64(i+1) {
			t.Fatalf("event[%d] sequence = %d, want %d — the journal is not monotonic", i, ev.Sequence, i+1)
		}
	}

	if !hasLedgerEvent(events, durable.EventCursorDispatched) {
		t.Fatalf("no CURSOR_DISPATCHED before the cycle ran (types: %v)", ledgerTypes(events))
	}
	if hasLedgerEvent(events, durable.EventExecutionCommitted) {
		t.Fatalf("a failed cycle committed an execution (types: %v)", ledgerTypes(events))
	}

	failure := ledgerEventFor(t, events, taskID, durable.EventFailureClassified)
	if got := payloadString(failure, "reason"); got != "pipeline_error" {
		t.Fatalf("FAILURE_CLASSIFIED reason = %q, want pipeline_error", got)
	}
	if got := payloadString(failure, "detail"); !strings.Contains(got, "openrouter: no choices") {
		t.Fatalf("FAILURE_CLASSIFIED detail = %q, want the real provider error", got)
	}

	paused := ledgerEventFor(t, events, taskID, durable.EventTaskPaused)
	if got := payloadString(paused, "reason"); !strings.Contains(got, "openrouter: no choices") {
		t.Fatalf("TASK_PAUSED reason = %q, want the real provider error", got)
	}
	if !hasLedgerEvent(events, durable.EventCheckpointCreated) {
		t.Fatalf("a failed cycle left no CHECKPOINT_CREATED (types: %v)", ledgerTypes(events))
	}

	// No terminal VERIFICATION_RESULT at all: the cycle was never retired.
	for _, ev := range events {
		if ev.EventType != durable.EventVerificationResult {
			continue
		}
		if terminal, _ := ev.Payload["terminal"].(bool); terminal {
			t.Fatalf("a failed cycle wrote a terminal VERIFICATION_RESULT (payload: %v)", ev.Payload)
		}
		if ok, _ := ev.Payload["ok"].(bool); ok {
			t.Fatalf("a failed cycle wrote ok=true verification (payload: %v)", ev.Payload)
		}
	}

	st, ok := readSnapshot(t, dir).Tasks[taskID]
	if !ok {
		t.Fatalf("snapshot.json has no task %q", taskID)
	}
	if st.Status != durable.TaskPaused {
		t.Fatalf("failed cycle task status = %s, want PAUSED", st.Status)
	}
	if st.Status.Terminal() {
		t.Fatalf("failed cycle task status %s is terminal — the run looks finished", st.Status)
	}

	// Reopening the journal must surface the unfinished work.
	store := durable.NewTaskStore(dir)
	if err := store.Open(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = store.Close() }()
	if _, ok := store.MostRecentRecoverable(); !ok {
		t.Fatal("a failed headless cycle is not recoverable — a restart would think nothing happened")
	}
	if faults := store.SequenceFaults(); len(faults) != 0 {
		t.Fatalf("replay reported sequence faults: %+v", faults)
	}
}

// TestRunLedgerSecondInvocationContinuesTheJournal proves two invocations over
// the same workspace are two executions: a second TASK_CREATED, a sequence that
// continues past the first cycle's last event without reusing or skipping a
// position, and both tasks independently reconstructable from the journal.
func TestRunLedgerSecondInvocationContinuesTheJournal(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "main.go", "package main\n")

	first, err := newRunLedger(dir, "say hello", nil)
	if err != nil {
		t.Fatalf("newRunLedger (first): %v", err)
	}
	firstTask := first.taskID
	first.Dispatch()
	first.Fail("pipeline_error", errors.New("app: generation: openrouter: no choices"))
	if err := first.Close(); err != nil {
		t.Fatalf("Close (first): %v", err)
	}
	firstEvents := readLedgerEvents(t, dir)
	firstLast := firstEvents[len(firstEvents)-1].Sequence

	second, err := newRunLedger(dir, "say hello", nil)
	if err != nil {
		t.Fatalf("newRunLedger (second): %v", err)
	}
	if second.taskID == firstTask {
		t.Fatalf("second invocation reused the first task id %q — the first cycle's record would be erased", firstTask)
	}
	secondTask := second.taskID
	second.Dispatch()
	second.Complete(&app.Result{
		Mode:        app.ModeGreenfield,
		Artifacts:   []ir.Artifact{{Path: "out.txt", Content: []byte("hi")}},
		Validations: []app.ArtifactValidation{{Passed: true}},
	})
	if err := second.Close(); err != nil {
		t.Fatalf("Close (second): %v", err)
	}

	events := readLedgerEvents(t, dir)
	if len(events) <= len(firstEvents) {
		t.Fatalf("second invocation appended nothing (types: %v)", ledgerTypes(events))
	}
	for i, ev := range events {
		if ev.Sequence != uint64(i+1) {
			t.Fatalf("event[%d] sequence = %d, want %d — the journal is not monotonic across invocations", i, ev.Sequence, i+1)
		}
	}
	if events[len(firstEvents)].Sequence != firstLast+1 {
		t.Fatalf("second invocation resumed at sequence %d, want %d", events[len(firstEvents)].Sequence, firstLast+1)
	}

	created := 0
	for _, ev := range events {
		if ev.EventType == durable.EventTaskCreated {
			created++
		}
	}
	if created != 2 {
		t.Fatalf("TASK_CREATED count = %d, want 2 (types: %v)", created, ledgerTypes(events))
	}

	// The failed cycle is still a failed, recoverable task; the completed one
	// is retired — neither overwrote the other.
	snap := readSnapshot(t, dir)
	failedState, ok := snap.Tasks[firstTask]
	if !ok {
		t.Fatalf("snapshot.json lost the first task %q", firstTask)
	}
	if failedState.Status != durable.TaskPaused {
		t.Fatalf("first task status = %s, want PAUSED (the second invocation must not resurrect it)", failedState.Status)
	}
	doneState, ok := snap.Tasks[secondTask]
	if !ok {
		t.Fatalf("snapshot.json has no second task %q", secondTask)
	}
	if doneState.Status != durable.TaskCompleted {
		t.Fatalf("second task status = %s, want COMPLETED", doneState.Status)
	}

	// Deleting the derived view loses nothing: it is a pure fold of the journal.
	store := durable.NewTaskStore(dir)
	if err := store.Open(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := store.RebuildSnapshot(); err != nil {
		t.Fatalf("RebuildSnapshot: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close (rebuild): %v", err)
	}
	rebuilt := readSnapshot(t, dir)
	if rebuilt.Tasks[firstTask].Status != durable.TaskPaused || rebuilt.Tasks[secondTask].Status != durable.TaskCompleted {
		t.Fatalf("rebuilt snapshot = %+v, want the same statuses as before", rebuilt.Tasks)
	}
}

// TestRunLedgerProvenanceInheritsTheObjectiveDirective proves the durable task
// carries the authorization the objective actually declared: a bare prompt
// inherits the read-only zero value, and $prompt authorizes a dynamic scope.
// Nothing here is invented — it is the SAME parser the TUI path uses.
func TestRunLedgerProvenanceInheritsTheObjectiveDirective(t *testing.T) {
	cases := []struct {
		objective string
		want      int
	}{
		{"say hello", 0},                    // no directive → read-only zero value
		{"$prompt add a greeting", 1},       // $prompt → dynamic
		{"$hot fix the parser @auth.go", 2}, // $hot → declared
	}
	for _, tc := range cases {
		dir := t.TempDir()
		ledger, err := newRunLedger(dir, tc.objective, []string{"auth.go"})
		if err != nil {
			t.Fatalf("newRunLedger(%q): %v", tc.objective, err)
		}
		st, ok := ledger.store.State(ledger.taskID)
		if !ok {
			t.Fatalf("objective %q: no materialized task", tc.objective)
		}
		if int(st.ScopeProvenance) != tc.want {
			t.Fatalf("objective %q: provenance = %d, want %d", tc.objective, st.ScopeProvenance, tc.want)
		}
		if len(st.ActiveTargetScope) != 1 || st.ActiveTargetScope[0] != "auth.go" {
			t.Fatalf("objective %q: scope = %v, want [auth.go]", tc.objective, st.ActiveTargetScope)
		}
		if err := ledger.Close(); err != nil {
			t.Fatalf("objective %q: Close: %v", tc.objective, err)
		}
	}
}

// TestRunLedgerCloseIsIdempotentAndSurfacesItsError proves teardown is safe to
// defer: Close never fails on a live store, and a deferred close error is
// joined onto the run's outcome rather than dropped.
func TestRunLedgerCloseIsIdempotentAndSurfacesItsError(t *testing.T) {
	dir := t.TempDir()
	ledger, err := newRunLedger(dir, "say hello", nil)
	if err != nil {
		t.Fatalf("newRunLedger: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("second Close must be idempotent: %v", err)
	}

	// A nil ledger is a no-op teardown, not a panic.
	var absent *runLedger
	if err := absent.Close(); err != nil {
		t.Fatalf("nil ledger Close: %v", err)
	}

	// A refused close error is surfaced: the run's own error survives and the
	// teardown failure rides along with it.
	runErr := errors.New("pipeline_error: boom")
	joined := errRunLedgerClosed(runErr, errors.New("store closed"))
	if !errors.Is(joined, runErr) {
		t.Fatalf("joined error lost the run failure: %v", joined)
	}
	if !strings.Contains(joined.Error(), "close durable execution ledger") {
		t.Fatalf("joined error dropped the close failure: %v", joined)
	}
	if got := errRunLedgerClosed(nil, errors.New("store closed")); got == nil {
		t.Fatal("a close failure on an otherwise successful run was dropped")
	}
	if got := errRunLedgerClosed(runErr, nil); !errors.Is(got, runErr) {
		t.Fatalf("a clean close changed the run outcome: %v", got)
	}

	// A closed store refuses further appends: the journal's last writer is
	// knowable.
	if err := ledger.store.PauseTask(ledger.taskID, "after close"); err == nil {
		t.Fatal("a closed ledger accepted an append — the last writer is unknowable")
	}
}

// TestRunLedgerCompletedCycleRecordsWhatWasVerified pins the success semantics:
// the record establishes a committed EXECUTION and the validation counts that
// produced the verdict. There is no "objective proven" claim anywhere in it —
// the headless path holds no objective-proof authority.
func TestRunLedgerCompletedCycleRecordsWhatWasVerified(t *testing.T) {
	dir := t.TempDir()
	ledger, err := newRunLedger(dir, "write a greeting", nil)
	if err != nil {
		t.Fatalf("newRunLedger: %v", err)
	}
	ledger.Dispatch()
	ledger.Complete(&app.Result{
		Mode:      app.ModeGreenfield,
		Artifacts: []ir.Artifact{{Path: "out.txt", Content: []byte("hi")}},
		Validations: []app.ArtifactValidation{
			{Passed: true},
			{Passed: true},
		},
	})
	taskID := ledger.taskID
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := readLedgerEvents(t, dir)
	if !hasLedgerEvent(events, durable.EventExecutionCommitted) {
		t.Fatalf("a completed cycle has no EXECUTION_COMMITTED (types: %v)", ledgerTypes(events))
	}
	commit := ledgerEventFor(t, events, taskID, durable.EventExecutionCommitted)
	if got := payloadString(commit, "operationId"); got != ledger.operationID {
		t.Fatalf("EXECUTION_COMMITTED operationId = %q, want %q", got, ledger.operationID)
	}

	var terminal *durable.LedgerEvent
	for i := range events {
		if events[i].TaskID != taskID || events[i].EventType != durable.EventVerificationResult {
			continue
		}
		if ok, _ := events[i].Payload["terminal"].(bool); ok {
			terminal = &events[i]
		}
	}
	if terminal == nil {
		t.Fatalf("a completed cycle wrote no terminal VERIFICATION_RESULT (types: %v)", ledgerTypes(events))
	}
	if ok, _ := terminal.Payload["ok"].(bool); !ok {
		t.Fatalf("terminal verification ok = false (payload: %v)", terminal.Payload)
	}
	detail := payloadString(*terminal, "detail")
	for _, want := range []string{"mode=greenfield", "artifacts=1", "validations=2/2 passed", "no objective-proof authority"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("terminal detail = %q, want it to contain %q", detail, want)
		}
	}

	if !hasLedgerEvent(events, durable.EventCheckpointCreated) {
		t.Fatalf("a completed cycle left no CHECKPOINT_CREATED (types: %v)", ledgerTypes(events))
	}
	if st, ok := readSnapshot(t, dir).Tasks[taskID]; !ok || st.Status != durable.TaskCompleted {
		t.Fatalf("completed cycle snapshot = %+v (ok=%v), want COMPLETED", st, ok)
	}

	// A retired execution is not offered back to the next process.
	store := durable.NewTaskStore(dir)
	if err := store.Open(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = store.Close() }()
	if rec := store.RecoverableTasks(); len(rec) != 0 {
		t.Fatalf("RecoverableTasks = %+v, want none after a completed cycle", rec)
	}
}

// TestBoundedRunDetailCapsProviderDerivedText proves a model answer or a long
// provider error can never dominate the journal.
func TestBoundedRunDetailCapsProviderDerivedText(t *testing.T) {
	long := strings.Repeat("x", runLedgerDetailLimit*3)
	got := boundedRunDetail(long)
	// The ellipsis is a multi-byte rune, so assert the truncated prefix rather
	// than an exact byte length.
	if !strings.HasPrefix(long, got[:runLedgerDetailLimit]) {
		t.Fatalf("bounded detail is not a truncated prefix of the source: %q", got)
	}
	if got := boundedRunDetail("  trimmed  "); got != "trimmed" {
		t.Fatalf("bounded detail = %q, want trimmed", got)
	}
}

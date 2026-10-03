package durable

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain"
)

// readSequences decodes the sequence column of every line in the journal, in
// file order. It is the on-disk truth the tests assert against — not an
// in-memory accessor — so a sequence that was never persisted fails here even
// if the store believed it had written one.
func readSequences(t *testing.T, path string) []uint64 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var out []uint64
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev LedgerEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode ledger line %q: %v", line, err)
		}
		out = append(out, ev.Sequence)
	}
	return out
}

func readEvents(t *testing.T, path string) []LedgerEvent {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var out []LedgerEvent
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev LedgerEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode ledger line %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

// rewriteSequence rewrites the journal with a caller-chosen sequence for the
// event at index i. It is how a test produces the corruption a real journal
// can suffer (a truncated, reordered or hand-edited file) without inventing a
// private store API.
func rewriteSequence(t *testing.T, path string, i int, seq uint64) {
	t.Helper()
	events := readEvents(t, path)
	if i < 0 || i >= len(events) {
		t.Fatalf("rewriteSequence: index %d out of range (%d events)", i, len(events))
	}
	events[i].Sequence = seq
	var b strings.Builder
	for _, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("rewrite ledger: %v", err)
	}
}

// The store assigns the position, so every append — across every method, not
// just the truth boundaries — lands on the next strictly increasing value and
// the value on disk is the value in memory.
func TestSequenceIsMonotonicAcrossEveryAppend(t *testing.T) {
	dir := testWorkDir(t)
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := s.NextSequence(); got != 1 {
		t.Fatalf("fresh store NextSequence = %d, want 1", got)
	}
	if _, err := s.CreateTaskWithProvenance("t1", "patch", []string{"a.txt"}, domain.ScopeDynamic); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.DispatchCursor(ExecutionCursor{TaskID: "t1", StepID: "s1", OperationID: "op1"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := s.CommitExecution("t1", "op1"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := s.RecordVerification("t1", "op1", true, "build green"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := s.Checkpoint("t1", "cp1"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := s.PauseTask("t1", "awaiting human"); err != nil {
		t.Fatalf("pause: %v", err)
	}

	got := readSequences(t, s.LedgerPath())
	want := []uint64{1, 2, 3, 4, 5, 6}
	if len(got) != len(want) {
		t.Fatalf("journal has %d events, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequence[%d] = %d, want %d (full: %v)", i, got[i], want[i], got)
		}
	}
	if next := s.NextSequence(); next != 7 {
		t.Fatalf("NextSequence after 6 appends = %d, want 7", next)
	}
	if gaps := s.SequenceGaps(); len(gaps) != 0 {
		t.Fatalf("a freshly written journal must report no gaps, got %v", gaps)
	}
}

// Concurrent appends are serialized by the store mutex, so the journal can
// never mint the same position twice — the property a caller-supplied
// counter would lose the moment two goroutines raced.
func TestSequenceAssignedUnderLockIsUniqueUnderConcurrency(t *testing.T) {
	dir := testWorkDir(t)
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.CreateTask("t1", "patch", []string{"a.txt"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	const writers = 16
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := range writers {
		go func(i int) {
			defer wg.Done()
			if err := s.RecordFailure("t1", "transient", "", "writer"); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got := readSequences(t, s.LedgerPath())
	if len(got) != writers+1 {
		t.Fatalf("journal has %d events, want %d", len(got), writers+1)
	}
	seen := make(map[uint64]bool, len(got))
	for i, seq := range got {
		if seq != uint64(i+1) {
			t.Fatalf("sequence[%d] = %d, want %d — positions are not monotonic under concurrency: %v", i, seq, i+1, got)
		}
		if seen[seq] {
			t.Fatalf("sequence %d was minted twice: %v", seq, got)
		}
		seen[seq] = true
	}
}

// The counter is reconstructed from the journal, never carried across the
// restart in a side file, so a reopened store continues the same series
// instead of restarting at 1 and silently overwriting history.
func TestSequenceReconstructedAfterReopen(t *testing.T) {
	dir := testWorkDir(t)
	first := NewTaskStore(dir)
	if err := first.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := first.CreateTask("t1", "patch", []string{"a.txt"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := first.CommitExecution("t1", "op1"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := NewTaskStore(dir)
	if err := second.Open(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := second.NextSequence(); got != 3 {
		t.Fatalf("reopened NextSequence = %d, want 3", got)
	}
	if _, err := second.CreateTask("t2", "second", []string{"b.txt"}); err != nil {
		t.Fatalf("create after reopen: %v", err)
	}
	got := readSequences(t, second.LedgerPath())
	want := []uint64{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("journal has %d events, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequence[%d] = %d, want %d (full: %v)", i, got[i], want[i], got)
		}
	}
}

// A journal written before the sequence existed carries zeros. Those events
// must replay exactly like any other and must not drag the counter backwards:
// the counter is the max of the non-zero values observed.
func TestLegacySequenceZeroReplaysClean(t *testing.T) {
	dir := testWorkDir(t)
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.CreateTask("legacy", "patch", []string{"a.txt"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.DispatchCursor(ExecutionCursor{TaskID: "legacy", OperationID: "op1"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := s.CommitExecution("legacy", "op1"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Strip the sequence from every line: exactly what an older build wrote.
	events := readEvents(t, s.LedgerPath())
	var b strings.Builder
	for _, ev := range events {
		ev.Sequence = 0
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(s.LedgerPath(), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("rewrite ledger: %v", err)
	}

	replay := NewTaskStore(dir)
	if err := replay.Open(); err != nil {
		t.Fatalf("reopen legacy ledger: %v", err)
	}
	if gaps := replay.SequenceGaps(); len(gaps) != 0 {
		t.Fatalf("a legacy journal is not a corrupt one, got gaps %v", gaps)
	}
	if faults := replay.SequenceFaults(); len(faults) != 0 {
		t.Fatalf("a legacy journal is not a corrupt one, got faults %v", faults)
	}
	if got := replay.NextSequence(); got != 1 {
		t.Fatalf("legacy-only journal NextSequence = %d, want 1 — a zero must not move the counter", got)
	}
	st, ok := replay.State("legacy")
	if !ok {
		t.Fatal("legacy journal must still reconstruct its task")
	}
	if st.Status != TaskRunning || st.Cursor == nil || st.Cursor.OperationID != "op1" {
		t.Fatalf("legacy replay did not reconstruct task state: %+v", st)
	}

	// A mixed journal (legacy lines followed by a new sequenced event) keeps
	// the counter at the highest non-zero value seen, not at the line count.
	if _, err := replay.CreateTask("fresh", "newer", []string{"c.txt"}); err != nil {
		t.Fatalf("create after legacy replay: %v", err)
	}
	if got := readSequences(t, replay.LedgerPath()); got[len(got)-1] != 1 {
		t.Fatalf("post-legacy append sequence = %d, want 1 (full: %v)", got[len(got)-1], got)
	}
}

// A journal that lost an event in the middle must still reconstruct, and must
// say which positions it could not account for instead of quietly folding a
// hole into the state.
func TestGapInCorruptedJournalIsReportedAndReplaySurvives(t *testing.T) {
	dir := testWorkDir(t)
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.CreateTask("t1", "patch", []string{"a.txt"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.DispatchCursor(ExecutionCursor{TaskID: "t1", StepID: "s1", OperationID: "op1"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := s.CommitExecution("t1", "op1"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := s.Checkpoint("t1", "cp1"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// Lose the middle event: the file now claims position 3 follows 1.
	events := readEvents(t, s.LedgerPath())
	var b strings.Builder
	for i, ev := range events {
		if i == 1 {
			continue
		}
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(s.LedgerPath(), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("rewrite ledger: %v", err)
	}

	replay := NewTaskStore(dir)
	if err := replay.Open(); err != nil {
		t.Fatalf("reopen gapped ledger: %v", err)
	}
	gaps := replay.SequenceGaps()
	if len(gaps) != 1 || gaps[0] != 2 {
		t.Fatalf("SequenceGaps = %v, want [2]", gaps)
	}
	faults := replay.SequenceFaults()
	if len(faults) != 1 || faults[0].Kind != SequenceFaultGap || faults[0].Observed != 3 || faults[0].Expected != 2 {
		t.Fatalf("SequenceFaults = %+v, want one GAP(observed=3, expected=2)", faults)
	}
	// The counter is still the max observed, so the next append continues the
	// series rather than reusing a position the journal already claims.
	if got := replay.NextSequence(); got != 5 {
		t.Fatalf("NextSequence after a gap = %d, want 5", got)
	}
	// And the surviving events still reconstruct real state.
	st, ok := replay.State("t1")
	if !ok || st.Intent != "patch" || st.LastCheckpointID != "cp1" {
		t.Fatalf("a gapped journal must still reconstruct the state it does contain: %+v (ok=%v)", st, ok)
	}
}

// A journal whose positions go backwards is not trustworthy as ordered
// evidence. Replay keeps the events it can and reports the defect.
func TestNonMonotonicSequenceIsReportedAndReplaySurvives(t *testing.T) {
	dir := testWorkDir(t)
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.CreateTask("t1", "patch", []string{"a.txt"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.DispatchCursor(ExecutionCursor{TaskID: "t1", StepID: "s1", OperationID: "op1"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Rewind the second event to the first event's position: the file now
	// claims two events occupy position 1.
	rewriteSequence(t, s.LedgerPath(), 1, 1)

	replay := NewTaskStore(dir)
	if err := replay.Open(); err != nil {
		t.Fatalf("reopen non-monotonic ledger: %v", err)
	}
	faults := replay.SequenceFaults()
	if len(faults) != 1 || faults[0].Kind != SequenceFaultNonMonotonic || faults[0].Observed != 1 || faults[0].Expected != 2 {
		t.Fatalf("SequenceFaults = %+v, want one NON_MONOTONIC(observed=1, expected=2)", faults)
	}
	if gaps := replay.SequenceGaps(); len(gaps) != 0 {
		t.Fatalf("a repeat is not a missing position, got gaps %v", gaps)
	}
	if got := replay.NextSequence(); got != 2 {
		t.Fatalf("NextSequence after a repeat = %d, want 2 — the max is never lowered", got)
	}
	if _, ok := replay.State("t1"); !ok {
		t.Fatal("a non-monotonic journal must still reconstruct the tasks it contains")
	}
}

// A gap in the middle of a journal must not cost the reconstruction that
// §15 depends on: the unfinished task and its last known state are still
// recoverable after the defect is reported.
func TestReplayReconstructsTaskStateAcrossSequenceDefects(t *testing.T) {
	dir := testWorkDir(t)
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.CreateTaskWithProvenance("done", "finished", []string{"a.txt"}, domain.ScopeDynamic); err != nil {
		t.Fatalf("create done: %v", err)
	}
	if err := s.Checkpoint("done", "cp-done"); err != nil {
		t.Fatalf("checkpoint done: %v", err)
	}
	if _, err := s.CreateTaskWithProvenance("open", "in flight", []string{"b.txt"}, domain.ScopeDynamic); err != nil {
		t.Fatalf("create open: %v", err)
	}
	if err := s.DispatchCursor(ExecutionCursor{TaskID: "open", StepID: "s2", OperationID: "op2"}); err != nil {
		t.Fatalf("dispatch open: %v", err)
	}
	rewriteSequence(t, s.LedgerPath(), 2, 9)

	replay := NewTaskStore(dir)
	if err := replay.Open(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if gaps := replay.SequenceGaps(); len(gaps) != 6 {
		t.Fatalf("SequenceGaps = %v, want 6 missing positions (3..8)", gaps)
	}
	most, ok := replay.MostRecentRecoverable()
	if !ok {
		t.Fatal("the interrupted task must be recoverable after the defect is reported")
	}
	if most.ID != "open" || most.Intent != "in flight" || most.ScopeProvenance != domain.ScopeDynamic {
		t.Fatalf("most recent recoverable = %+v, want the interrupted 'open' task", most)
	}
	if most.Cursor == nil || most.Cursor.OperationID != "op2" {
		t.Fatalf("interrupted task lost its cursor: %+v", most.Cursor)
	}
	if rec := replay.RecoverableTasks(); len(rec) != 2 || rec[0].ID != "open" {
		t.Fatalf("RecoverableTasks = %+v, want 'open' most recent then 'done'", rec)
	}
}

// MostRecentRecoverable answers the §9 question — "what was in flight when the
// process died?" — using the fold's own order, so it is correct for a legacy
// journal that carries no sequence at all.
func TestMostRecentRecoverableUsesReplayOrderNotSequence(t *testing.T) {
	dir := testWorkDir(t)
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.CreateTask("first", "older", []string{"a.txt"}); err != nil {
		t.Fatalf("create first: %v", err)
	}
	if err := s.DispatchCursor(ExecutionCursor{TaskID: "first", OperationID: "op1"}); err != nil {
		t.Fatalf("dispatch first: %v", err)
	}
	if _, err := s.CreateTask("second", "newer", []string{"b.txt"}); err != nil {
		t.Fatalf("create second: %v", err)
	}
	if err := s.DispatchCursor(ExecutionCursor{TaskID: "second", OperationID: "op2"}); err != nil {
		t.Fatalf("dispatch second: %v", err)
	}
	// Strip every sequence: only file order remains as evidence.
	events := readEvents(t, s.LedgerPath())
	var b strings.Builder
	for _, ev := range events {
		ev.Sequence = 0
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(s.LedgerPath(), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("rewrite ledger: %v", err)
	}

	replay := NewTaskStore(dir)
	if err := replay.Open(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	most, ok := replay.MostRecentRecoverable()
	if !ok || most.ID != "second" {
		t.Fatalf("MostRecentRecoverable = %+v (ok=%v), want 'second'", most, ok)
	}
}

// A store that has been torn down must stop writing. Otherwise a shut-down
// runtime keeps appending execution truth whose last writer nobody can name.
func TestCloseRefusesFurtherAppends(t *testing.T) {
	dir := testWorkDir(t)
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.CreateTask("t1", "patch", []string{"a.txt"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close must be idempotent: %v", err)
	}
	if err := s.CommitExecution("t1", "op1"); err == nil {
		t.Fatal("a closed store must refuse further appends")
	}
	if got := readSequences(t, s.LedgerPath()); len(got) != 1 {
		t.Fatalf("closed store appended %d events, want the original 1", len(got))
	}
}

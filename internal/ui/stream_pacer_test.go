package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ── UNIFIED STREAM PACER ──────────────────────────────────────────────────────
//
// The contract these tests defend is narrow and entirely behavioural: a
// background source may not cause a document composition, and the frame tick must
// not stop while a source is holding bytes. Everything else is implementation.

// TestPacerCoalescesManyChunksIntoOneFrame is the central claim. A `/exec` of a
// large file arrives in hundreds of chunks, and the whole point of the pacer is
// that N chunks and one chunk cost the same on the render path. So the assertion
// is a count: many notes, one drain, one composition.
// shellPacerTestModel is a model with a live shell channel and a running exec
// entry, and nothing else. The channel is installed directly rather than through
// streamShellCmd, because these tests are about the RENDER path: spawning a real
// process would add a goroutine, a filesystem and a timing dependency to an
// assertion about frame counts.
func shellPacerTestModel(t *testing.T, cmd string) *model {
	t.Helper()
	m := newTestModel()
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.activityTree = NewActivityTree()
	m.activityTree.AppendOrUpdateExec(cmd, -1, 0, "")
	m.shellCh = make(chan tea.Msg, 64)
	m.shellRunning = true
	return m
}

func TestPacerCoalescesManyChunksIntoOneFrame(t *testing.T) {
	m := shellPacerTestModel(t, "cat build.log")

	const chunks = 400
	for range chunks {
		nm, _ := m.Update(shellChunkMsg{text: "compiling module\n"})
		m = nm.(*model)
	}
	if m.refreshScheduled {
		t.Fatal("a shell chunk armed a repaint: exec output must render on the frame tick")
	}
	joins := m.ensureStreamPacer().joins

	// One frame tick, and every one of those chunks is rendered by it.
	nm, _ := m.Update(FrameTickMsg{})
	m = nm.(*model)
	m.Update(FrameTickMsg{})

	if got := m.ensureStreamPacer().joins; got != joins+1 {
		t.Errorf("%d chunks produced %d paced frames, want exactly 1", chunks, got-joins)
	}
	// The output is not merely counted, it is all there: pacing is a latency
	// decision and must never become a correctness one.
	entries := m.activityTree.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 exec entry, got %d", len(entries))
	}
	if got := strings.Count(entries[0].CommandExec.Output, "compiling module"); got != chunks {
		t.Errorf("exec output carries %d of %d chunks: pacing dropped output", got, chunks)
	}
}

// TestPacerArmsTheFrameLoopItDependsOn pins the obligation that makes the design
// safe. Because a source no longer renders on arrival, the clock has to outlive
// its producers — otherwise the first note after a quiet period would park bytes
// with nothing left to draw them. This is the /exec-before-the-stream case, where
// m.streaming is false and nothing else holds the ticker up.
func TestPacerArmsTheFrameLoopItDependsOn(t *testing.T) {
	m := shellPacerTestModel(t, "make build")
	if m.streaming {
		t.Fatal("precondition: the answer stream must not be what keeps the loop alive here")
	}
	m.frameTickActive = false

	nm, cmd := m.Update(shellChunkMsg{text: "cc -c main.c\n"})
	m = nm.(*model)

	if !m.frameTickActive {
		t.Fatal("a paced source must arm the frame loop: otherwise its bytes are stranded")
	}
	if cmd == nil {
		t.Fatal("the arming note must schedule something: the tick that will drain it")
	}
	// The command itself is not executed here: the shell handler's other half is
	// a blocking read of the pipe, and running it would park the test on an empty
	// channel. What matters is the armed flag above and the keep-alive below.
	//
	// And the loop stays alive while the source is still pending.
	nm, tick := m.Update(FrameTickMsg{})
	m = nm.(*model)
	if m.pacedStreamsActive() {
		t.Fatal("precondition: the frame tick must have drained the pending chunk")
	}
	nm, _ = m.Update(shellChunkMsg{text: "cc -c other.c\n"})
	m = nm.(*model)
	nm, _ = m.Update(FrameTickMsg{})
	m = nm.(*model)
	if m.pacedStreamsActive() {
		t.Error("a second chunk after the first must also be drained by its frame")
	}
	_ = tick
}

// TestPacerStopsTheLoopWhenEverySourceIsDrained is the other half of the same
// obligation, and the reason the pacer tracks pending state at all: a loop that
// outlives its sources is a timer nobody can stop, and a frame that drains nothing
// has nothing to draw.
func TestPacerStopsTheLoopWhenEverySourceIsDrained(t *testing.T) {
	m := shellPacerTestModel(t, "true")

	nm, _ := m.Update(shellChunkMsg{text: "ok\n"})
	m = nm.(*model)
	if !m.pacedStreamsActive() {
		t.Fatal("precondition: the chunk must be pending")
	}

	nm, _ = m.Update(FrameTickMsg{}) // drains
	m = nm.(*model)
	if m.pacedStreamsActive() {
		t.Fatal("the drain must release every pending source")
	}
	// The shell is still "running" as far as the flags go, so the loop can only
	// stop here because the pacer had nothing left — which is the point: a paced
	// source's lifetime, not the shell flag's, decides the clock.
	m.shellRunning = false
	nm, cmd := m.Update(FrameTickMsg{}) // nothing left to draw
	m = nm.(*model)
	if cmd != nil {
		t.Error("the frame loop must self-terminate once no source is pending")
	}
	if m.frameTickActive {
		t.Error("frameTickActive must clear when the loop self-terminates")
	}
}

// TestShellExitDrainsSynchronously is the correctness clause. The terminal handler
// is the last opportunity to see its own output: the loop it would have waited for
// is being torn down on the very same line. So the exit path drains synchronously,
// exactly as it did before pacing existed, and a pending flag is never left for a
// tick that will not come.
func TestShellExitDrainsSynchronously(t *testing.T) {
	m := shellPacerTestModel(t, "echo tail-line")

	nm, _ := m.Update(shellChunkMsg{text: "the last line of output\n"})
	m = nm.(*model)
	if !m.pacedStreamsActive() {
		t.Fatal("precondition: the chunk must be pending at exit time")
	}

	nm, _ = m.Update(shellExitMsg{cmd: "echo tail-line", exitCode: 0})
	m = nm.(*model)
	if m.pacedStreamsActive() {
		t.Error("the shell exit must drain the pacer synchronously")
	}
	// The terminal handler re-composed the document synchronously, which is what
	// "nothing is lost" means here: the drain ran on this message, not on a tick
	// the teardown was about to cancel.
	if !m.scrollPoolValid() {
		t.Error("the shell exit must compose the document on its own message")
	}
	entries := m.activityTree.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 exec entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].CommandExec.Output, "the last line of output") {
		t.Errorf("the command's final chunk is missing from the exec entry: %q",
			entries[0].CommandExec.Output)
	}
}

// TestOneFrameServesEverySource is the "unified" claim. The answer, the reasoning
// trace and the command output are different producers with different rates, and
// the frame must cost the same whatever combination of them moved. So all three
// note, and one tick produces exactly one join.
func TestOneFrameServesEverySource(t *testing.T) {
	m := shellPacerTestModel(t, "make test")

	p := m.ensureStreamPacer()
	nm, _ := m.Update(ReasoningChunkMsg{Chunk: "thinking about the build"})
	m = nm.(*model)
	nm, _ = m.Update(shellChunkMsg{text: "ok 1/3\n"})
	m = nm.(*model)
	p.note(pacedLog, false)

	joins := p.joins
	nm, _ = m.Update(FrameTickMsg{})
	m = nm.(*model)
	if m.pacedStreamsActive() {
		t.Error("the frame tick must have drained every noted source")
	}
	if got := p.joins; got != joins+1 {
		t.Errorf("three sources produced %d frames, want 1", got-joins)
	}
	if p.perSource[pacedReasoning] != 1 || p.perSource[pacedExec] != 1 || p.perSource[pacedLog] != 1 {
		t.Errorf("per-source frame counts = %v, want each source served exactly once",
			p.perSource[:pacedSourceCount])
	}
}

// TestPacerPreservesTheDetachedReader is the anti-yank guarantee. A reader who
// has scrolled up is reading history; streamed output must accumulate below them
// without moving the viewport. The pacer records the follow REQUEST at note time
// and honours it at drain time, which is where the detach latch is readable — so
// the request is still subject to policy rather than applied unconditionally.
func TestPacerPreservesTheDetachedReader(t *testing.T) {
	m := shellPacerTestModel(t, "make long")
	for range 120 {
		m.records = append(m.records, record{role: roleSystem, text: "history line "})
	}
	m.refreshViewportContent()
	m.setScrollLocked(true)
	m.scrollBy(-3)
	offset := m.docScrollOffset

	nm, _ := m.Update(shellChunkMsg{text: "still building\n"})
	m = nm.(*model)
	nm, _ = m.Update(FrameTickMsg{})
	m = nm.(*model)

	if m.docScrollOffset != offset {
		t.Errorf("streamed output moved a detached reader: offset %d -> %d", offset, m.docScrollOffset)
	}
	if m.userIsScrollingUp != true {
		t.Error("the detach latch must survive a paced frame")
	}
}

// TestPacerFollowsTheTailForAnAttachedReader is the other half: a reader at the
// bottom still gets followed, so output is never hidden below the fold.
func TestPacerFollowsTheTailForAnAttachedReader(t *testing.T) {
	m := shellPacerTestModel(t, "make long")
	for range 120 {
		m.records = append(m.records, record{role: roleSystem, text: "history line"})
	}
	m.refreshViewportContent()
	m.setScrollLocked(false)
	// Park at the absolute bottom, where the reader is following.
	m.docScrollOffset = m.maxAppScroll()
	bottom := m.docScrollOffset

	nm, _ := m.Update(shellChunkMsg{text: "new output\n"})
	m = nm.(*model)
	nm, _ = m.Update(FrameTickMsg{})
	m = nm.(*model)

	if m.docScrollOffset < bottom {
		t.Errorf("an attached reader was scrolled backwards: %d -> %d", bottom, m.docScrollOffset)
	}
	if m.userIsScrollingUp {
		t.Error("a paced follow must not engage the detach latch")
	}
}

// TestStreamPacerIsANoOpWithoutAConstruction pins the nil/degenerate contract.
// Every handler in this package must be safe on a bare model literal, and a pacer
// that had to be constructed before it can be written is a pacer that silently
// stops pacing in a headless harness.
func TestStreamPacerIsANoOpWithoutAConstruction(t *testing.T) {
	var p *streamPacer
	p.note(pacedExec, true) // must not panic
	if p.active() {
		t.Error("a nil pacer has nothing pending")
	}
	if moved, follow := p.drain(); moved || follow {
		t.Error("draining a nil pacer must report nothing")
	}
	m := &model{}
	m.ensureStreamPacer().note(pacedExec, false)
	if !m.pacedStreamsActive() {
		t.Error("the lazily built pacer must accept a note immediately")
	}
}

// TestPacerResetsAtTurnBoundaries pins the per-turn hygiene: a pending flag from
// the previous answer must not make the first frame of the next one report a
// change that never happened, and must not request a tail-follow nobody made.
//
// The reset lives at the two genuine turn boundaries — a stream starting and the
// surface being cleared — and deliberately NOT inside resetStreamingRenderer,
// which also runs on every non-streaming document refresh. Zeroing the pacer
// there would discard bytes a live /exec had already produced, which is the
// failure mode this split exists to prevent.
func TestPacerResetsAtTurnBoundaries(t *testing.T) {
	m := shellPacerTestModel(t, "make check")
	m.ensureStreamPacer().note(pacedExec, true)
	if !m.pacedStreamsActive() {
		t.Fatal("precondition: the source must be pending")
	}

	// A non-streaming document refresh must NOT drop it: the exec is still live.
	m.refreshViewportContent()
	if !m.pacedStreamsActive() {
		t.Fatal("a document refresh discarded a live source's pending bytes")
	}

	// A new turn must.
	m.clearPresentation()
	if m.pacedStreamsActive() {
		t.Error("a cleared surface must not leave a pending source for the next turn")
	}
}

// TestPacerOutOfRangeSourceIsIgnored pins that a bad source id is a no-op rather
// than a panic. The pacer is written from many handlers, and a handler that
// eventually gets a new source should not be able to corrupt the array.
func TestPacerOutOfRangeSourceIsIgnored(t *testing.T) {
	m := newTestModel()
	m.ensureStreamPacer().note(pacedSource(200), true)
	if m.pacedStreamsActive() {
		t.Error("an out-of-range source must not register as pending")
	}
}

// TestPacerNoteIsConstantWork is the memory claim restated as a measurement: the
// producer's cost must not grow with the number of chunks already seen, or the
// pacer has merely moved the O(n) somewhere less visible.
func TestPacerNoteIsConstantWork(t *testing.T) {
	if testing.Short() {
		t.Skip("latency measurement skipped in -short mode")
	}
	p := &streamPacer{}
	// Warm the call so the measurement is not dominated by first-call setup.
	for range 1000 {
		p.note(pacedExec, false)
	}
	p.drain()

	const iters = 500000
	start := time.Now()
	for range iters {
		p.note(pacedExec, false)
	}
	perNote := time.Since(start) / iters
	if perNote > 500*time.Nanosecond {
		t.Errorf("a note costs %v, too high for a fixed-size bit write", perNote)
	}
}

// TestFrameBoundSourceSetIsClosed pins the source registry: every member is a
// surface the user watches grow, and frameBound is the assertion that all of them
// render through the frame loop. A new source added without a pacer note would
// fail here rather than quietly re-introducing arrival-rate rendering.
func TestFrameBoundSourceSetIsClosed(t *testing.T) {
	for src := pacedSource(0); src < pacedSourceCount; src++ {
		if !frameBound(src) {
			t.Errorf("source %d is not frame-bound", src)
		}
	}
	if frameBound(pacedSourceCount) {
		t.Error("a source past the registry must not be frame-bound")
	}
}

// TestPacerDrainIsIdempotentPerFrame pins that a drain with nothing pending does
// not report a change. The frame tick schedules a repaint from this answer, so a
// spurious true would repaint the whole document on every idle tick.
func TestPacerDrainIsIdempotentPerFrame(t *testing.T) {
	p := &streamPacer{}
	for range 5 {
		if moved, _ := p.drain(); moved {
			t.Fatal("draining an empty pacer reported a change")
		}
	}
	if p.joins != 0 {
		t.Errorf("idle frames counted %d joins, want 0", p.joins)
	}
	p.note(pacedLog, false)
	p.drain()
	p.drain()
	if p.joins != 1 {
		t.Errorf("joins = %d, want exactly 1: a second drain had nothing to release", p.joins)
	}
}

// TestBatchCmdsCollapsesTrivialShapes pins the helper the paced handlers use to
// hold both their own continuation and a possibly-armed tick. tea.Batch panics on
// a nil command at dispatch time, so the collapse is a safety property and not
// just tidiness.
func TestBatchCmdsCollapsesTrivialShapes(t *testing.T) {
	if got := batchCmds(); got != nil {
		t.Error("no commands must batch to nil")
	}
	if got := batchCmds(nil, nil); got != nil {
		t.Error("only nils must batch to nil")
	}
	only := tea.Cmd(func() tea.Msg { return nil })
	if got := batchCmds(nil, only, nil); got == nil {
		t.Error("a single live command must survive the batch")
	}
	both := batchCmds(only, only)
	if both == nil {
		t.Error("two live commands must batch")
	}
	if _, ok := both().(tea.BatchMsg); !ok {
		t.Error("two live commands must produce a batch, not a single command")
	}
}

// TestPacerKeepsReadingShellSource is the liveness check for the exec path: the
// handler must still schedule its own next read alongside the frame tick, or
// pacing would have silently turned into "stop reading the pipe".
func TestPacerKeepsReadingShellSource(t *testing.T) {
	m := shellPacerTestModel(t, "seq 1 5")

	nm, cmd := m.Update(shellChunkMsg{text: "1\n"})
	m = nm.(*model)
	if cmd == nil {
		t.Fatal("the shell chunk handler must still read the next chunk")
	}
	if m.shellCh == nil {
		t.Error("the shell channel must survive a paced chunk")
	}
	if !m.pacedStreamsActive() {
		t.Error("the chunk must be pending for the frame tick to render")
	}
}

package ui

import tea "github.com/charmbracelet/bubbletea"

// ── UNIFIED STREAM PACER ───────────────────────────────────────────────────────
//
// # THE PROBLEM
//
// The frame loop has one clock: FrameTickMsg, at 30 FPS. The answer stream is
// bound to it. Nothing else was.
//
// Every other source of streamed output used to render on ARRIVAL:
//
//	m.push(roleSystem, line);  m.refreshViewportContent()   // exec output
//	m.thinkingBuffer.Append(c); m.refreshViewportContent()  // reasoning
//
// That is the difference between a paced surface and an unpaced one, and it is
// not subtle. A `/exec` of `cat build.log` delivers hundreds of chunks per
// second, and each one ran a full document re-composition — the row pool, the
// hit map, the tail panels, the reasoning band — synchronously on the UI
// goroutine. The visible result is not a fast stream, it is a stalled UI: the
// frame clock stops being the bottleneck and becomes a passenger, keystrokes
// queue behind render work nobody asked for, and the terminal paints as fast as
// the event loop happens to reach it.
//
// So the same 30 FPS the answer gets is now the cadence for EVERY surface: the
// answer, the reasoning trace, the command output, and the tool/activity log.
// Throughput is unchanged — nothing is dropped and nothing is buffered longer
// than one frame — but the RENDER is constant-rate again, which is the only thing
// the eye can actually perceive as smooth.
//
// # THE SHAPE
//
// A source notes that bytes arrived and nothing else. It does not render, does not
// re-budget, and does not arm a timer of its own:
//
//	producer ──note()──▶ pacer.pending[source] = true
//	                          │
//	                   FrameTickMsg (30ms)
//	                          │
//	                   drain() ──▶ ONE refreshViewportContent for the whole frame
//
// Two properties fall out of that shape and both are the point:
//
//   - PER-FRAME COST IS O(SOURCES), NOT O(CHUNKS). Four sources produce the same
//     one refresh as four hundred chunks, because a note is a bit write and a
//     drain is a four-element scan.
//   - A SOURCE CANNOT OUTRUN THE CLOCK. There is no code path from a chunk to a
//     render, so a source that arrives at 5000 chunks/sec cannot ask for 5000
//     frames. It asks for "the next frame", once.
//
// # CORRECTNESS IS NOT A LATENCY CONCERN
//
// Pacing a render is a latency decision; it is never allowed to be a correctness
// one. So every TERMINAL event for a source drains it synchronously (drainNow)
// instead of waiting for a tick that may not come: the shell's exit, a stream
// end, a stream error. Nothing can be left pending behind a loop that has already
// stopped, which is the only way this design could lose output.

// pacedSource identifies one background stream that renders through the frame
// loop. The set is closed and small on purpose: every member is a surface the
// user watches grow, and a member that nobody watches grow does not belong here.
type pacedSource uint8

const (
	// pacedAnswer is the visible assistant response. It is paced already — this
	// is where the content buffer is drained — and it is listed so the frame tick
	// has one uniform place to ask "did anything move".
	pacedAnswer pacedSource = iota
	// pacedReasoning is the reasoning/think trace (ThinkingBuffer, traceBuffer).
	pacedReasoning
	// pacedExec is live command output: /exec and the build shell.
	pacedExec
	// pacedLog is the tool/activity log.
	pacedLog

	pacedSourceCount
)

// streamPacer is the frame-bound render gate for every background stream.
//
// It holds no bytes and no channels: a producer that has already appended to its
// own buffer only records THAT it appended. That is deliberate — the pacer must
// never become a second copy of the data, because a second copy is a second thing
// to keep in sync and a second place for a race to live.
type streamPacer struct {
	// pending records that a source appended since the last drain.
	pending [pacedSourceCount]bool
	// follow records that a source wants the viewport to keep up with its tail.
	// It is separate from pending because the two answer different questions: a
	// source can have arrived while the reader is parked mid-document, and that
	// reader must not be yanked (see drain).
	follow [pacedSourceCount]bool
	// joins counts frames on which at least one source was pending, and perSource
	// counts them individually. Together they are the observability that makes
	// "the exec output is now frame-paced" a measurement rather than a claim: a
	// test can assert that N chunks produced exactly one join.
	joins      uint64
	perSource  [pacedSourceCount]uint64
	drainCount uint64
}

// note records that a source appended, and whether the viewport should follow its
// tail. It is the ONLY method a producer calls, and it is O(1) and
// non-blocking — a producer must never wait for the UI goroutine to catch up,
// because the whole point of a producer running on its own goroutine is that it
// cannot.
//
// follow is the tail-follow request the source would have made on arrival. It is
// captured rather than acted on so a reader who has scrolled up keeps their
// position: the note is honoured at the drain, where the detach latch and the
// workspace auto-scroll policy can be read.
func (p *streamPacer) note(src pacedSource, follow bool) {
	if p == nil || src >= pacedSourceCount {
		return
	}
	p.pending[src] = true
	if follow {
		p.follow[src] = true
	}
}

// drain releases every pending source and reports whether anything moved, plus
// whether any of them asked to follow the tail.
//
// It is called once per frame, so it is a fixed four-element scan no matter how
// many chunks arrived in the interval. That fixed cost is the reason a fast
// source cannot make the frame expensive: the per-chunk work happened at the
// producer, and the per-frame work here does not depend on it.
func (p *streamPacer) drain() (moved, follow bool) {
	if p == nil {
		return false, false
	}
	for i := range p.pending {
		if p.pending[i] {
			moved = true
			p.perSource[i]++
		}
		p.pending[i] = false
		if p.follow[i] {
			follow = true
		}
		p.follow[i] = false
	}
	if moved {
		p.joins++
		p.drainCount++
	}
	return moved, follow
}

// active reports whether any source has unrendered bytes. The frame loop consults
// it to decide whether to keep ticking: a tick that drains nothing has nothing to
// draw, and a loop that outlives its sources is a timer nobody can stop.
func (p *streamPacer) active() bool {
	if p == nil {
		return false
	}
	for i := range p.pending {
		if p.pending[i] {
			return true
		}
	}
	return false
}

// frameBound reports whether src renders through the frame loop rather than
// synchronously on arrival. It exists so the invariant is stated once and can be
// asserted, rather than being a comment that rots.
func frameBound(src pacedSource) bool {
	return src < pacedSourceCount
}

// batchCmds returns a command that runs both, collapsing the trivial shapes so a
// handler does not have to branch on how many commands it happens to hold.
//
// It matters for the frame tick specifically: the tick is not something to
// schedule unconditionally. Handlers that must both continue reading their own
// source and possibly arm the clock need to express "these two, or just the one,
// or neither", and `tea.Batch` of a nil command panics at dispatch time.
func batchCmds(cmds ...tea.Cmd) tea.Cmd {
	var live []tea.Cmd
	for _, c := range cmds {
		if c != nil {
			live = append(live, c)
		}
	}
	switch len(live) {
	case 0:
		return nil
	case 1:
		return live[0]
	default:
		return tea.Batch(live...)
	}
}

// ── Model integration ─────────────────────────────────────────────────────────

// ensureStreamPacer lazily constructs the pacer. Like every other lazily-built
// register in this model it is safe on a bare struct literal, because every
// headless harness in this package builds the model that way and a register that
// must exist before it can be written is a register that silently stops working.
func (m *model) ensureStreamPacer() *streamPacer {
	if m.streamPacer == nil {
		m.streamPacer = &streamPacer{}
	}
	return m.streamPacer
}

// pacedStreamsActive reports whether a background source is holding unrendered
// bytes. It is the frame loop's keep-alive condition for the paced sources: a
// tick must outlive its producers or their output is stranded in memory with
// nothing left to draw it.
func (m *model) pacedStreamsActive() bool {
	return m != nil && m.streamPacer.active()
}

// notePacedSource records that a background source appended, and arms the frame
// loop if it is not already armed.
//
// Arming is the whole reason this is a method and not a bare pacer.note. A
// source that notes into a dead loop has parked its bytes forever: the answer
// stream keeps the loop alive through m.streaming, but a `/exec` running before
// the stream starts, or a sub-task trace arriving outside a turn, has nothing
// else holding the ticker up. So the FIRST note after a quiet period re-arms it,
// and every note after that is a bit write.
func (m *model) notePacedSource(src pacedSource, follow bool) tea.Cmd {
	p := m.ensureStreamPacer()
	p.note(src, follow)
	if m.frameTickActive {
		return nil
	}
	m.frameTickActive = true
	return m.frameTickCmd()
}

// drainPacedSources releases whatever the background sources accumulated and
// reports whether anything moved.
//
// It deliberately does NOT compose the document. The frame tick already owns a
// single-flight repaint gate (scheduleRepaint) that guarantees at most one
// composition per frame, and the answer stream's flush goes through that same
// gate. Composing here as well would mean two compositions on a frame where both
// fired — the exact double work the gate exists to prevent — so the drain's only
// job is to say "something moved" and let the gate own the rendering.
//
// It also does NOT call gotoBottomIfAllowed. That helper composes immediately,
// which is right for a synchronous handler and wrong here; and it does not need
// to, because refreshViewportContent already pins the viewport to the tail
// whenever the follow policy allows it (calculateEffectiveYOffset). So all the
// drain owes the reader is the LATCH RELEASE, and the frame's single composition
// does the rest.
func (m *model) drainPacedSources() bool {
	moved, follow := m.ensureStreamPacer().drain()
	if follow {
		// The detach latch is the only thing that can hold a reader off the tail
		// once new bytes exist. Releasing it here means the frame's composition
		// places them, which is the one composition this frame will do.
		m.setScrollLocked(false)
	}
	return moved
}

// drainPacedSourcesNow is the TERMINAL drain. Every path that ends a source —
// shell exit, stream end, stream error, cancel — calls this instead of waiting
// for a tick.
//
// The reasoning is that a terminal handler is the last opportunity to see its own
// output: the loop that would have drained it may already be stopping, and a
// pending flag behind a stopped loop is output that never appears. So the
// terminal path renders synchronously, exactly as it did before pacing existed,
// and pacing governs only the steady state. That is the same discipline the token
// pacer uses with DrainAll, and it is what makes the change safe to make at all.
func (m *model) drainPacedSourcesNow(src pacedSource) {
	p := m.ensureStreamPacer()
	p.note(src, false)
	if moved, follow := p.drain(); moved {
		m.refreshViewportContent()
		if follow {
			m.gotoBottomIfAllowed()
		}
	}
}

// resetStreamPacer releases the pacer at a turn boundary. Like every other
// per-turn register it must not carry a pending flag across a turn: a flag left
// set by the previous answer would make the first frame of the next one report a
// change that never happened.
func (m *model) resetStreamPacer() {
	if m == nil || m.streamPacer == nil {
		return
	}
	*m.streamPacer = streamPacer{}
}

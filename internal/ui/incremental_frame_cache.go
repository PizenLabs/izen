package ui

import "github.com/PizenLabs/izen/internal/ui/markdown"

// ── O(1) PER-FRAME COST: THE DOCUMENT IS APPEND-ONLY DURING A STREAM ────────
//
// # THE SHAPE OF THE PROBLEM
//
// A 30FPS frame tick has a budget of about 2ms of compute before it starts
// eating the wheel's latency. The streaming tail renderer is already O(new
// bytes): complete lines are committed to the persistent block renderer once
// and the still-growing trailing line is re-rendered alone. That part of the
// frame is bounded by the DELTA.
//
// Three other things a frame does are not. Each walks the WHOLE document:
//
//   - the scroll line pool, which re-reads every docLayout row's RenderedStr
//     out of an 88-byte-strided struct array;
//   - the hit map, which re-derives every record's wrap geometry — a Split, a
//     Wordwrap, a lipgloss.Width and a rune conversion per logical line, with
//     allocations for each;
//   - the selection framebuffer, which copies every row and then allocates a
//     Cell per column.
//
// On a long conversation those three are the frame. They are also all PURE
// FUNCTIONS of state that does not change while a stream is running, which is
// what makes them cacheable rather than merely deferrable.
//
// # WHY THE DOCUMENT IS APPEND-ONLY HERE
//
// The streaming tail is spliced in as a trailing SEGMENT of docLayout, starting
// at streamingDocStart. syncStreamingSegment trims the document back to that
// boundary and re-appends the tail, so:
//
//   - rows [0, streamingDocStart) come from the record layout and cannot move;
//   - rows above the tail boundary grow monotonically within a turn;
//   - the records slice itself is frozen while m.streaming is true — the answer
//     is committed as a record only at completion, and completion is not a
//     streaming frame.
//
// # THE ONE ROW THAT IS NOT STABLE
//
// The still-growing partial line is re-rendered from scratch on every tick and
// can legitimately produce a DIFFERENT row than it did last tick: a leading pipe
// latches the table holdback and the row disappears entirely, a balanced marker
// promotes from plain text to styled. So the last row of the document is
// volatile by construction, and every cache here treats docLen-1 as volatile
// rather than trusting a growth-only argument about it. A cache that could hold
// a row that changes is a cache that flickers, and a flicker is a worse defect
// than the millisecond it was saving.

// rowCacheHeadroom is the spare capacity a freshly built row cache is given, so
// the frames right after a rebuild can extend it in place. It is sized for a
// fast stream at 30FPS: 64 rows is a little over two seconds of a 30-row-per-
// second answer, which is far longer than a rebuild should ever be followed by
// a full-frame rebuild.
const rowCacheHeadroom = 64

// ── TRANSIENT AST BALANCE: THE STREAMING TAIL'S PARSE COPY ──────────────────
//
// The one row a frame cannot cache is the still-growing trailing line, and it is
// also the one row that flickers. It is re-parsed from scratch on every tick, and
// while it grows it is a PREFIX of the line the model is writing — so the frame
// sees `**The migration is` and has to decide what that means. Interpreting it
// literally paints two raw asterisks; holding it back paints no style at all and
// then restyles the whole phrase on the next frame. Both are the "raw markup
// flashes in while the answer streams" report.
//
// frameTailLine resolves it by handing the AST pass a TRANSIENT PARSE COPY: the
// trailing line with a matching closer appended for every delimiter still open.
// The style appears on the first tick, the raw marker never reaches the screen,
// and the copy is thrown away with the frame.
//
// Three things make this a frame-level concern rather than a renderer one:
//
//   - it is per-FRAME derived. It is recomputed from the raw bytes every tick and
//     never stored, because the next tick's prefix is a different string and a
//     stored copy would be a stale one;
//   - the RAW bytes stay authoritative. The holdback, the UncommittedBuffer, the
//     tail memo key and the committed record all keep the unmodified line, so the
//     line that is finally committed is byte-identical to the bytes that arrived;
//   - it is gated on the line being inline-incomplete for the WHOLE line. A line
//     that is also an open fence, a table row or a heading is block grammar, the
//     holdback owns it, and a transient closer on those bytes would desync the
//     state machine rather than style anything.
func frameTailLine(raw string, inCode, inTable bool) string {
	// The gate runs FIRST, on the raw bytes. Everything the holdback owns is
	// excluded here, so a fenced or tabular line never reaches the balancer at
	// all and the block state machine sees exactly what the stream sent.
	kind, incomplete := markdown.Incomplete(raw, inCode, inTable)
	if !incomplete || kind != markdown.BlockInline {
		return raw
	}
	balanced := markdown.BalanceTrailingLine(raw)
	if balanced == raw {
		return raw
	}
	// The balancer already verifies its own output; this second check is the
	// frame's own guard and it is one comparison, because a line that could not
	// be closed comes back as the identical string.
	if _, stillOpen := markdown.Incomplete(balanced, inCode, inTable); stillOpen {
		return raw
	}
	return balanced
}

// docRowPool returns the rendered rows of the document, extending the cached
// prefix rather than re-reading it.
//
// The returned slice is owned by the cache: callers must read it and must not
// retain or mutate it past the next frame. The one caller (refreshViewportContent)
// immediately copies it into the scroll pool, so ownership is unobservable from
// outside — which is the only way a shared growing slice stays safe without a
// copy per frame.
func (m *model) docRowPool(docLen int) []string {
	layout := m.docLayout
	if layout == nil || docLen <= 0 {
		return nil
	}
	width := layout.Width()
	// Everything except the last row is byte-stable for the duration of a
	// stream, so that is the boundary the cache is allowed to claim.
	stable := docLen - 1

	live := m.streaming &&
		m.docRowCache != nil &&
		m.docRowWidth == width &&
		m.docRowRecords == len(m.records) &&
		m.docRowCached <= stable

	if !live {
		// Headroom so the ordinary case — a stream appending a line or two per
		// frame — extends the cache IN PLACE. Without it every frame would
		// reallocate the backing array on its first append, which is the same
		// O(document) copy the cache exists to avoid, one frame later.
		rows := make([]string, docLen, docLen+rowCacheHeadroom)
		for i := 0; i < docLen; i++ {
			rows[i] = layout.Lines[i].RenderedStr
		}
		m.docRowCache = rows
		m.docRowWidth = width
		m.docRowRecords = len(m.records)
		m.docRowCached = stable
		m.docRowRebuilds++
		return rows
	}

	// Extend: rows [docRowCached, stable) are new-or-just-promoted, and the
	// volatile last row is always re-read so a re-rendered partial line is
	// never served from cache.
	for i := m.docRowCached; i < stable; i++ {
		m.docRowCache = append(m.docRowCache, layout.Lines[i].RenderedStr)
	}
	m.docRowCache = m.docRowCache[:docLen]
	m.docRowCache[docLen-1] = layout.Lines[docLen-1].RenderedStr
	m.docRowCached = stable
	return m.docRowCache
}

// hitMapFor returns the record-derived physical-row layout, memoized across
// frames in which the records did not change.
//
// The memo is deliberately scoped to a LIVE STREAM. Outside a stream the hit
// map is rebuilt every frame exactly as it always was, so no non-streaming
// behaviour — a record edited in place, a session resumed, a /clear — can
// observe a stale cache. Inside a stream the key is sound because records are
// frozen by construction; the key is checked anyway, so a mid-stream record
// mutation (an activity line, a trace summary) still forces a rebuild.
func (m *model) hitMapFor() []RowLayout {
	if !m.streaming {
		return buildFullHitMap(m)
	}
	width := m.PaneWidth()
	if width < 40 {
		width = 40
	}
	prefix := m.viewportContentPrefixHeight()
	if m.hitMapCacheLive &&
		m.hitMapCacheRecords == len(m.records) &&
		m.hitMapCachePrefix == prefix &&
		m.hitMapCacheWidth == width {
		return m.hitMapCache
	}
	rows := buildFullHitMap(m)
	m.hitMapCache = rows
	m.hitMapCacheRecords = len(m.records)
	m.hitMapCachePrefix = prefix
	m.hitMapCacheWidth = width
	m.hitMapCacheLive = true
	m.hitMapRebuilds++
	return rows
}

// framebufferNeeded reports whether the selection framebuffer has to be fresh
// in THIS frame.
//
// The framebuffer is read by exactly three things — the framebuffer selection
// window, the copy extractor, and the selection renderer — and every one of them
// is gated on a live mouse selection. It is otherwise pure cost: a per-row
// struct copy plus a Cell per column, repeated for the whole document on every
// frame of a stream.
//
// So the gate is: a live selection always needs it, and a stream with no
// selection does not. The second clause is safe because the frame on which a
// selection BEGINS runs through refreshViewportContent with mouseSel.Active
// already set — the same place this is decided — so the geometry a selection
// needs is built in the selection's first frame and never a frame later.
//
// A nil framebuffer is always needed, which is what keeps the first frame after
// a resize (and every headless harness that constructs a model by hand)
// rasterizing as it always did.
func (m *model) framebufferNeeded() bool {
	if m.framebuffer == nil {
		return true
	}
	if m.mouseSel.Active || m.mouseSel.Dragging {
		return true
	}
	return !m.streaming
}

// invalidateFrameCaches drops every per-frame cache.
//
// It is called at the two boundaries where "the document is append-only within
// a turn" stops being true for STRUCTURAL reasons: a new turn, and a cleared
// surface. It is deliberately not called for a resize or a record mutation —
// those are already keys on the caches themselves, which is the difference
// between a key (read from the state the cache derives from, so it cannot be
// forgotten) and a flag (set at every write site, so it can be).
//
// Calling it is always safe: a dropped cache is rebuilt on the next frame at
// the full cost, which is exactly what the pre-cache code always paid.
func (m *model) invalidateFrameCaches() {
	m.docRowCache = nil
	m.docRowCached = 0
	m.docRowRecords = 0
	m.docRowWidth = 0
	m.hitMapCache = nil
	m.hitMapCacheLive = false
	m.hitMapCacheRecords = 0
	m.hitMapCachePrefix = 0
	m.hitMapCacheWidth = 0
}

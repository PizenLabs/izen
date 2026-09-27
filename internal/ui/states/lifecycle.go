// Package states owns the PRE-EXECUTION lifecycle state machine: the explicit
// indicator states the view mounts while the runtime is producing a surface but
// no surface exists yet.
//
// The distinction this package draws is the whole point. "Streaming" and
// "executing" are POST-arrival states — they have content to show, so the
// viewport renders that content and any indicator is decoration. The states
// here are PRE-arrival: a table is still being constructed, a code block is
// still being formatted, an edit is still being staged, a tool is still being
// prepared, a workspace is still being mapped. During that window the viewport
// has ONE honest thing to say ("this is happening, it is not done") and
// everything else is noise.
//
// THREE INVARIANTS (each one a DoD clause):
//
//  1. ONE INDICATOR, ONE LINE. Exactly one pre-execution state can be mounted at
//     a time, and its indicator renders on exactly one physical row. A second
//     Enter() supersedes the first rather than stacking a second line — stacking
//     is how a viewport turns into a log of its own progress.
//
//  2. NO FABRICATED WORK. An indicator exists only for a state the runtime
//     actually entered. StateIdle is the resting state and renders nothing;
//     there is deliberately no "Working..." catch-all here, because a generic
//     claim is indistinguishable from a stalled process.
//
//  3. DETERMINISTIC TEXT. The indicator line for a state is a pure function of
//     (State, target) — see Describe. The view never rewords it, never appends
//     its own progress narration, and never renders a different string for the
//     same state, so the same work always reads the same way.
//
// The machine is a single-slot, monotonic-epoch register. It is safe for
// concurrent use because the runtime stages patches from worker goroutines
// while the UI goroutine renders: Enter/Exit take the write lock, Snapshot
// takes the read lock, and every epoch bump is observable so a late finalizer
// from a superseded state can be rejected instead of clobbering the live one.
package states

import (
	"strings"
	"sync"
)

// State is one pre-execution lifecycle state. The zero value is StateIdle, the
// resting state with no mounted indicator, so a zero-valued Machine is already
// coherent and never needs constructor-only invariants.
type State uint8

const (
	// StateIdle is the resting state: no pre-execution indicator is mounted
	// and the viewport renders no skeleton. It is the only non-pending state.
	StateIdle State = iota

	// StateTablePending — a table block has been recognised in the stream and
	// its grid is still being constructed. The rows/columns do not exist yet,
	// so no skeleton may claim a row count.
	StateTablePending

	// StateCodePending — a fenced code block has been recognised and its
	// syntax/lexing pass has not finished. No partial glyphs are rendered.
	StateCodePending

	// StateWorkspacePatch — a workspace edit has been compiled and is being
	// staged. The target is the file being edited; the indicator names it
	// because "which file" is the only thing a user can usefully watch here.
	StateWorkspacePatch

	// StateToolExecution — a tool invocation is being prepared. Nothing has
	// been handed to the subprocess or provider yet.
	StateToolExecution

	// StateAstIndexing — workspace context is being mapped (AST/index pass)
	// before the model is called.
	StateAstIndexing
)

// Indicator tags. Each state carries a bracketed class prefix so the one line
// on screen names WHAT KIND of work is pending without reading the sentence.
// They are deliberately distinct words, not abbreviations of each other, so a
// glance is enough to route the eye.
const (
	tagStruct   = "[struct]"
	tagCode     = "[code]"
	tagMutation = "[mutation]"
	tagExec     = "[exec]"
	tagIndex    = "[index]"
)

// Indicator clauses. Each state owns exactly one sentence; the trailing
// ellipsis is part of the clause, not appended by the caller, so an indicator
// can never render as "…." or lose its ellipsis on a partial update.
const (
	clauseTable    = "Constructing table view..."
	clauseCode     = "Formatting code block..."
	clausePatch    = "Staging edit"
	clauseTool     = "Preparing tool execution..."
	clauseIndexing = "Mapping workspace context..."
)

// DefaultTarget is the subject rendered for StateWorkspacePatch when the caller
// has not resolved a concrete file yet. It is a noun, never an ellipsis or an
// empty string, so the indicator always reads as a complete claim: an edit is
// being staged somewhere in the workspace.
const DefaultTarget = "workspace"

// String returns the machine name of the state. It is a stable identifier (used
// by tests and trace output), not the indicator line.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateTablePending:
		return "table-pending"
	case StateCodePending:
		return "code-pending"
	case StateWorkspacePatch:
		return "workspace-patch"
	case StateToolExecution:
		return "tool-execution"
	case StateAstIndexing:
		return "ast-indexing"
	default:
		return "unknown"
	}
}

// Pending reports whether the state mounts an indicator. Only pending states
// are mountable; StateIdle is the absence of an indicator, and any unknown
// value is treated as absent rather than silently rendered as work.
func (s State) Pending() bool {
	switch s {
	case StateTablePending, StateCodePending, StateWorkspacePatch,
		StateToolExecution, StateAstIndexing:
		return true
	default:
		return false
	}
}

// Targeted reports whether the state's indicator names a concrete subject.
// Only StateWorkspacePatch does today: a file path is a stable, checkable
// thing to watch, whereas a table or a code block has no subject a user could
// verify. A state that is not Targeted renders its clause verbatim and ignores
// any target passed to Describe.
func (s State) Targeted() bool {
	return s == StateWorkspacePatch
}

// Tag returns the bracketed class prefix for the state ("[struct]",
// "[mutation]", …), or "" for StateIdle.
func (s State) Tag() string {
	switch s {
	case StateTablePending:
		return tagStruct
	case StateCodePending:
		return tagCode
	case StateWorkspacePatch:
		return tagMutation
	case StateToolExecution:
		return tagExec
	case StateAstIndexing:
		return tagIndex
	default:
		return ""
	}
}

// Describe renders the canonical ONE-LINE indicator for the state.
//
// The five returned strings are the contract:
//
//	[struct] Constructing table view...
//	[code] Formatting code block...
//	[mutation] Staging edit @<file>...
//	[exec] Preparing tool execution...
//	[index] Mapping workspace context...
//
// The result never contains a newline: a mounted indicator occupies exactly one
// physical row, and a newline here would silently become a second row in the
// viewport. Any control character in target is folded to a space so a hostile
// file name cannot inject line structure either.
func (s State) Describe(target string) string {
	switch s {
	case StateTablePending:
		return tagStruct + " " + clauseTable
	case StateCodePending:
		return tagCode + " " + clauseCode
	case StateWorkspacePatch:
		return tagMutation + " " + clausePatch + " @" + subject(target) + "..."
	case StateToolExecution:
		return tagExec + " " + clauseTool
	case StateAstIndexing:
		return tagIndex + " " + clauseIndexing
	default:
		return ""
	}
}

// All returns every mountable state in canonical order. It exists so a caller
// (a renderer, a test, a diagnostic dump) can enumerate the whole vocabulary
// without hard-coding the list a second time.
func All() []State {
	return []State{
		StateTablePending,
		StateCodePending,
		StateWorkspacePatch,
		StateToolExecution,
		StateAstIndexing,
	}
}

// subject normalises a target into something safe to embed in the indicator
// line: trimmed, control-free, and non-empty.
func subject(target string) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return DefaultTarget
	}
	return Sanitize(t)
}

// Sanitize flattens a target so it can never introduce terminal structure into
// the one-line indicator: CR/LF/TAB and every other C0/C1 control becomes a
// single space, and consecutive spaces collapse. It is deliberately lossy —
// the indicator is a status line, not a faithful rendering of a path, and
// fidelity belongs to the finalized content that replaces the indicator.
func Sanitize(target string) string {
	if target == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(target))
	space := false
	for _, r := range target {
		if r < 0x20 || r == 0x7f {
			space = true
			continue
		}
		if r == ' ' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// ── Latch (hysteresis) ──────────────────────────────────────────────────────
//
// A Latch is a ONE-WAY hold. It turns ON at the first qualifying observation
// and stays ON through every subsequent observation until something explicitly
// releases it. Nothing in between can turn it off.
//
// It exists because a pre-execution indicator must not be derived from whatever
// the most recent chunk happened to look like. The failure it prevents is
// OSCILLATION: a table block's rows arrive as a burst of pipe-delimited lines
// separated by bare cell fragments, and a state derived from "does this chunk
// look like table content right now" flips on and off once per cell boundary. A
// viewport that mounts and releases the same one-row indicator dozens of times a
// second is a viewport that flickers — the row blinks, the document height jumps
// on every flip, and the shimmer glyph restarts from frame zero each time.
//
// The latch turns the same question into a one-way transition:
//
//	Engage()  — the construct was recognised (the opening pipes arrived).
//	On()      — read the hold; the indicator is mounted and STAYS mounted.
//	Release() — an EXPLICIT termination signal arrived (a complete line that is
//	            not part of the table, a blank line, or stream completion).
//
// Only Release may turn it off, and a Release with no hold is a no-op. The zero
// value is an open, released latch, so a struct-literal holder is coherent
// without a constructor.
//
// SCOPE. A Latch release is unkeyed: Release() ends whatever is held, and it is
// the CALLER's job to only call it for the hold it means. That is sufficient
// here because the latch is reconciled from an authoritative holdback read on
// the same turn it is written, so it cannot be stale. A release that must be
// scoped to one specific mount ("end the hold I opened, not whatever replaced
// it") is a different requirement and is keyed by epoch at the Machine level —
// see Snapshot.Epoch.
type Latch struct {
	mu       sync.RWMutex
	on       bool
	engaged  uint64
	released uint64
}

// On reports whether the latch is currently engaged (held).
func (l *Latch) On() bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.on
}

// Engage turns the latch ON and reports whether this call is what turned it on.
// Engaging an already-engaged latch is a no-op reporting false, so "the hold
// began here" stays observable — and, more importantly, so nothing that happens
// while the hold is live can be mistaken for the start of a new one.
func (l *Latch) Engage() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.on {
		return false
	}
	l.on = true
	l.engaged++
	return true
}

// Release turns the latch OFF and reports whether it actually held. Releasing an
// open latch is a no-op reporting false: a redundant termination signal is
// therefore observable rather than silently successful, and — the point of the
// whole type — a second release can never re-release a hold that a LATER engage
// has already installed.
func (l *Latch) Release() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.on {
		return false
	}
	l.on = false
	l.released++
	return true
}

// Set assigns the latch and reports whether the value changed. It is the
// reconcile form for a caller whose latch is DERIVED from authoritative state
// (a holdback buffer that is the real single source of truth): the latch mirrors
// that state every tick without ever inventing a hold of its own.
func (l *Latch) Set(on bool) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.on == on {
		return false
	}
	l.on = on
	if on {
		l.engaged++
	} else {
		l.released++
	}
	return true
}

// Counts returns how many times the latch has been engaged and released over its
// lifetime. Equal counts mean the latch is quiescent; a rising engage count with
// a flat release count is the oscillation this type exists to make impossible.
func (l *Latch) Counts() (engaged, released uint64) {
	if l == nil {
		return 0, 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.engaged, l.released
}

// Snapshot is an immutable, lock-free view of the machine. It is what the
// renderer reads: a state, its target, and the epoch that identifies WHICH
// mount this snapshot belongs to.
type Snapshot struct {
	State  State
	Target string
	// Epoch increments on every accepted transition (Enter and Exit). A
	// finalizer carrying a stale epoch is rejected, so a late event from a
	// superseded state can never finalize the indicator that replaced it.
	Epoch uint64
	// Active mirrors State.Pending() for readers that should not have to
	// reach through the State type to know whether anything is mounted.
	Active bool
}

// Indicator returns the snapshot's rendered indicator line ("" when inactive).
func (s Snapshot) Indicator() string {
	if !s.Active {
		return ""
	}
	return s.State.Describe(s.Target)
}

// Machine is the single-slot pre-execution indicator register. The zero value
// is a valid, idle machine.
type Machine struct {
	mu     sync.RWMutex
	state  State
	target string
	epoch  uint64
}

// NewMachine returns an idle machine.
func NewMachine() *Machine {
	return &Machine{}
}

// Enter mounts the indicator for st, superseding whatever was mounted before,
// and returns the resulting snapshot. Enter is a no-op for a non-pending state
// (StateIdle, or any unknown value): it reports false and leaves the machine
// untouched, so a caller cannot mount an indicator that renders nothing.
//
// Supersession is deliberate and total: the viewport holds ONE line, so a
// second pre-execution state replaces the first instead of queueing behind it.
// The epoch advances on every accepted transition so a late finalizer from the
// superseded state is detectable.
func (m *Machine) Enter(st State, target string) (Snapshot, bool) {
	if m == nil || !st.Pending() {
		return m.Snapshot(), false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = st
	m.target = strings.TrimSpace(Sanitize(target))
	m.epoch++
	return Snapshot{
		State:  m.state,
		Target: m.target,
		Epoch:  m.epoch,
		Active: true,
	}, true
}

// Exit unmounts the indicator and returns the resulting snapshot. It reports
// false when nothing was mounted, so a redundant finalize is observable rather
// than silently successful. Exiting twice is safe and idempotent.
func (m *Machine) Exit() (Snapshot, bool) {
	if m == nil {
		return Snapshot{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.state.Pending() {
		return Snapshot{Epoch: m.epoch}, false
	}
	m.state = StateIdle
	m.target = ""
	m.epoch++
	return Snapshot{Epoch: m.epoch}, true
}

// Advance moves a mounted indicator to st/target WITHOUT treating it as a
// supersession: the epoch is left alone, so a finalizer that was already in
// flight for this mount still applies. It is the seam for a state that refines
// its subject mid-flight (the file path resolves after the edit begins).
//
// A non-pending st exits the machine, matching Enter's contract that a
// non-pending state is the absence of an indicator.
func (m *Machine) Advance(st State, target string) Snapshot {
	if m == nil {
		return Snapshot{}
	}
	if !st.Pending() {
		s, _ := m.Exit()
		return s
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.state.Pending() {
		// Advancing a resting machine is a first mount: bump the epoch so the
		// caller can still treat it as a transition.
		m.epoch++
	}
	m.state = st
	if t := strings.TrimSpace(Sanitize(target)); t != "" {
		m.target = t
	}
	return Snapshot{
		State:  m.state,
		Target: m.target,
		Epoch:  m.epoch,
		Active: true,
	}
}

// Relabel replaces only the subject of the mounted indicator, leaving the
// state and the epoch untouched. An empty label keeps the current one, so a
// caller that briefly has no target cannot blank a line it already resolved.
func (m *Machine) Relabel(target string) Snapshot {
	if m == nil {
		return Snapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.state.Pending() {
		return Snapshot{Epoch: m.epoch}
	}
	if t := strings.TrimSpace(Sanitize(target)); t != "" {
		m.target = t
	}
	return Snapshot{
		State:  m.state,
		Target: m.target,
		Epoch:  m.epoch,
		Active: true,
	}
}

// Snapshot returns the current mount. It is nil-safe and lock-free enough for
// the render path (a single RLock, no I/O, no allocation).
func (m *Machine) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Snapshot{
		State:  m.state,
		Target: m.target,
		Epoch:  m.epoch,
		Active: m.state.Pending(),
	}
}

// Active reports whether an indicator is currently mounted.
func (m *Machine) Active() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.Pending()
}

// Epoch returns the current transition counter. A zero epoch means nothing has
// ever been mounted. Callers use it to observe supersession: a snapshot whose
// epoch has moved on describes a DIFFERENT mount than the one that produced it,
// which is how a late finalizer is recognised without trusting a message to
// still be relevant.
func (m *Machine) Epoch() uint64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.epoch
}

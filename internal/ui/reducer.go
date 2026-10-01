package ui

// ── Linear Sequential Narrative Projection (Phase 15) ────────────────────────
//
// The Main Viewport used to be one flat slice of records, appended to by every
// source that had something to say. Four consequences followed, and all four are
// the same root cause — a flat slice has no notion of what a STEP is:
//
//  1. INFRASTRUCTURE POLLUTION. A loop transition, a barrier wait, a grant and a
//     preflight decision are facts about the runtime talking to itself, but they
//     arrived through the same append as a human sentence, so the viewport filled
//     with machine tags at the exact moment a person was deciding whether to
//     trust a result.
//
//  2. DUPLICATE STATUS. A step that emits its status on every tick appended a
//     line per tick. "Model waiting..." repeated eleven times is not eleven facts
//     — it is one fact and ten echoes, and it displaces the steps around it.
//
//  3. TELEMETRY IN THE NARRATIVE. A live token count or cost figure appended as
//     a record is unreadable by construction: the previous value is already
//     gone, so what the reader sees is a number with no reference point.
//
//  4. NO SEALING. Nothing in a flat slice distinguishes "the current step" from
//     "a step that already happened", so the renderer had no way to render the
//     first differently from the rest, and every consumer re-derived the
//     distinction by string-matching roles.
//
// The fix is a TWO-TIER state:
//
//	MainViewportState = HistoryNodes (immutable) + ActiveNode (mutable)
//
// and a REDUCER that sits between the event bus and the viewport renderer. The
// reducer is the single place that decides which tier a piece of content lands
// in, and the Single Active Node Invariant is what makes the projection linear:
// at most one node is ever mutable, and a step transition SEALS it.
//
// The invariant, stated precisely because everything else follows from it:
//
//	Exactly one ActiveNode exists at any time.
//	A record of a DIFFERENT kind seals the current ActiveNode into History.
//	A record of the SAME kind REPLACES the ActiveNode's payload — it does not
//	append.  ← this is what kills the duplicate status line.
//	Infrastructure telemetry never reaches either tier; it is diverted to Trace.
//	Telemetry metrics never reach either tier; they are routed to the Sidebar HUD.
//
// Two consequences are worth naming. First, the narrative is a SEQUENCE, not a
// log: history is append-only and ordered, so scrolling back is meaningful.
// Second, the reducer is a PURE function of its input sequence — the same events
// in the same order always produce the same state, which is what makes "the
// viewport is clean and linear" a testable property instead of a screenshot.

import (
	"strings"
	"sync"
)

// NarrativeKind classifies a Main Viewport node. The set is the ALLOWED
// vocabulary: a record that is not one of these is infrastructure, and
// infrastructure does not belong in the narrative.
//
// The four human-centric kinds are the ones a person actually acts on. Each
// corresponds to a question the reader is asking: what did it think (Thought),
// what did it touch (ToolExecution), what would change (DiffPreview), and is it
// done (ObjectiveVerdict).
type NarrativeKind int

const (
	// NodeUser is the human's own turn. It is not a step, so it never becomes an
	// ActiveNode — it is a boundary marker in the history.
	NodeUser NarrativeKind = iota
	// NodeAnswer is the assistant's own prose: the conversational answer itself.
	NodeAnswer
	// NodeThought is one reasoning step, rendered as a human milestone.
	NodeThought
	// NodeToolExecution is one action taken against the workspace.
	NodeToolExecution
	// NodeDiffPreview is a proposed change shown for review.
	NodeDiffPreview
	// NodeObjectiveVerdict is the terminal evaluation of an objective.
	NodeObjectiveVerdict
	// NodeSystem is a plain human-facing notice.
	NodeSystem
	// NodeError is a human-facing failure report.
	NodeError
)

// String renders the kind for diagnostics and structured telemetry.
func (k NarrativeKind) String() string {
	switch k {
	case NodeUser:
		return "user"
	case NodeAnswer:
		return "answer"
	case NodeThought:
		return "thought"
	case NodeToolExecution:
		return "tool_execution"
	case NodeDiffPreview:
		return "diff_preview"
	case NodeObjectiveVerdict:
		return "objective_verdict"
	case NodeSystem:
		return "system"
	case NodeError:
		return "error"
	default:
		return "unknown"
	}
}

// streams reports whether a kind's content ARRIVES INCREMENTALLY.
//
// This distinction is what makes step transitions detectable without a step
// identity field, and it is not cosmetic:
//
//   - A STREAMED kind (the answer) grows fragment by fragment. Sealing each
//     fragment would turn one answer into dozens of nodes and destroy the linear
//     reading, so consecutive records of a streamed kind merge into one node.
//   - A DISCRETE kind (a step, an action, a verdict, a failure) is a complete
//     statement emitted once. Two of them are two steps by construction — the
//     runtime announced both — so a change of kind, or of turn, seals.
//
// Deriving the boundary from how the content is produced rather than from string
// matching is what keeps the projection deterministic. A heuristic on the text
// ("does this look like a new step?") would classify "Inspecting index.html" and
// "Applying changes" as the same step and merge a five-step run into one node.
func (k NarrativeKind) streams() bool { return k == NodeAnswer }

// narrativeKindFor maps a render role onto the narrative vocabulary. The mapping
// is total and lives here, once, so no call site has to know that a status line
// is a Thought and a diff is a DiffPreview.
func narrativeKindFor(r role) NarrativeKind {
	switch r {
	case roleUser:
		return NodeUser
	case roleAI:
		return NodeAnswer
	case roleError:
		return NodeError
	case roleStatus, roleActivity:
		// Both are the runtime talking about what it is doing. The finer
		// distinction between a thought, an action and a verdict is carried in
		// the content, not the role — see classifyNarrative.
		return NodeThought
	case roleCode:
		return NodeDiffPreview
	default:
		return NodeSystem
	}
}

// classifyNarrative refines a role-level kind using the CONTENT the runtime
// produced. It is deliberately conservative: an unclassifiable step stays a
// Thought (a human milestone) rather than being upgraded into a diff or a
// verdict, because over-claiming a kind is how a preview turns out to have been
// a plan and a verdict turns out to have been a guess.
func classifyNarrative(r role, text string) NarrativeKind {
	base := narrativeKindFor(r)
	if base != NodeThought {
		return base
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "diff"), strings.Contains(lower, "proposed change"),
		strings.Contains(lower, "preview"):
		return NodeDiffPreview
	case strings.Contains(lower, "objective"), strings.Contains(lower, "verified"),
		strings.Contains(lower, "verification"), strings.Contains(lower, "applied and verified"),
		strings.Contains(lower, "not proven"), strings.Contains(lower, "no files were modified"):
		return NodeObjectiveVerdict
	case strings.Contains(lower, "applied change"), strings.Contains(lower, "applied patch"),
		strings.Contains(lower, "created "), strings.Contains(lower, "updated "),
		strings.Contains(lower, "inspected "), strings.Contains(lower, "ran "):
		return NodeToolExecution
	default:
		return NodeThought
	}
}

// HistoryNode is a SEALED viewport node. Once a node is in History it is never
// mutated: the fields are value types, the reducer copies on seal, and nothing
// downstream holds a pointer into it. That is what makes the history a stable
// scrollback target rather than a view that changes under the reader.
type HistoryNode struct {
	// Kind is the narrative classification.
	Kind NarrativeKind
	// Role and Text are the render inputs, preserved verbatim so the projection
	// never rewrites what the runtime said.
	Role role
	Text string
	// TurnID is the conversation turn the node belongs to.
	TurnID uint64
	// Seq is the node's position in the sealed sequence. It is monotonically
	// increasing and gap-free, so a consumer can detect a lost node without
	// inspecting content.
	Seq uint64
}

// record projects the node back into the flat render input the viewport consumes.
// It is a pure function with no receiver state, so the projection can be
// recomputed from history at any time and must always agree.
func (n HistoryNode) record() record {
	return record{role: n.Role, text: n.Text, turnID: n.TurnID}
}

// ActiveNode is the MUTABLE node: the step currently in progress. It is the only
// node in the state that may change, and that is precisely what makes the
// Single Active Node Invariant enforceable — there is nowhere else for a
// duplicate to hide.
type ActiveNode struct {
	// Kind, Role, Text and TurnID mirror HistoryNode; see its field docs.
	Kind   NarrativeKind
	Role   role
	Text   string
	TurnID uint64
	// Seq is the sequence number this node WILL take when it is sealed. Reserving
	// it up front is what lets a caller reference the active node without
	// depending on when it becomes history.
	Seq uint64
	// Revisions counts how many times the payload has been replaced in place. A
	// revision count above zero is the duplicate-status mechanism working: the
	// same step restated itself N times and occupied exactly one node.
	Revisions int
	// SealedAt is the monotonic index at which this node entered History. It is
	// zero while the node is still active.
	SealedAt int
}

// record projects the active node into the flat render input.
func (n *ActiveNode) record() record {
	if n == nil {
		return record{}
	}
	return record{role: n.Role, text: n.Text, turnID: n.TurnID}
}

// MainViewportState is the two-tier projection the viewport renders.
//
// The zero value is valid and empty: no history, no active node. A state with a
// nil ActiveNode is a state in which every step has been sealed, which is
// exactly what a reader sees between turns.
type MainViewportState struct {
	// History is the sealed, ordered, immutable sequence. It only ever grows at
	// the end, and a growth is a seal.
	History []HistoryNode
	// Active is the single mutable node, or nil when no step is in flight.
	Active *ActiveNode
	// SealedCount is the number of nodes ever sealed. It equals len(History) and
	// exists so a test can assert the invariant without recomputing it.
	SealedCount int
	// ReplacedCount counts in-place revisions of the active node. It is the
	// direct measure of how much duplicate status the reducer suppressed.
	ReplacedCount int
	// DivertedCount counts records refused the narrative and sent to Trace.
	DivertedCount int
}

// Records projects the two tiers into the flat, ordered render input the
// viewport consumes: sealed history first, then the active node.
//
// The active node is included LAST and unconditionally, because an in-flight
// step is part of the sequence the reader is following — omitting it would make
// the last line of the viewport a lie. What is NOT included is a second copy of
// a node that is already in history: the invariant guarantees that cannot happen.
func (s MainViewportState) Records() []record {
	out := make([]record, 0, len(s.History)+1)
	for _, node := range s.History {
		out = append(out, node.record())
	}
	if s.Active != nil {
		out = append(out, s.Active.record())
	}
	return out
}

// Len returns the total number of nodes, sealed and active. An absent active node
// contributes ZERO — a state with no step in flight has nothing to render for it,
// and counting a phantom node here would make every post-run measurement off by
// one.
func (s MainViewportState) Len() int {
	if s.Active == nil {
		return len(s.History)
	}
	return len(s.History) + 1
}

// ProjectionDecision is the reducer's verdict on one input record. It is a value
// so a caller can assert on WHY a record was refused, not merely that it was.
type ProjectionDecision int

const (
	// ProjectSealed: the record started a new step; the previous active node was
	// sealed and this record became the new active node.
	ProjectSealed ProjectionDecision = iota
	// ProjectOpened: the record became the active node with nothing to seal
	// before it.
	ProjectOpened
	// ProjectReplaced: the record restated the current step in place. No new
	// node was created — this is the duplicate-status path.
	ProjectReplaced
	// ProjectAppended: the record extended the current step's text.
	ProjectAppended
	// ProjectDiverted: the record is infrastructure telemetry and was sent to
	// Trace instead of the narrative.
	ProjectDiverted
	// ProjectRejected: the record carried no content and was dropped.
	ProjectRejected
)

// String renders the decision for diagnostics.
func (d ProjectionDecision) String() string {
	switch d {
	case ProjectSealed:
		return "sealed"
	case ProjectOpened:
		return "opened"
	case ProjectReplaced:
		return "replaced"
	case ProjectAppended:
		return "appended"
	case ProjectDiverted:
		return "diverted"
	case ProjectRejected:
		return "rejected"
	default:
		return "unknown"
	}
}

// ProjectionReducer is the single authority between the event bus and the
// viewport renderer. It owns the MainViewportState and the Trace divert, and it
// is the only writer of both.
//
// It is safe for concurrent use. Domain events are delivered on bus dispatch
// goroutines while the update loop renders, so without the lock the reducer
// would race its own state — and a race here is not a crash, it is a duplicated
// or lost narrative line, which is the exact defect the phase is about.
type ProjectionReducer struct {
	mu    sync.RWMutex
	state MainViewportState
	// trace is the divert target for infrastructure telemetry. Nil disables
	// diversion bookkeeping but never admits the line to the narrative: an
	// unwired Trace is a reason to lose the information, not a reason to show it
	// in the wrong place.
	trace *TelemetryDemuxer
	// hud is the in-place telemetry surface. Nil disables metric routing; the
	// narrative is unaffected either way.
	hud *SidebarHUD
	// nextSeq is the sequence number the next node will reserve.
	nextSeq uint64
	// allowUserNodes keeps NodeUser records in the history. It is on by default;
	// the flag exists so a surface that renders the prompt in its own chrome can
	// turn the history into steps-only without changing the reducer.
	allowUserNodes bool
}

// NewProjectionReducer wires a reducer to its divert and telemetry surfaces.
// A nil trace or hud is legal and leaves that capability inert.
func NewProjectionReducer(trace *TelemetryDemuxer, hud *SidebarHUD) *ProjectionReducer {
	return &ProjectionReducer{
		trace:          trace,
		hud:            hud,
		allowUserNodes: true,
	}
}

// Trace returns the divert target, so a caller can assert that information was
// moved rather than discarded.
func (r *ProjectionReducer) Trace() *TelemetryDemuxer {
	if r == nil {
		return nil
	}
	return r.trace
}

// HUD returns the in-place telemetry surface.
func (r *ProjectionReducer) HUD() *SidebarHUD {
	if r == nil {
		return nil
	}
	return r.hud
}

// State returns a copy of the current projection. The History slice is copied so
// a caller cannot reach in and mutate a sealed node behind the reducer's back.
func (r *ProjectionReducer) State() MainViewportState {
	if r == nil {
		return MainViewportState{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	state := r.state
	if len(r.state.History) > 0 {
		state.History = make([]HistoryNode, len(r.state.History))
		copy(state.History, r.state.History)
	}
	if r.state.Active != nil {
		active := *r.state.Active
		state.Active = &active
	}
	return state
}

// Records projects the current state into the flat render input.
func (r *ProjectionReducer) Records() []record {
	if r == nil {
		return nil
	}
	return r.State().Records()
}

// RebindTrace points the divert at a new Trace buffer while PRESERVING both
// tiers of the projection.
//
// It exists because the demuxer is created lazily and can be replaced by a
// /clear, a re-init or a test harness. Constructing a fresh reducer would be
// simpler and wrong: it would silently drop every node already projected, so the
// conversation would lose its history because an internal buffer was swapped.
// Keeping the state and changing only the destination is the behaviour the
// boundary actually requires.
func (r *ProjectionReducer) RebindTrace(trace *TelemetryDemuxer) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace = trace
}

// Reset clears both tiers and the counters. The telemetry surface is NOT
// cleared: a session's token and cost totals outlive the transcript that produced
// them, and zeroing them on /clear would report a spend that did not happen.
func (r *ProjectionReducer) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = MainViewportState{}
	r.nextSeq = 0
}

// Reduce projects one record and returns the decision.
//
// The order of the tests IS the policy, and each one is a place a record can be
// refused before it becomes narrative:
//
//	empty      → rejected   (nothing to say)
//	machine    → diverted   (Trace owns it, always)
//	user turn  → sealed     (a boundary marker, never an active step)
//	duplicate  → replaced   (the duplicate-status fix)
//	continuation → appended (a streamed kind growing in place)
//	new step   → sealed+open
func (r *ProjectionReducer) Reduce(rec record) ProjectionDecision {
	if r == nil {
		return ProjectRejected
	}
	text := strings.TrimSpace(rec.text)
	if text == "" {
		return ProjectRejected
	}
	// ── INFRASTRUCTURE DIVERSION ─────────────────────────────────────
	// Checked FIRST and unconditionally, so no caller can reach the narrative
	// with a machine line by ordering its own checks differently. The line is
	// handed to Trace verbatim: the boundary moves information, it never
	// discards it, and it never rewrites it.
	if !IsUserFacingEvidence(text) {
		r.mu.Lock()
		r.state.DivertedCount++
		trace := r.trace
		r.mu.Unlock()
		if trace != nil {
			trace.Ingest(text)
		}
		return ProjectDiverted
	}

	kind := classifyNarrative(rec.role, text)

	r.mu.Lock()
	defer r.mu.Unlock()

	// A user turn is a boundary, not a step. Sealing first is what keeps the
	// previous step's final state in history before the reader's next input
	// appears.
	if kind == NodeUser {
		if !r.allowUserNodes {
			return ProjectRejected
		}
		r.sealLocked()
		r.state.History = append(r.state.History, r.reserveLocked(kind, rec, text))
		return ProjectSealed
	}

	// ── THE SINGLE ACTIVE NODE INVARIANT ─────────────────────────────
	// Two rules, applied in this order, and the order is the policy.
	//
	// (a) DUPLICATE SUPPRESSION, first. A byte-identical restatement of the
	//     current step replaces the payload in place. The step occupies exactly one
	//     node however many times it is announced, which is what makes
	//     "Model waiting..." appear once rather than once per tick. This is checked
	//     BEFORE any sealing decision because a duplicate is not a new step — it
	//     is the same step saying itself again.
	//
	// (b) STEP TRANSITION, second. A streamed kind of the same kind in the same
	//     turn continues the current node; anything else seals it and opens a new
	//     one. History is append-only, so the sequence stays linear.
	//
	// Turn zero is exempt from the turn comparison — it means "no turn has been
	// opened yet" (a fresh or headless surface), not "turn number zero", so every
	// turn-less record belongs to the same ambient context rather than to its own
	// step.
	if r.state.Active != nil {
		if r.state.Active.Text == text {
			r.state.Active.Revisions++
			r.state.ReplacedCount++
			return ProjectReplaced
		}
		if kind.streams() && r.state.Active.Kind == kind && !turnsDiffer(r.state.Active.TurnID, rec.turnID) {
			// A continuation, not a repeat: a streaming answer grows line by line.
			// The node's kind is re-derived so a step that began as a Thought and
			// turned out to be a diff preview is classified by what it turned out to
			// be.
			r.state.Active.Text = r.state.Active.Text + "\n" + text
			r.state.Active.Revisions++
			r.state.ReplacedCount++
			if refined := classifyNarrative(rec.role, text); refined != r.state.Active.Kind && refined != NodeThought {
				r.state.Active.Kind = refined
			}
			return ProjectAppended
		}
	}

	sealed := false
	if r.state.Active != nil {
		r.sealLocked()
		sealed = true
	}
	r.state.Active = &ActiveNode{
		Kind:   kind,
		Role:   rec.role,
		Text:   text,
		TurnID: rec.turnID,
		Seq:    r.nextSeq,
	}
	r.nextSeq++
	if sealed {
		return ProjectSealed
	}
	return ProjectOpened
}

// SealActive seals the current active node, if any. The viewport renderer calls
// it when an execution reaches a terminal state, so the last step of a run
// enters history in the same frame the run ends rather than lingering as a
// mutable node that no longer has anything to mutate.
func (r *ProjectionReducer) SealActive() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sealLocked()
}

// sealLocked moves the active node into history. It is the ONLY writer of
// History, which is what guarantees history is append-only and ordered.
func (r *ProjectionReducer) sealLocked() {
	if r.state.Active == nil {
		return
	}
	r.state.History = append(r.state.History, HistoryNode{
		Kind:   r.state.Active.Kind,
		Role:   r.state.Active.Role,
		Text:   r.state.Active.Text,
		TurnID: r.state.Active.TurnID,
		Seq:    r.state.Active.Seq,
	})
	r.state.SealedCount = len(r.state.History)
	r.state.Active = nil
}

// turnsDiffer reports whether two turn identities represent different
// conversation turns. Turn zero means "no turn opened yet", so it is compatible
// with itself and with a real turn for the purpose of step identity: a fresh
// surface must still project its opening activity as a linear sequence rather
// than sealing a node per record.
func turnsDiffer(a, b uint64) bool {
	return a != 0 && b != 0 && a != b
}

// reserveLocked appends a sealed node directly — used for boundary markers,
// which are history by definition and never have an active lifetime.
func (r *ProjectionReducer) reserveLocked(kind NarrativeKind, rec record, text string) HistoryNode {
	node := HistoryNode{
		Kind:   kind,
		Role:   rec.role,
		Text:   text,
		TurnID: rec.turnID,
		Seq:    r.nextSeq,
	}
	r.nextSeq++
	return node
}

// RouteTelemetry writes one metric into the in-place HUD. It is the ONLY way
// telemetry reaches a surface, and it cannot reach the narrative: the HUD has no
// record sink and the reducer's narrative tiers are unreachable from here.
//
// An unknown metric is a no-op rather than a new slot. Letting callers invent
// slot names is exactly how a fixed layout becomes a variable one.
func (r *ProjectionReducer) RouteTelemetry(metric HUDMetric, value string) {
	if r == nil {
		return
	}
	r.mu.RLock()
	hud := r.hud
	r.mu.RUnlock()
	if hud == nil {
		return
	}
	switch metric {
	case HUDContextTokens, HUDCost, HUDMCPStatus:
		hud.Set(metric, value)
	}
}

// Audit renders the sealed history as plain text. It exists for tests and for
// $inspect-style diagnostics: asserting that the viewport is free of machine
// tags should not require reconstructing a render pipeline to read it.
func (r *ProjectionReducer) Audit() []string {
	if r == nil {
		return nil
	}
	state := r.State()
	out := make([]string, 0, state.Len())
	for _, node := range state.History {
		out = append(out, node.Text)
	}
	if state.Active != nil {
		out = append(out, state.Active.Text)
	}
	return out
}

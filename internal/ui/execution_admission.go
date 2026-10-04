package ui

import (
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/workflow"
	domainorch "github.com/PizenLabs/izen/internal/domain/orchestration"
)

// phaseBuildForRecovery is the logical phase a run whose evidence recorded no
// human boundary is restored into: the executable position, because a run that
// was mid-execution is owed its execution — not a terminal verdict it never
// reached.
const phaseBuildForRecovery = domainorch.PhaseBuild

// ── EXECUTION ADMISSION ─────────────────────────────────────────────────────
//
// ONE QUESTION, ANSWERED BEFORE ANY WORK: may a new execution run start right
// now?
//
// The reported defect was that this question was answered LATE — after intent
// parsing, after autonomy routing, after the workflow phase had already been
// switched into build, and after an autonomy trace had been rendered. The
// operator saw a full autonomy decision, a BUILDING header and a provider-shaped
// execution, and only then:
//
//	terminal failure: an execution is already active or parked at a human
//	boundary — resume or abort it first
//
// Nothing had run, but the trace implied it had. Admission now happens at the
// authoritative boundary: BEFORE the mode switch, BEFORE the decision render,
// BEFORE any planning. A refusal costs no provider call, no plan, no mutation and
// no misleading trace.
//
// The three entities stay distinct:
//
//	Conversation Session — messages, context revisions, history, token metrics
//	Execution Run       — objective, scope, targets, candidate, workflow state,
//	                      failure ledger, authorization/mutation/verification
//	                      state, evidence, run identity
//	Human Boundary      — awaiting execution authorization / awaiting mutation
//	                      authorization
//
// A new conversation does not terminate an execution, and an execution is never
// silently inherited by one.

// ParkedExecution is the authoritative description of a run that is alive but
// blocked on a human decision. Every field is read from the runtime's own state;
// none of it is inferred from a UI flag.
type ParkedExecution struct {
	// RunID is the runtime's stable run identity for the parked execution.
	RunID string
	// State is the runtime's own lifecycle label for the boundary.
	State string
	// Targets is the authoritative target set the run holds.
	Targets []string
	// CandidateID is the held mutation candidate, when the boundary is one.
	CandidateID string
	// Resumable reports whether a resume decision exists at all.
	Resumable bool
}

// parkedExecutionSummary reads the runtime's authoritative parked-run facts.
//
// It reads the DRIVER (the owner of the loop and the human boundary) and the
// WORKFLOW state machine (the owner of the lifecycle position) — never a UI
// flag. A UI that reports "nothing is parked" while the driver holds a boundary
// is the same defect the admission check exists to prevent.
func (m *model) parkedExecutionSummary() (ParkedExecution, bool) {
	if m == nil || m.autonomousDriver == nil {
		return ParkedExecution{}, false
	}
	// A held boundary is the runtime's own statement that a run is waiting.
	b := m.autonomousDriver.Boundary()
	if b == nil {
		return ParkedExecution{}, false
	}
	summary := ParkedExecution{
		RunID:       m.autonomousRunID(),
		State:       parkedBoundaryStateLabel(b),
		Targets:     append([]string(nil), b.Targets...),
		CandidateID: b.CandidateID,
		Resumable:   b.Resumable,
	}
	if summary.CandidateID == "" {
		summary.CandidateID = b.PatchID
	}
	if summary.State == "" {
		summary.State = m.workflowLifecycleLabel()
	}
	return summary, true
}

// autonomousRunID returns the driver's stable run identity, or "" when the
// driver does not publish one.
func (m *model) autonomousRunID() string {
	if m == nil || m.autonomousDriver == nil {
		return ""
	}
	if id := m.autonomousDriver.RunID(); id != "" {
		return id
	}
	return m.workflowLifecycleLabel()
}

// workflowLifecycleLabel renders the authoritative lifecycle position. A parked
// run is NEVER reported as idle: "idle" is the resting position of a run that
// owes nothing, which is the one thing a parked run is not.
func (m *model) workflowLifecycleLabel() string {
	if m == nil || m.workflowSM == nil {
		return "unknown"
	}
	st := m.workflowSM.State()
	if st.Parked() {
		return st.String()
	}
	return st.String()
}

// parkedBoundaryStateLabel names the human boundary precisely. "awaiting mutation
// authorization" and "awaiting execution authorization" are different questions
// to a human, and conflating them is what made one generic approval card serve
// two incompatible decisions.
func parkedBoundaryStateLabel(b *autonomy.HumanBoundary) string {
	if b == nil {
		return ""
	}
	switch b.Action {
	case autonomy.HumanBoundaryApproval:
		return "awaiting mutation authorization"
	case autonomy.HumanBoundaryDecomposition:
		return "awaiting plan authorization"
	case autonomy.HumanBoundaryClarify:
		return "awaiting target selection"
	case autonomy.HumanBoundaryProposal:
		return "awaiting recovery decision"
	default:
		return "paused"
	}
}

// admitNewExecutionRun is the authoritative admission check for a NEW execution
// run. It returns a truthful refusal when another run is active or parked.
//
// It is called BEFORE the mode switch and BEFORE the autonomy decision is
// rendered, so a refusal costs zero provider calls, zero planning and zero
// mutation — and shows no autonomy trace that would imply work began.
//
// A refusal is NOT a failure: it is the runtime declining to start a second
// execution while one is still owed work. The operator is offered the two real
// choices — resume the parked run, or abort it — because those are the only two
// things that can legitimately clear the lane.
func (m *model) admitNewExecutionRun() bool {
	parked, ok := m.parkedExecutionSummary()
	if !ok {
		return true
	}
	m.renderExecutionBlocked(parked)
	return false
}

// renderExecutionBlocked renders the refusal. It states which run is parked, in
// which state, over which target, that nothing was started, and what the
// operator can do about it.
func (m *model) renderExecutionBlocked(parked ParkedExecution) {
	var sb strings.Builder
	sb.WriteString(permissionTitleStyle.Render(Icon.Warning + " EXECUTION BLOCKED"))
	sb.WriteString("\n")
	if parked.RunID != "" {
		sb.WriteString(permissionDescStyle.Render("Run:") + " " + permissionTargetStyle.Render(parked.RunID) + "\n")
	}
	sb.WriteString(permissionDescStyle.Render("State:") + " " + permissionTargetStyle.Render(parked.State) + "\n")
	if len(parked.Targets) > 0 {
		sb.WriteString(permissionDescStyle.Render("Target:") + " " + permissionTargetStyle.Render(strings.Join(parked.Targets, ", ")) + "\n")
	}
	if parked.CandidateID != "" {
		sb.WriteString(permissionDescStyle.Render("Candidate:") + " " + permissionTargetStyle.Render(parked.CandidateID) + "\n")
	}
	sb.WriteString("  " + infoStyle.Render("No new execution started.") + "\n")
	sb.WriteString("  " + mutedStyle.Render("No provider call, no planning and no mutation were performed.") + "\n")
	sb.WriteString("  " + mutedStyle.Render("Choose: [Resume] the parked run · [Abort] it and start fresh") + "\n")
	// The full execution truth, so the operator can always answer "what run is
	// this, what is it waiting for, and has anything been mutated?" without
	// guessing — including what REMAINS to be done.
	if remaining := m.Truth().Remaining; len(remaining) > 0 {
		sb.WriteString("  " + mutedStyle.Render("Still owed by the parked run: "+strings.Join(remaining, "; ")) + "\n")
	}

	m.push(roleError, sb.String())
	// The refusal must not impersonate an execution: release the foreground
	// operation and make sure no spinner or phase survives it.
	m.autonomousActive = false
	m.resolveApprovalState()
	m.push(roleSystem, infoStyle.Render("  Press Enter to resume the parked execution, or Ctrl+C to abort it."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
}

// reportParkedExecutionAfterNew states, immediately after a new CONVERSATION is
// started, that a previous EXECUTION run is still parked and remains resumable.
//
// Without this the operator is told "fresh session" and then discovers a live run
// through a late, unexplained refusal — the exact contradiction that made `/new`
// unusable while a run was parked.
func (m *model) reportParkedExecutionAfterNew(parked ParkedExecution, present bool) {
	if !present {
		m.push(roleSystem, infoStyle.Render("/new: started a fresh conversation · previous conversation preserved, resumable via /session resume A|B"))
		return
	}
	var sb strings.Builder
	sb.WriteString("/new: started a fresh conversation · previous conversation preserved, resumable via /session resume A|B")
	sb.WriteString("\n")
	sb.WriteString("  " + orangeStyle.Render("Previous execution remains parked:") + "\n")
	if parked.RunID != "" {
		sb.WriteString("    " + permissionDescStyle.Render("run") + "  " + permissionTargetStyle.Render(parked.RunID) + "\n")
	}
	sb.WriteString("    " + permissionDescStyle.Render("state") + " " + permissionTargetStyle.Render(parked.State) + "\n")
	if len(parked.Targets) > 0 {
		sb.WriteString("    " + permissionDescStyle.Render("target") + " " + permissionTargetStyle.Render(strings.Join(parked.Targets, ", ")) + "\n")
	}
	sb.WriteString("\n")
	sb.WriteString("  " + mutedStyle.Render("A conversation boundary does not terminate an execution run.") + "\n")
	sb.WriteString("  " + mutedStyle.Render("Use  Enter  to resume it, or  Ctrl+C  to abort it.") + "\n")
	sb.WriteString("  " + mutedStyle.Render("A new build prompt will be refused at admission until it is resumed or aborted.") + "\n")
	m.push(roleSystem, sb.String())
}

// ParkedRun is the DURABLE record of one parked execution run.
//
// It is the contract between "a run parked at a human boundary" and "a process
// that starts again". Persistence must capture enough to reconstruct the run's
// SEMANTIC state — its identity, its scope, its candidate, its boundary — and
// nothing that would let recovery invent progress the run never made.
//
// The deliberate omissions matter as much as the fields: there is no mutation
// state (a parked run has applied nothing), no verification state (nothing was
// verified), and no objective verdict (nothing was proven). Recovery therefore
// CANNOT restore those, and a restore that claimed them would be fabricating a
// completion.
type ParkedRun struct {
	// RunID is the execution run's stable identity.
	RunID string
	// ObjectiveID is the objective's identity within that run.
	ObjectiveID string
	// Scope is the authoritative target set the run holds.
	Scope []string
	// CandidateID is the held mutation candidate's identity.
	CandidateID string
	// CandidateDigest is the content fingerprint the review was rendered from. It
	// is what makes a restored authorization safe: a candidate replaced while the
	// process was down cannot be applied under the pre-restart review.
	CandidateDigest string
	// Boundary names the human decision the run awaits.
	Boundary autonomy.HumanBoundaryAction
	// Resumable reports whether a resume decision exists.
	Resumable bool
	// ParkedAt is when the run reached its boundary.
	ParkedAt time.Time
}

// ToParkedRunEvidence projects the durable record onto the recovery input. It is
// the ONLY conversion, so what is persisted and what is restored cannot drift.
func (r ParkedRun) ToParkedRunEvidence() parkedRunEvidence {
	return parkedRunEvidence{
		RunID:       r.RunID,
		Targets:     append([]string(nil), r.Scope...),
		CandidateID: r.CandidateID,
		Digest:      r.CandidateDigest,
		Boundary:    r.Boundary,
		Resumable:   r.Resumable,
	}
}

// currentParkedRun reads the durable record for the run this model currently
// holds. It returns ok=false when nothing is parked, which is the whole truth a
// restart needs to know about a clean process.
func (m *model) currentParkedRun() (ParkedRun, bool) {
	if m == nil {
		return ParkedRun{}, false
	}
	b := m.autonomousBoundary
	if b == nil || m.autonomousDriver == nil {
		return ParkedRun{}, false
	}
	run := ParkedRun{
		RunID:           m.autonomousDriver.RunID(),
		Scope:           append([]string(nil), b.Targets...),
		CandidateID:     b.CandidateID,
		CandidateDigest: b.CandidateDigest,
		Boundary:        b.Action,
		Resumable:       b.Resumable,
		ParkedAt:        time.Now(),
	}
	if run.CandidateID == "" {
		run.CandidateID = b.PatchID
	}
	return run, true
}

// ExecutionTruth exposes the answers the operator must always be able to get, as
// typed values rather than rendered text, so a surface (a command, a modal, a
// test) can present them without re-deriving state.
type ExecutionTruth struct {
	ConversationID    string
	ConversationSlot  string
	ExecutionRunID    string
	ExecutionState    string
	CandidateID       string
	CandidateDigest   string
	Targets           []string
	Parked            bool
	PendingBoundary   string
	HumanBoundaryKind string
	MutationApplied   bool
	AppliedTargets    []string
	Remaining         []string
}

// Truth reads the current execution truth from AUTHORITATIVE state.
//
// Every field is a read of the runtime's own owners — the session manager for the
// conversation, the workflow state machine for the lifecycle, the driver for the
// run identity and boundary, and the executor for the candidate. None of it is
// inferred from a UI flag, a cached label or an optimistic assumption, which is
// what lets the TUI answer "has anything been mutated?" honestly.
func (m *model) Truth() ExecutionTruth {
	t := ExecutionTruth{}
	if m == nil {
		return t
	}
	if m.sessionManager != nil {
		t.ConversationSlot = m.sessionManager.Active().String()
	}
	if m.sess != nil {
		t.ConversationID = m.sess.SessionID
		t.ConversationSlot = orUnknown(t.ConversationSlot)
	}
	if m.workflowSM != nil {
		t.ExecutionState = m.workflowSM.State().String()
		t.Parked = m.workflowSM.Parked()
	}
	if m.autonomousDriver != nil {
		t.ExecutionRunID = m.autonomousDriver.RunID()
		if b := m.autonomousDriver.Boundary(); b != nil {
			t.Parked = true
			t.PendingBoundary = parkedBoundaryStateLabel(b)
			t.HumanBoundaryKind = b.Action.String()
			t.CandidateID = b.CandidateID
			t.CandidateDigest = b.CandidateDigest
			t.Targets = append([]string(nil), b.Targets...)
		}
	}
	// The mutation question is read from the ONE owner that knows which bytes
	// reached disk — the executor's post-write record. Hardcoding this to "no"
	// would have been the optimistic lie this surface exists to prevent, and
	// inferring it from "nothing is pending" would have been a guess: a rejected
	// or aborted candidate leaves nothing pending and writes nothing either.
	if m.executor != nil {
		t.AppliedTargets = m.executor.AppliedMutations()
		t.MutationApplied = len(t.AppliedTargets) > 0
	}
	t.Remaining = m.executionRemainingWork()
	return t
}

// executionRemainingWork names what the run still owes, from the lifecycle and the
// candidate — never from optimism. A held, unapplied candidate means the mutation
// is outstanding regardless of how many stages reported completion.
func (m *model) executionRemainingWork() []string {
	if m == nil {
		return nil
	}
	var out []string
	if m.workflowSM != nil && m.workflowSM.Parked() {
		out = append(out, "human authorization")
	}
	if m.executor != nil {
		if ids := m.executor.PendingPatchIDs(); len(ids) > 0 {
			out = append(out, "apply the authorized mutation")
		}
	}
	if m.workflowSM != nil && m.workflowSM.State().Executable() && m.autonomousParked() {
		out = append(out, "verify the mutation")
	}
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// parkedExecutionPresent reports whether a live execution run is parked at a
// human boundary, read from the RUNTIME (the driver's own boundary) rather than
// from a UI flag.
//
// It is the predicate that decides whether a conversation-scoped reset may release
// the pending-approval gate: clearing a conversation must not silently discard a
// live run's human decision surface.
func (m *model) parkedExecutionPresent() bool {
	if m == nil || m.autonomousDriver == nil {
		return false
	}
	return m.autonomousDriver.Boundary() != nil
}

// workflowParked is the invariant-1 predicate at the model level: a run holding a
// human boundary is never observable as idle.
func (m *model) workflowParked() bool {
	if m == nil {
		return false
	}
	if m.workflowSM != nil && m.workflowSM.Parked() {
		return true
	}
	_, ok := m.parkedExecutionSummary()
	return ok
}

// parkedRunEvidence is the DURABLE record a restart reads to reconstruct a
// parked execution. Every field is evidence the runtime itself recorded before
// shutdown; none of it is inferred from the absence of an event.
//
// Reconstruction is only ever permitted to a NON-TERMINAL parked position. A run
// whose evidence says it was executing when the process died is restored as
// executing, and one whose evidence says it parked is restored as parked —
// never as idle, completed or failed, because "we do not know" is not a
// terminal verdict and must not be invented as one.
type parkedRunEvidence struct {
	// RunID is the execution run's stable identity.
	RunID string
	// Targets is the authoritative target set the run holds.
	Targets []string
	// CandidateID is the held mutation candidate.
	CandidateID string
	// Digest is the candidate's content fingerprint the review was rendered from.
	Digest string
	// Boundary is the kind of human decision the run is waiting on.
	Boundary autonomy.HumanBoundaryAction
	// Resumable reports whether a resume decision exists.
	Resumable bool
}

// recoverParkedExecution reconstructs a parked execution after a process restart.
//
// The semantic state is restored EXACTLY: the run is awaiting human
// authorization, not idle and not finished. That distinction is the whole point —
// a restart that resolved a live run into `idle` would make the next mutation
// authorization impossible to authorize, which is precisely the reported failure
// reproduced across a process boundary.
func (m *model) recoverParkedExecution(ev parkedRunEvidence) {
	if m == nil {
		return
	}
	if ev.Boundary == "" {
		// No human boundary was recorded: the run was executing when the process
		// ended. Restore the executable position, not a terminal one. Nothing
		// proves the run finished, so it did not.
		if m.orch != nil {
			_ = m.orch.Force(phaseBuildForRecovery, workflow.TransitionContext{HasCapabilities: m.caps != nil})
		}
		return
	}
	if !m.parkExecutionAtBoundary() {
		m.logActivity("[execution] restart could not park the restored run at its human boundary")
	}
	m.markApprovalPending()
	if ev.Resumable {
		m.autonomousBoundary = &autonomy.HumanBoundary{
			PatchID:          ev.CandidateID,
			CandidateID:      ev.CandidateID,
			CandidateDigest:  ev.Digest,
			Targets:          append([]string(nil), ev.Targets...),
			CandidateTargets: append([]string(nil), ev.Targets...),
			Action:           ev.Boundary,
			Resumable:        ev.Resumable,
			Reason:           "execution restored from durable evidence — awaiting the same human decision",
		}
	}
	m.logActivity("[execution] restored parked run %s at %s (%d target(s), candidate %s held, nothing applied)",
		ev.RunID, parkedBoundaryStateLabel(m.autonomousBoundary), len(ev.Targets), ev.CandidateID)
}

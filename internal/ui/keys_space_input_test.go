package ui

// ── THE SPACE IS TEXT, EVEN WHILE A HUMAN BOUNDARY IS ON SCREEN ─────────────
//
// The reported symptom, reproduced in the real TUI: with a MUTATION REVIEW card
// open, typing `/session execution` produced `/sessionexecution` and the parser
// answered `unknown command "/sessionexecution"`.
//
// The cause was not the parser and not the boundary. `isPrintableRunes` gated
// the "a printable character typed into the focused input is ALWAYS text"
// rule on `msg.Type == tea.KeyRunes`, and Bubble Tea delivers a bare space as
// `tea.KeySpace`. So every printable character except the one that separates a
// command from its argument reached the input, and the space fell through to
// the card gate, which matches nothing for KeySpace and swallowed it.
//
// The consequence is not cosmetic: the rule exists so an operator can compose
// the NEXT command while a decision is pending. A gate that eats the space
// makes every argument-taking command untypable exactly when the operator is
// most likely to want to ask what is going on.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
	runtimeorch "github.com/PizenLabs/izen/internal/runtime/orchestrator"
)

// typeIntoInput types s one keystroke at a time, the way a human does, and
// returns the resulting buffer.
func typeIntoInput(m *model, s string) string {
	m.ti.Focus()
	m.ti.SetValue("")
	for _, r := range s {
		var msg tea.KeyMsg
		if r == ' ' {
			// Exactly what Bubble Tea emits for a bare space.
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		}
		_, _ = m.Update(msg)
	}
	return m.ti.Value()
}

// boundaryModel is an interactive model parked at a mutation-review boundary —
// the state in which the space was being eaten.
func boundaryModel(t *testing.T) *model {
	t.Helper()
	m := initializedChatModel(t)
	m.autonomousBoundary = &autonomy.HumanBoundary{
		Action:    autonomy.HumanBoundaryApproval,
		PatchID:   "patch-1",
		Resumable: true,
	}
	m.enterApprovalState()
	return m
}

// TestSpaceReachesTheInputWhileABoundaryCardIsUp is the direct regression for
// `/session execution` typing as `/sessionexecution`.
func TestSpaceReachesTheInputWhileABoundaryCardIsUp(t *testing.T) {
	m := boundaryModel(t)
	for _, cmd := range []string{"/session execution", "/new", "/help me", "/provider ollama"} {
		got := typeIntoInput(m, cmd)
		if got != cmd {
			t.Errorf("with a boundary card up, typing %q produced %q — the space did not reach the input", cmd, got)
		}
	}
}

// TestSpaceDoesNotResolveTheBoundary pins the half of the contract that matters
// most: making space typeable must not let it act as a decision. A space is not
// an approval and it is not a rejection.
func TestSpaceDoesNotResolveTheBoundary(t *testing.T) {
	m := boundaryModel(t)
	before := m.autonomousBoundary
	typeIntoInput(m, "/session execution")
	if m.autonomousBoundary == nil || m.autonomousBoundary.PatchID != before.PatchID {
		t.Fatal("a space resolved the pending mutation review")
	}
	if got := m.workflowSM.State(); got != workflow.StateAwaitingAuthorization {
		t.Errorf("a space moved the lifecycle to %q, want %q", got, workflow.StateAwaitingAuthorization)
	}
	// The held candidate is still held: a space authorized nothing.
	if m.executor != nil && len(m.executor.PendingPatchIDs()) > 0 {
		t.Error("a space applied or dropped the held candidate")
	}
}

// TestScrollShortcutsStillWorkWhenTheInputIsNotFocused is the other direction:
// the shortcuts exist for inspecting the log behind a card, and hoisting the
// text precedence must not have taken them away. Text wins while typing;
// scrolling is still available once focus is elsewhere.
func TestScrollShortcutsStillWorkWhenTheInputIsNotFocused(t *testing.T) {
	m := boundaryModel(t)
	m.ti.Blur()
	m.ti.SetValue("")

	// Space is the snap-to-tail shortcut: it must NOT have become a space
	// character in the buffer.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if got := m.ti.Value(); got != "" {
		t.Errorf("with the input unfocused, space typed %q instead of scrolling", got)
	}
	if m.userScrollLocked {
		t.Error("space did not release the scroll lock")
	}
}

// TestTypingJkWhileACardIsUp pins the sibling half of the same precedence
// inversion. `j` and `k` are the viewport scroll keys, and a KeyMsg cannot
// distinguish a typed `j` from a pressed one — so with a card up they were
// swallowed too, which quietly broke any command containing those letters.
func TestTypingJkWhileACardIsUp(t *testing.T) {
	m := boundaryModel(t)
	for _, cmd := range []string{"/ask j", "/build k", "/session jack k"} {
		if got := typeIntoInput(m, cmd); got != cmd {
			t.Errorf("with a card up, typing %q produced %q — j/k were eaten as scroll keys", cmd, got)
		}
	}
}

// parkedApprovalModel is a model parked at a mutation-review boundary with a
// REAL held candidate, so a decision would have something to apply.
func parkedApprovalModel(t *testing.T) (*model, *e2eHarness, string, string) {
	t.Helper()
	root := e2eWorkspace(t)
	h, patchID := newE2EModel(t, root)
	m := h.model
	// The conversation authority and the intent gateway are wired the way
	// production wires them, so a typed `/new` and a typed build prompt travel
	// the real routing path instead of stopping at a wiring check.
	sm := newE2ESessionManager(t)
	m.sessionManager = sm
	m.sess = sm.Session()
	m.gateway = execution.NewIntentGateway(root)
	e2ePark(t, m, patchID)
	if !m.autonomousParked() {
		t.Fatal("fixture did not reach the parked boundary")
	}
	return m, h, patchID, root
}

// TestEnterDoesNotAuthorizeATypedCommand is the authorization-integrity guard.
//
// The reproduced hazard: with a mutation review open, the operator typed `/new`
// and pressed Enter. The Enter was claimed by the card's "apply mutation"
// alias, the workspace was written, and `/new` was never submitted.
//
// A decision must be something the operator MEANT. Enter and Esc keep their
// meaning as the card's primary/secondary action only while nothing is typed;
// Alt+A and Alt+R remain unconditional so the decision is always reachable.
func TestEnterDoesNotAuthorizeATypedCommand(t *testing.T) {
	m, _, patchID, root := parkedApprovalModel(t)
	target := filepath.Join(root, e2eTarget)
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("precondition: the CREATE target must not exist yet (%v)", err)
	}

	// The operator types a command they intend to RUN, and presses Enter.
	typeIntoInput(m, "/new")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// The workspace is untouched and the candidate is still held: no
	// authorization, no mutation.
	if !m.executor.CandidateHeld(patchID) {
		t.Error("Enter while a command was typed applied the held mutation")
	}
	if _, err := os.Stat(target); err == nil {
		t.Errorf("a typed command + Enter created %s on disk", e2eTarget)
	}
	if m.autonomousBoundary == nil {
		t.Fatal("Enter while a command was typed resolved the boundary")
	}
	// And the command ran, rather than being stranded in the buffer by a
	// keyboard the boundary owned.
	if got := strings.TrimSpace(m.ti.Value()); got != "" {
		t.Errorf("the typed command was not submitted; buffer = %q", got)
	}
	if text := viewportOf(m); !strings.Contains(text, "started a fresh conversation") {
		t.Errorf("`/new` did not run:\n%s", text)
	}
	// The parked execution survives the conversation boundary: a conversation
	// boundary is not an execution boundary.
	if !m.autonomousParked() {
		t.Error("`/new` destroyed the parked execution run")
	}
	if !m.workflowSM.Parked() {
		t.Errorf("`/new` released the parked lifecycle: state=%q", m.workflowSM.State())
	}
}

// TestNewBuildPromptWhileParkedIsRefusedAtAdmission is the admission requirement
// end to end, driven through the keyboard: a new build prompt typed while a run
// is parked must be REFUSED before any intent parsing, planning or provider
// call, with the parked run left intact and resumable.
func TestNewBuildPromptWhileParkedIsRefusedAtAdmission(t *testing.T) {
	m, h, patchID, root := parkedApprovalModel(t)
	callsBefore := h.providerCalls()

	typeIntoInput(m, "$prompt make the heading blue")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	text := viewportOf(m)
	if !strings.Contains(text, "EXECUTION BLOCKED") {
		t.Fatalf("a new build prompt was not refused at admission:\n%s", text)
	}
	// Refused BEFORE any work: no provider call, no planning, no mutation.
	if after := h.providerCalls(); after != callsBefore {
		t.Errorf("the refused prompt still called the provider (%d -> %d)", callsBefore, after)
	}
	if !m.executor.CandidateHeld(patchID) {
		t.Error("the refused prompt disturbed the held candidate")
	}
	if _, err := os.Stat(filepath.Join(root, e2eTarget)); err == nil {
		t.Error("the refused prompt wrote to the workspace")
	}
	// And the operator is told what their options are.
	for _, want := range []string{"Resume", "Abort", "No new execution started"} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal does not offer %q:\n%s", want, text)
		}
	}
}

// TestAltSpaceStaysAKeybindingGuard is the counterpart: Alt+A remains
// unconditional, so the decision is never more than one keystroke away.
func TestAltAAppliesEvenWithACommandTyped(t *testing.T) {
	m, _, _, _ := parkedApprovalModel(t)
	typeIntoInput(m, "/new")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}, Alt: true})
	if m.autonomousBoundary != nil {
		t.Error("Alt+A no longer authorizes while a command is typed; the decision became unreachable")
	}
	if text := viewportOf(m); !strings.Contains(text, "Authorization accepted") {
		t.Errorf("Alt+A did not take the authorization path:\n%s", text)
	}
}

// TestBareEnterStillAppliesTheReviewedCandidate pins the other half: the
// documented `[Enter] approve` path must keep working for the case it was
// designed for — an operator who has typed nothing and just wants to accept.
func TestBareEnterStillAppliesTheReviewedCandidate(t *testing.T) {
	m, _, _, _ := parkedApprovalModel(t)
	m.ti.SetValue("")
	m.ti.Focus()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.autonomousBoundary != nil {
		t.Error("a bare Enter no longer applies the reviewed candidate")
	}
	if text := viewportOf(m); !strings.Contains(text, "Authorization accepted") {
		t.Errorf("a bare Enter did not take the authorization path:\n%s", text)
	}
}

// TestAltSpaceStaysAKeybinding guards the other direction: an Alt-modified key
// is a keybinding mechanism, never text. Alt+A and Alt+R are the review's own
// decisions, and this fix must not have made Alt+<space> type a character.
func TestAltSpaceStaysAKeybinding(t *testing.T) {
	if isPrintableRunes(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}, Alt: true}) {
		t.Error("alt+space was classified as text; alt-modified keys are keybindings")
	}
	if isPrintableRunes(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}, Alt: true}) {
		t.Error("alt+a was classified as text")
	}
	// And the ordinary space IS text.
	if !isPrintableRunes(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}) {
		t.Error("a bare space is not classified as text")
	}
	// A space key carrying a non-printable rune is not text either.
	if isPrintableRunes(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{0x07}}) {
		t.Error("a space key carrying a control rune was classified as text")
	}
}

// TestCasualChatDoesNotDestroyAParkedRun pins the last door into the reported
// defect.
//
// The lifecycle reset that produced `expected Building or Repairing … got idle`
// did not only happen on the authorization path. A casual message ("hi") at a
// mutation review ran the casual auto-unwind, which reset the workflow to idle
// and released the boundary — while the run was still holding a candidate no
// human had ruled on. The run was reported as idle because it HAD BEEN made
// idle, by a code path nobody was watching.
//
// Conversation is not a decision. A casual message must leave the parked run
// exactly as it found it, and must say so.
func TestCasualChatDoesNotDestroyAParkedRun(t *testing.T) {
	m, _, patchID, _ := parkedApprovalModel(t)
	before := m.workflowSM.State()
	boundary := m.autonomousBoundary

	if !m.handleCasualAutoUnwind("hi") {
		t.Fatal("the casual prompt was not consumed; this test is not on the lane it claims to cover")
	}

	if got := m.workflowSM.State(); got != before {
		t.Errorf("a casual message moved the lifecycle %q -> %q; a parked run must not be unwound", before, got)
	}
	if !m.workflowSM.Parked() {
		t.Errorf("a casual message released the parked run: %q", m.workflowSM.State())
	}
	if m.autonomousBoundary == nil || m.autonomousBoundary.PatchID != boundary.PatchID {
		t.Error("a casual message dropped the human boundary")
	}
	if !m.executor.CandidateHeld(patchID) {
		t.Error("a casual message dropped the held candidate")
	}
	// And the operator is told the run is still waiting, rather than being left
	// to assume their message resolved it.
	if text := viewportOf(m); !strings.Contains(text, "parked at a human boundary") {
		t.Errorf("the casual reply does not state that the run is still parked:\n%s", text)
	}
}

// TestCasualChatStillUnwindsAStuckPhase is the other half: the unwind exists to
// rescue a session trapped in a LIVE execution phase, and it must keep doing
// that. Only a run parked at a human boundary is exempt.
func TestCasualChatStillUnwindsAStuckPhase(t *testing.T) {
	// A live execution phase — not a parked one.
	m := initializedChatModel(t)
	sm := workflow.NewWorkflowStateMachine()
	m.workflowSM = sm
	m.orch = runtimeorch.New(sm, nil)
	// PLANNING is a live phase with no human decision attached, so the casual
	// unwind has something to rescue. Build is not used here precisely because
	// reaching it legitimately requires an authorized plan.
	if err := m.orch.Transition(runtimeorch.PhasePlan, workflow.TransitionContext{}); err != nil {
		t.Fatalf("driving to the plan phase: %v", err)
	}
	if m.workflowSM.Parked() {
		t.Fatalf("precondition: expected a live phase, got the parked %q", m.workflowSM.State())
	}
	if !m.handleCasualAutoUnwind("hi") {
		t.Fatal("the casual prompt was not consumed")
	}
	if got := m.workflowSM.State(); got == workflow.StatePlanning || got == workflow.StateAwaitingAuthorization {
		t.Errorf("a stuck phase was not unwound: %q", got)
	}
}

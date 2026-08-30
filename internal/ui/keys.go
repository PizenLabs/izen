package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/presentation"
	proposaltui "github.com/PizenLabs/izen/internal/ui/tui"
)

// isPrintableRunes reports whether a key message is a plain, unmodified
// printable character (or character run). This is the canonical test for
// "text the user is typing": Alt-modified keys and control keys are
// keybinding mechanisms, never text.
func isPrintableRunes(msg tea.KeyMsg) bool {
	if msg.Type != tea.KeyRunes || len(msg.Runes) == 0 {
		return false
	}
	if msg.Alt {
		return false
	}
	for _, r := range msg.Runes {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// forwardToInput routes a printable keystroke into the focused text input,
// returning the textinput command. It is the single implementation of the
// "active text input" keyboard precedence (priority 1): a printable character
// typed into the focused input is ALWAYS text — it can never be hijacked by a
// card, chip, or keybinding shortcut.
func (m *model) forwardToInput(msg tea.KeyMsg) tea.Cmd {
	var tiCmd tea.Cmd
	m.ti, tiCmd = m.ti.Update(msg)
	m.syncInputFromTI()
	m.updateSuggestions()
	return tiCmd
}

// cycleExecVisibility advances the active human presentation layer of a gated
// execution: NORMAL → EXPANDED → DEBUG → NORMAL. It is the Ctrl+O execution
// expansion contract. Returns false when no gated execution is active so the
// key falls through to the thought-block / log-store behavior.
func (m *model) cycleExecVisibility() bool {
	if m.execView == nil || !m.executionResolving || !m.execView.Active() {
		return false
	}
	switch m.execVisibility {
	case presentation.VisibilityNormal:
		m.execVisibility = presentation.VisibilityExpanded
	case presentation.VisibilityExpanded:
		m.execVisibility = presentation.VisibilityDebug
	default:
		m.execVisibility = presentation.VisibilityNormal
	}
	m.refreshViewportContent()
	if m.Ready && !m.userIsScrollingUp {
		m.followTail()
	}
	return true
}

// toggleThoughtBlock expands/collapses the active reasoning block OR the live
// shell-output block. Priority:
//
//  1. Shell execution (Ctrl+O during a running/completed command): expands or
//     collapses the accumulated stdout/stderr of the exec entry in the
//     activity tree — the "Bash Execution Spinner with Ctrl+O Expansion"
//     contract. Takes precedence so a running shell is always inspectable.
//  2. The event-driven ThinkingBuffer, then the legacy ThinkingPanel.
//
// The viewport is repainted synchronously so the inline box toggles
// immediately on the keypress — even while reasoning tokens or shell output
// are still streaming in (async inspection). Returns false when no thought or
// shell block exists.
func (m *model) toggleThoughtBlock() bool {
	if m.activityTree != nil && m.activityTree.HasCommandExec() {
		m.activityTree.ToggleExpanded()
		m.refreshViewportContent()
		if m.Ready && !m.userIsScrollingUp {
			m.followTail()
		}
		return true
	}
	switch {
	case m.thinkingBuffer != nil && m.thinkingBuffer.Len() > 0:
		m.thinkingBuffer.Toggle()
	case m.thinkingPanel != nil:
		m.thinkingPanel.Toggle()
	case m.traceBuffer.Len() > 0:
		// "Human-Centered / Reversible": models without a formal reasoning
		// channel (e.g. Gemma) never feed the ThinkingBuffer, so Ctrl+O used to
		// do nothing. The raw output trace is captured in traceBuffer instead —
		// toggle its expanded viewport. The window anchor is released so the
		// next render re-anchors to the trace tail on expansion.
		m.traceExpanded = !m.traceExpanded
		m.traceWindowAnchored = false
	default:
		return false
	}
	m.refreshViewportContent()
	// While the output-trace viewport is expanded during an active stream,
	// preserve the user's scroll position: new chunks must never yank the
	// viewport to the bottom (the Ctrl+O flicker).
	if m.Ready && !m.userIsScrollingUp && !m.traceExpanded {
		m.followTail()
	}
	return true
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ── GLOBAL: Alt+O toggles reasoning block visibility ────────────
	// The unified ThinkingBuffer (event-driven thought block) takes priority;
	// the legacy ThinkingPanel is only toggled when no event-driven block is
	// active (e.g. legacy build fast-track reasoning).
	if msg.String() == "alt+o" {
		m.toggleThoughtBlock()
		return m, nil
	}

	// ── GLOBAL: Ctrl+O toggles the active thought block ──────────────
	// Expands/collapses the reasoning block for the currently active message
	// (ThinkingBuffer). When no thought block is active, falls back to cycling
	// through the foldable build-log entries (legacy behavior).
	//
	// PHASE 6: while a gated execution is active, Ctrl+O cycles the human
	// presentation layer (NORMAL → EXPANDED → DEBUG → NORMAL) so the user can
	// reveal runtime details and the full event stream on demand.
	if msg.Type == tea.KeyCtrlO {
		if m.cycleExecVisibility() {
			return m, nil
		}
		if m.toggleThoughtBlock() {
			return m, nil
		}
		if m.logStore != nil {
			newID := m.logStore.ToggleCycle()
			if newID >= 0 {
				m.refreshViewportContent()
				if m.Ready && !m.userIsScrollingUp {
					m.followTail()
				}
			}
		}
		return m, nil
	}

	// ── AUTONOMY TARGET SELECTOR (§8) ─────────────────────────────────
	// An ambiguous mutation target presents a small candidate selector: ↑/↓
	// navigate, Enter selects and continues execution, Esc cancels. It takes
	// precedence over every other key handler while a selector is outstanding.
	if len(m.pendingAutonomyTargets) > 0 {
		switch {
		case msg.Type == tea.KeyUp || msg.String() == "k":
			m.navigateAutonomyTarget(-1)
			return m, nil
		case msg.Type == tea.KeyDown || msg.String() == "j":
			m.navigateAutonomyTarget(1)
			return m, nil
		case msg.Type == tea.KeyEnter:
			return m, m.activateAutonomyTarget()
		case msg.Type == tea.KeyEscape || msg.String() == "alt+x":
			return m, m.cancelAutonomyTargetSelector()
		default:
			if m.ti.Focused() && isPrintableRunes(msg) {
				return m, m.forwardToInput(msg)
			}
			return m, nil
		}
	}

	// ── AUTONOMY PROPOSAL (ask_user decision surface) ──────────────────
	// The proposal is the ONLY authorization gate: ↑/↓ navigate the action
	// list, Enter activates the highlighted action (Execute / Inspect /
	// Cancel), Esc cancels. It takes precedence over every other key handler
	// while a proposal is outstanding. No /grant command exists.
	if m.pendingAutonomyProposal != nil {
		switch {
		case msg.Type == tea.KeyUp || msg.String() == "k":
			m.navigateAutonomyProposal(-1)
			return m, nil
		case msg.Type == tea.KeyDown || msg.String() == "j":
			m.navigateAutonomyProposal(1)
			return m, nil
		case msg.Type == tea.KeyEnter:
			return m, m.activateAutonomyProposal()
		case msg.Type == tea.KeyEscape || msg.String() == "alt+x":
			return m, m.cancelAutonomyProposal()
		case msg.String() == "i":
			m.toggleAutonomyProposalInspect()
			return m, nil
		default:
			// Printable text still composes the next command while the
			// proposal renders (the input line stays focused).
			if m.ti.Focused() && isPrintableRunes(msg) {
				return m, m.forwardToInput(msg)
			}
			return m, nil
		}
	}

	// ── THOUGHT-BOX SCROLL ────────────────────────────────────────────
	// While the expanded reasoning box overflows maxLines, j/k, arrows, and
	// PgUp/PgDn scroll WITHIN the box (the proposal-diff convention) so the
	// user can read earlier reasoning without losing place. Space jumps back
	// to the tail and resumes auto-scroll. When the box fits or is collapsed
	// these keys fall through to the main viewport / input untouched — and the
	// intercepted keys never reach the text input, so no raw ANSI/regex escape
	// sequence can leak into the prompt bar.
	if m.thinkingBuffer != nil && m.thinkingBuffer.Expanded() && m.thinkingBuffer.HasOverflow() &&
		m.state != StateAwaitingApproval {
		switch {
		case msg.String() == "k" || msg.Type == tea.KeyUp || msg.Type == tea.KeyCtrlU:
			m.thinkingBuffer.ScrollUp(3)
			m.refreshViewportContent()
			return m, nil
		case msg.String() == "j" || msg.Type == tea.KeyDown || msg.Type == tea.KeyCtrlD:
			m.thinkingBuffer.ScrollDown(3)
			m.refreshViewportContent()
			return m, nil
		case msg.Type == tea.KeyPgUp || msg.Type == tea.KeyHome:
			m.thinkingBuffer.ScrollUp(6)
			m.refreshViewportContent()
			return m, nil
		case msg.Type == tea.KeyPgDown || msg.Type == tea.KeyEnd:
			m.thinkingBuffer.ScrollDown(6)
			m.refreshViewportContent()
			return m, nil
		case msg.Type == tea.KeySpace:
			m.thinkingBuffer.ResetScroll()
			m.refreshViewportContent()
			if m.Ready && !m.userIsScrollingUp {
				m.followTail()
			}
			return m, nil
		}
	}

	// ── GLOBAL: Alt+F / Option+F / Meta+F — Handoff from /ask to /investigate ──
	// Checks the latest valid /ask Context Ledger (ask_handoff packet), and if
	// present, transitions to /investigate with the ledger injected as context.
	// If no valid /ask Context Ledger exists, rejects with a clear TUI notice.
	if msg.String() == "alt+f" {
		if m.state == StateProcessing || m.state == StateAwaitingApproval {
			return m, nil
		}
		if m.streaming || m.agentRunning || m.reviewRunning || m.pipelineRunning {
			return m, nil
		}

		// Check for a valid /ask Context Ledger
		hasAskHandoff := false
		handoffContent := ""
		if m.sess != nil && m.sess.ContextLedger != nil {
			// Check for an "ask_handoff" packet in the ledger
			for _, p := range m.sess.ContextLedger.Packets {
				if p.Kind == "ask_handoff" {
					hasAskHandoff = true
					handoffContent = p.Payload
					break
				}
			}
			// Fallback: check Diagnostics if no ask_handoff packet found
			if !hasAskHandoff && m.sess.ContextLedger.Diagnostics != "" {
				hasAskHandoff = true
				handoffContent = m.sess.ContextLedger.Diagnostics
			}
		}
		// Also check the transient handoffLedgerContent
		if !hasAskHandoff && m.handoffLedgerContent != "" {
			hasAskHandoff = true
			handoffContent = m.handoffLedgerContent
		}

		if !hasAskHandoff || handoffContent == "" {
			m.push(roleError, "No active Context Ledger from /ask. Run $prompt <query> in any mode first to generate a Forensic Context Ledger.")
			m.refreshViewportContent()
			m.followTail()
			return m, nil
		}

		// Create Handoff Context from the ask handoff payload
		m.handoffCtx.LastFailurePayload = handoffContent
		m.handoffCtx.ProposedFix = handoffContent
		m.handoffLedgerContent = handoffContent

		// ── INTENT-BASED /ask HANDOFF BYPASS ───────────────────────────
		// FRONTEND_UI and code-generation/rewrite prompts add ZERO diagnostic
		// value to /investigate: the engine short-circuits them in 0s with an
		// "Inconclusive" result while injecting forensic overhead into the TUI
		// and context ledger. Bypass the engine and transition directly to
		// /plan (UI/layout tasks) or /build (code mutation).
		intent := investigate.ClassifyIntent(handoffContent)
		if intent.IsFrontendUI() {
			// Preserve the raw request alongside the routing marker so the
			// microkernel pipeline can plan from the actual prompt.
			m.handoffLedgerContent = "frontend ui intent detected — hand off to plan\n" + handoffContent
			m.handoffCtx.ProposedFix = handoffContent
			m.persistUserIntentPacket(handoffContent)
			m.modeChangeAuthorized = true
			m.currentResult = nil
			return m, m.setMode(modes.ModePlan)
		}
		if hasMutationIntent(handoffContent) && hasExecutableBuildTarget(handoffContent, m) {
			m.handoffCtx.PendingTodos = synthesizeBuildTodosFromMutation(handoffContent)
			m.modeChangeAuthorized = true
			m.currentResult = nil
			return m, m.setMode(modes.ModeBuild)
		}

		// Transition mode to /investigate (clean transition)
		m.modeChangeAuthorized = true
		m.currentResult = nil
		cmd := m.setMode(modes.ModeInvestigate)
		return m, cmd
	}

	// ── StateProcessing: block input but allow viewport navigation ──────
	if m.state == StateProcessing {
		if m.Ready {
			scrollStep := func() int {
				step := 1
				return step
			}
			pageStep := func() int {
				step := m.Viewport.Height / 2
				if step < 1 {
					step = 1
				}
				return step
			}
			switch {
			case msg.Type == tea.KeyUp || msg.String() == "k" || msg.Type == tea.KeyCtrlU:
				if msg.Type == tea.KeyCtrlU {
					m.scrollBy(-pageStep())
				} else {
					m.scrollBy(-scrollStep())
				}
				return m, nil
			case msg.Type == tea.KeyDown || msg.String() == "j" || msg.Type == tea.KeyCtrlD:
				if msg.Type == tea.KeyCtrlD {
					m.scrollBy(pageStep())
				} else {
					m.scrollBy(scrollStep())
				}
				return m, nil
			case msg.Type == tea.KeyPgUp || msg.Type == tea.KeyHome:
				m.scrollBy(-m.Viewport.Height / 2)
				if m.Viewport.Height/2 == 0 {
					m.scrollBy(-1)
				}
				return m, nil
			case msg.Type == tea.KeyPgDown || msg.Type == tea.KeyEnd:
				m.scrollBy(m.Viewport.Height / 2)
				if m.Viewport.Height/2 == 0 {
					m.scrollBy(1)
				}
				return m, nil
			case msg.Type == tea.KeySpace:
				m.setScrollLocked(false)
				m.traceWindowAnchored = false
				m.followTail()
				return m, nil
			}
		}
		return m, nil
	}

	// ── Viewport navigation pass-through in locked states ───────────────
	// j/k and ctrl+u/ctrl+d forward to the main workspace viewport so
	// the user can fluidly inspect the log/context history behind the
	// approval/proposal modal. Arrow keys are reserved for proposal
	// diff scrolling (see block below). Tracks scroll-up to prevent
	// auto-scroll jank when the user inspects log history.
	if m.state == StateAwaitingApproval {
		if m.Ready {
			switch {
			case msg.String() == "k" || msg.Type == tea.KeyCtrlU:
				step := 1
				if msg.Type == tea.KeyCtrlU {
					step = m.Viewport.Height / 2
					if step < 1 {
						step = 1
					}
				}
				m.scrollBy(-step)
				return m, nil
			case msg.String() == "j" || msg.Type == tea.KeyCtrlD:
				step := 1
				if msg.Type == tea.KeyCtrlD {
					step = m.Viewport.Height / 2
					if step < 1 {
						step = 1
					}
				}
				m.scrollBy(step)
				return m, nil
			case msg.Type == tea.KeySpace:
				m.setScrollLocked(false)
				m.traceWindowAnchored = false
				m.followTail()
				return m, nil
			}
		}
	}

	// ── Proposal diff scroll (inner view) ─────────────
	// Arrow keys and page keys scroll the proposal diff content
	// inside the proposal dock when an expanded diff is available.
	// When no expanded diff is active, arrow keys fall through to
	// the viewport for collapsed proposals and build approval prompts.
	// j/k are reserved for the main workspace viewport.
	if m.state == StateAwaitingApproval && m.Ready {
		if len(m.pendingProposals) > 0 {
			p := m.pendingProposals[0]
			if p.Expanded && p.Diff != "" {
				diffLines := strings.Split(p.Diff, "\n")
				totalDiff := 0
				for _, l := range diffLines {
					if l == "" {
						continue
					}
					if strings.HasPrefix(l, "---") || strings.HasPrefix(l, "+++") {
						continue
					}
					totalDiff++
				}
				maxOffset := totalDiff - maxProposalDiffHeight
				if maxOffset < 0 {
					maxOffset = 0
				}

				switch msg.Type {
				case tea.KeyUp:
					if m.proposalDiffOffset > 0 {
						m.proposalDiffOffset--
					}
					return m, nil
				case tea.KeyDown:
					if m.proposalDiffOffset < maxOffset {
						m.proposalDiffOffset++
					}
					return m, nil
				case tea.KeyPgUp:
					m.proposalDiffOffset = max(0, m.proposalDiffOffset-maxProposalDiffHeight)
					return m, nil
				case tea.KeyPgDown:
					m.proposalDiffOffset = min(maxOffset, m.proposalDiffOffset+maxProposalDiffHeight)
					return m, nil
				}
			}
		}
	}

	// ── Awaiting approval / hotfix ambiguity ─────────────────────────
	// Both states hard-intercept the keyboard: the proposal/approval gate and
	// the actionable ambiguity card (Clarify / Inspect candidates / Cancel).
	if m.state == StateAwaitingApproval || m.state == StateHotfixAmbiguous {
		// ── PRIORITY 1: ACTIVE TEXT INPUT ───────────────────────────
		// A printable character typed into the focused input is ALWAYS text.
		// Card/chip actions (Clarify, Inspect, Select, Accept, Reject) use
		// explicit keybinding mechanisms (alt+…, Enter, Esc) so they can never
		// hijack normal typing — the developer can compose the next command
		// while an approval or ambiguity card is on screen.
		if m.ti.Focused() && isPrintableRunes(msg) {
			return m, m.forwardToInput(msg)
		}

		// ── PRODUCTION AUTONOMOUS DRIVER BOUNDARY (Phase 6) ─────────
		// A parked autonomous run holds one human decision: approve the held
		// mutation (Alt+A / Enter), reject it (Alt+R / Esc), pick a clarify
		// candidate (↑/↓ + Enter), or abort the parked run (Ctrl+C). The
		// driver owns the loop; the UI only decides and resumes.
		if m.autonomousParked() {
			b := m.autonomousBoundary
			switch {
			case b.Action == autonomy.HumanBoundaryProposal && msg.Type == tea.KeyUp:
				if m.proposalTUI != nil {
					m.proposalTUI.Navigate(-1)
				}
				return m, nil
			case b.Action == autonomy.HumanBoundaryProposal && msg.Type == tea.KeyDown:
				if m.proposalTUI != nil {
					m.proposalTUI.Navigate(1)
				}
				return m, nil
			case b.Action == autonomy.HumanBoundaryProposal && msg.Type == tea.KeyEnter:
				intent := proposaltui.ProposalCancel
				if m.proposalTUI != nil {
					intent = m.proposalTUI.Select()
				}
				m.push(roleSystem, infoStyle.Render("  "+Icon.Success+" Recovery selected — "+string(intent)+"..."))
				m.refreshViewportContent()
				m.followTail()
				return m, m.resumeAutonomousProposal(string(intent))
			case b.Action == autonomy.HumanBoundaryProposal && msg.Type == tea.KeyEscape:
				m.push(roleSystem, infoStyle.Render("  "+Icon.Error+" Cancelled — autonomous run aborted."))
				m.refreshViewportContent()
				m.followTail()
				return m, m.resumeAutonomousProposal("cancel")
			case b.Action == autonomy.HumanBoundaryApproval &&
				(msg.String() == "alt+a" || msg.Type == tea.KeyEnter):
				m.push(roleSystem, infoStyle.Render("  "+Icon.Success+" Approved — runtime applying patch..."))
				m.refreshViewportContent()
				m.followTail()
				return m, m.resumeAutonomousApprove()
			case b.Action == autonomy.HumanBoundaryDecomposition && msg.Type == tea.KeyEnter:
				// Authorize the WHOLE staged DAG: every sub-task executes as
				// one atomic transaction under the plan's own preflight scopes.
				m.push(roleSystem, infoStyle.Render("  "+Icon.Success+" Plan authorized — running the staged DAG..."))
				m.refreshViewportContent()
				m.followTail()
				return m, m.resumeAutonomousProposalApprove()
			case b.Action == autonomy.HumanBoundaryClarify && msg.Type == tea.KeyUp:
				m.navigateAutonomousBoundary(-1)
				return m, nil
			case b.Action == autonomy.HumanBoundaryClarify && msg.Type == tea.KeyDown:
				m.navigateAutonomousBoundary(1)
				return m, nil
			case b.Action == autonomy.HumanBoundaryClarify && msg.Type == tea.KeyEnter:
				m.push(roleSystem, infoStyle.Render("  target selected — resuming the run..."))
				m.refreshViewportContent()
				m.followTail()
				return m, m.resumeAutonomousClarify()
			case b.Action == autonomy.HumanBoundaryApproval &&
				(msg.String() == "alt+r" || msg.Type == tea.KeyEscape):
				m.push(roleSystem, infoStyle.Render("  "+Icon.Error+" Rejected — runtime finalizing. No files were modified."))
				m.refreshViewportContent()
				m.followTail()
				return m, m.resumeAutonomousReject("rejected by operator")
			case b.Action == autonomy.HumanBoundaryInform &&
				(msg.String() == "alt+r" || msg.Type == tea.KeyEscape):
				m.push(roleSystem, infoStyle.Render("  "+Icon.Error+" Dismissed — autonomous run aborted."))
				m.refreshViewportContent()
				m.followTail()
				return m, m.abortAutonomousRun("dismissed by operator")
			case b.Action == autonomy.HumanBoundaryClarify && msg.Type == tea.KeyEscape:
				m.push(roleSystem, infoStyle.Render("  "+Icon.Error+" Cancelled — autonomous run aborted."))
				m.refreshViewportContent()
				m.followTail()
				return m, m.abortAutonomousRun("cancelled by operator")
			case b.Action == autonomy.HumanBoundaryDecomposition && msg.Type == tea.KeyEscape:
				// Cancel the whole staged plan: nothing executed, nothing mutated.
				m.push(roleSystem, infoStyle.Render("  "+Icon.Error+" Cancelled — decomposition plan discarded. No files were modified."))
				m.refreshViewportContent()
				m.followTail()
				return m, m.resumeAutonomousProposalReject("cancelled by operator")
			}
			return m, nil
		}

		// ── Hybrid Intent Gateway mode-selection prompt ─────────────
		// The router classified the prompt with confidence below the policy
		// threshold. Digits select a mode directly, ←/→ cycle the highlight,
		// Enter confirms the highlighted mode, Esc falls back to /ask.
		// ── Effort Selector (←/→) ────────────────────────────────────
		if msg.Type == tea.KeyLeft {
			if m.currentEffort > EffortAuto {
				m.currentEffort--
			}
			m.recalcViewportHeight()
			m.refreshViewportContent()
			m.followTail()
			return m, nil
		}
		if msg.Type == tea.KeyRight {
			if m.currentEffort < EffortHigh {
				m.currentEffort++
			}
			m.recalcViewportHeight()
			m.refreshViewportContent()
			m.followTail()
			return m, nil
		}

		// ── Arrow-key fallback: scroll the main viewport when
		// there is no expanded proposal diff to inspect. This
		// keeps navigation functional for collapsed proposals and
		// build approval prompts where the diff scroll block
		// above does not intercept. ──────────────────────────
		if msg.Type == tea.KeyUp || msg.Type == tea.KeyDown ||
			msg.Type == tea.KeyPgUp || msg.Type == tea.KeyPgDown {
			var vpCmd tea.Cmd
			m.Viewport, vpCmd = m.Viewport.Update(msg)
			return m, vpCmd
		}

		// ── $hot AMBIGUOUS RESOLUTION CARD ─────────────────────────
		// The target-confidence boundary paused the request. The developer must
		// Clarify target ([⌥C]), Inspect candidates ([⌥I], read-only), or
		// Cancel ([⌥X]/Esc). NO Accept/Reject is rendered — there is no patch.
		// Candidate selection ([1-9] in inspect mode) is an explicit human act
		// that makes the target explicit; it never happens automatically.
		//
		// LIVENESS: the card is a terminal outcome of the hotfix operation,
		// ── $hot HOTFIX APPROVAL GATE ─────────────────────────────
		// The hotfix patch was generated but NOT applied. The developer must
		// explicitly authorize (Alt+A / Enter) or reject (Alt+R / Esc).
		// On approval the patch is written to disk and the stashed plan
		// restored; on rejection the hotfix aborts cleanly to PAUSED with
		// zero disk mutation.
		if m.pendingHotfixTask != nil && m.pendingHotfixPatch != nil {
			switch {
			case msg.String() == "alt+a" || msg.Type == tea.KeyEnter:
				patch := m.pendingHotfixPatch
				executorPatchID := m.executorPendingPatchID
				m.pendingHotfixTask = nil
				m.pendingHotfixPatch = nil
				m.pendingProposals = nil
				m.resolveApprovalState()
				m.ti.Focus()
				m.recalcViewportHeight()
				m.refreshViewportContent()
				m.followTail()

				// ── EXECUTOR-OWNED APPROVAL (authority migration) ──
				// When the proposal came from the RuntimeExecutor, the apply,
				// verification and commit happen inside the runtime via
				// Approve. The runtime emits the mutation lifecycle events; the
				// UI renders them and the returned executionResultMsg. The UI
				// never calls PatchManager.Apply here.
				if executorPatchID != "" {
					m.executorPendingPatchID = ""
					m.executorPendingTargets = nil
					m.push(roleSystem, infoStyle.Render(
						fmt.Sprintf("  "+Icon.Success+" Approved — runtime applying patch to %s...", patch.File)))
					return m, tea.Batch(
						func() tea.Msg { return agentStartMsg{label: "runtime hotfix apply"} },
						m.runExecutorApproveCmd(executorPatchID),
						m.smoothStreamTickCmd(),
					)
				}

				// ── EXECUTOR-OWNED APPLY (single authority) ──────────
				// The RuntimeExecutor is the sole mutation apply authority. A
				// proposal staged without an executor-held patch ID routes
				// through RuntimeExecutor.Approve with the dock's patch ID;
				// without a held patch the approval is a no-op with a message.
				if patch == nil {
					m.push(roleSystem, infoStyle.Render("No held patch to approve — the executor owns apply."))
					m.refreshViewportContent()
					m.followTail()
					return m, nil
				}
				m.appliedHotfixFile = patch.File
				return m, tea.Batch(
					func() tea.Msg { return agentStartMsg{label: "hotfix apply"} },
					m.runExecutorApproveCmd(patch.ID),
					m.smoothStreamTickCmd(),
				)

			case msg.String() == "alt+r" || msg.Type == tea.KeyEscape:
				rejectedPath := m.pendingHotfixTask.Target
				executorPatchID := m.executorPendingPatchID
				m.pendingHotfixTask = nil
				m.pendingHotfixPatch = nil
				m.pendingProposals = nil
				m.executorPendingPatchID = ""
				m.executorPendingTargets = nil
				m.resolveApprovalState()
				m.ti.Focus()
				m.recalcViewportHeight()
				m.push(roleSystem, infoStyle.Render(
					"  "+Icon.Error+" Rejected — hotfix aborted. No files were modified."))
				m.push(roleError, fmt.Sprintf(
					"[HOTFIX] Developer rejected patch to %s.",
					rejectedPath))

				m.hotfixActive = false
				if stashedTasks, rerr := m.restorePlan(); rerr == nil && len(stashedTasks) > 0 {
					m.sess.StageTaskList(&stashedTasks)
					_ = m.sess.Save()
				}
				m.refreshViewportContent()
				m.followTail()
				if executorPatchID != "" {
					return m, m.runExecutorRejectCmd(executorPatchID, "hotfix rejected by developer")
				}
				return m, m.runtimeRejectCmd(rejectedPath, "hotfix rejected by developer")
			}
			return m, nil
		}

		// ── Build approval (SHELL_EXEC permission box) ──────────────
		if m.pendingBuildApproval && m.pendingBuildTask != nil {
			task := m.pendingBuildTask
			switch {
			case msg.String() == "alt+a" || msg.Type == tea.KeyEnter:
				m.pendingBuildApproval = false
				m.pendingBuildTask = nil
				m.resolveApprovalState()
				m.recalcViewportHeight()
				m.ti.Focus()
				m.refreshViewportContent()
				m.followTail()
				m.push(roleSystem, infoStyle.Render("  "+Icon.Success+" Approved — executing shell command..."))
				return m, tea.Batch(
					func() tea.Msg { return agentStartMsg{label: "shell exec"} },
					m.runBuildShellExec(task),
					m.smoothStreamTickCmd(),
					m.runtimeApproveCmd(task.Target),
				)

			case msg.String() == "alt+l":
				m.pendingBuildAllowAlways = true
				m.pendingBuildApproval = false
				m.pendingBuildTask = nil
				m.resolveApprovalState()
				m.recalcViewportHeight()
				m.ti.Focus()
				m.refreshViewportContent()
				m.followTail()
				m.push(roleSystem, infoStyle.Render(
					"  "+Icon.Success+" Approved (always) — executing shell command..."))
				return m, tea.Batch(
					func() tea.Msg { return agentStartMsg{label: "shell exec"} },
					m.runBuildShellExec(task),
					m.smoothStreamTickCmd(),
					m.runtimeApproveCmd(task.Target),
				)

			case msg.String() == "alt+r" || msg.Type == tea.KeyEscape:
				m.pendingBuildApproval = false
				m.pendingBuildTask = nil
				m.resolveApprovalState()
				m.recalcViewportHeight()
				m.ti.Focus()
				if m.sess != nil {
					tasks := m.sess.CurrentTasks
					for i := range tasks {
						if tasks[i].StepNum == task.StepNum {
							tasks[i].Status = "stalled"
							break
						}
					}
					m.sess.StageTaskList(&tasks)
					_ = m.sess.Save()
				}
				m.push(roleSystem, infoStyle.Render(
					"  "+Icon.Error+" Rejected — shell execution aborted."))
				m.push(roleError, fmt.Sprintf(
					"[SECURITY] Aborting unauthorized shell execution: %s",
					task.Target))
				m.refreshViewportContent()
				m.followTail()
				return m, m.runtimeRejectCmd(task.Target, "shell execution rejected")
			}
			return m, nil
		}

		// ── Native Tool Call Buffer Approval ──────────────────────────
		if m.toolCallBuffer != nil && m.toolCallBuffer.HasPending() {
			switch {
			case msg.String() == "a":
				// Accept single call — apply approved calls to disk
				return m, m.applyToolCallBuffer()
			case msg.String() == "l":
				// Allow all — approve all and apply
				m.toolCallBuffer.ApproveAll()
				return m, m.applyToolCallBuffer()
			case msg.String() == "r" || msg.Type == tea.KeyEscape:
				// Reject — discard buffer
				m.toolCallBuffer.Reject()
				m.resolveApprovalState()
				m.recalcViewportHeight()
				m.push(roleSystem, infoStyle.Render("tool calls rejected"))
				return m, nil
			case msg.String() == "e":
				// Cycle effort level
				m.cycleEffort()
				m.refreshViewportContent()
				return m, nil
			}
			return m, nil
		}

		// ── File-mutation proposal approval ─────────────────────────
		switch {
		case msg.String() == "alt+a" || msg.Type == tea.KeyEnter:
			// The RuntimeExecutor is the sole mutation apply authority. The
			// executor-staged proposals are handled by the $hot approval gate
			// above (executorPatchID path). For the generic dock, route
			// through RuntimeExecutor.Approve when a held patch exists,
			// otherwise no-op with a message.
			if m.executorPendingPatchID != "" {
				return m, m.runExecutorApproveCmd(m.executorPendingPatchID)
			}
			if len(m.pendingProposals) > 0 {
				return m, m.runExecutorApproveCmd(m.pendingProposals[0].ID)
			}
			m.push(roleSystem, infoStyle.Render("No held patch to approve — the executor owns apply."))
			m.refreshViewportContent()
			m.followTail()
			return m, nil
		case msg.String() == "alt+l":
			if m.executorPendingPatchID != "" {
				return m, m.runExecutorApproveCmd(m.executorPendingPatchID)
			}
			if len(m.pendingProposals) > 0 {
				return m, m.runExecutorApproveCmd(m.pendingProposals[0].ID)
			}
			m.push(roleSystem, infoStyle.Render("No held patch to approve — the executor owns apply."))
			m.refreshViewportContent()
			m.followTail()
			return m, nil
		case msg.String() == "alt+p":
			if len(m.pendingProposals) > 0 {
				m.pendingProposals[0].Expanded = !m.pendingProposals[0].Expanded
				m.proposalDiffOffset = 0
				m.recalcViewportHeight()
				m.refreshViewportContent()
				m.followTail()
			}
			return m, nil
		case msg.String() == "alt+r" || msg.Type == tea.KeyEscape:
			if m.sess != nil {
				m.sess.ClearHistory()
				_ = m.sess.Save()
			}

			if m.currentBuildTaskID > 0 && m.sess != nil {
				tasks := m.sess.CurrentTasks
				for i := range tasks {
					if tasks[i].StepNum == m.currentBuildTaskID {
						tasks[i].Status = "stalled"
						break
					}
				}
				m.sess.StageTaskList(&tasks)
				_ = m.sess.Save()
			}

			m.ti.Focus()
			m.resolveApprovalState()
			m.recalcViewportHeight()
			m.awaitingConfirmation = false
			m.pendingProposals = nil
			m.acceptAll = false
			m.push(roleSystem, infoStyle.Render("changes rejected"))
			return m, m.runtimeRejectCmd("proposal", "changes rejected")
		}
		return m, nil
	}

	if msg.Type == tea.KeyEscape {
		if m.showHelpOverlay {
			m.showHelpOverlay = false
			return m, nil
		}
		if m.showSuggestions {
			m.dismissSuggestions()
			return m, nil
		}
		if m.streaming && m.streamCancel != nil {
			m.streamCancel()
			m.streamCancel = nil
			m.interruptRequested = true
			// ── APPLICATION-LAYER COMMAND RECORD ──────────────────────
			// The interruption is routed through the Runtime facade as a
			// CancelCmd so the canonical command/event contract observes it.
			return m, tea.Batch(
				func() tea.Msg { return TaskFinishedMsg{} },
				m.runtimeCancelCmd("stream interrupted"),
			)
		}
		if m.proposedShellCmd != "" {
			m.proposedShellCmd = ""
			m.push(roleSystem, infoStyle.Render("Command cancelled."))
		}
		// Build approval is now handled inside StateAwaitingApproval in the
		// block above. The escape key there stalls the task and returns to chat.
		m.ti.SetValue("")
		m.ti.Reset()
		m.syncInputFromTI()
		return m, nil
	}

	switch msg.Type {
	case tea.KeyCtrlD:
		if m.ti.Value() == "" && !m.agentRunning && !m.streaming && !m.reviewRunning && !m.pipelineRunning {
			// Route the exit through the confirmation gate so a stray Ctrl+D
			// can never close the session without an explicit [ Yes ].
			m.beginQuitConfirm()
			return m, nil
		}
		return m, nil

	case tea.KeyCtrlC:
		if m.showSuggestions {
			m.dismissSuggestions()
			return m, nil
		}
		if m.agentRunning || m.streaming || m.reviewRunning || m.pipelineRunning || m.planPending {
			execution.KillAllOrphans()
			m.cancelAllBackgroundContexts()
			m.push(roleSystem, infoStyle.Render("Interrupted."))
			// ── APPLICATION-LAYER COMMAND RECORD ──────────────────────
			// The hard interrupt is routed through the Runtime facade as a
			// CancelCmd so the canonical command/event contract observes it.
			return m, tea.Batch(
				func() tea.Msg { return TaskFinishedMsg{} },
				m.runtimeCancelCmd("ctrl-c interrupt"),
			)
		}
		m.ti.SetValue("")
		m.ti.Reset()
		m.syncInputFromTI()
		return m, nil

		// ── Enter: submit (only when autocomplete is NOT active) ───────────────
		// STALE-VIEWPORT GUARD: Every submission path MUST call
		// refreshViewportContent+GotoBottom before returning, guaranteeing
		// the user's input appears immediately rather than waiting for the
		// next UI tick. This prevents the "stale screen until next keypress"
		// regression.
		//
		// HUMAN-IN-THE-LOOP CHECKPOINT: If the agent proposed a shell command
		// (proposedShellCmd is set), the command was injected into the input bar
		// for review. Pressing Enter executes it as a shell command rather than
		// sending it to the LLM. The system remains deterministic, fully visible,
		// and safe from unintended execution.
	case tea.KeyEnter:
		return m.submitEnter()

		// ── History navigation (only when suggestions are NOT active) ─────────
	case tea.KeyUp:
		if m.showSuggestions && len(m.suggestions) > 0 {
			return m, nil
		}
		m.proposedShellCmd = ""
		if len(m.history) == 0 {
			return m, nil
		}
		if m.historyIndex > 0 {
			m.historyIndex--
		}
		m.ti.SetValue(m.history[m.historyIndex])
		m.ti.CursorEnd()
		m.syncInputFromTI()
		return m, nil

	case tea.KeyDown:
		if m.showSuggestions && len(m.suggestions) > 0 {
			return m, nil
		}
		m.proposedShellCmd = ""
		if m.historyIndex < len(m.history)-1 {
			m.historyIndex++
			m.ti.SetValue(m.history[m.historyIndex])
			m.ti.CursorEnd()
		} else {
			m.historyIndex = len(m.history)
			m.ti.SetValue("")
		}
		m.syncInputFromTI()
		return m, nil

		// ── '/' and '@' → forward to text input AND trigger suggestions ──────────
	case tea.KeyRunes:
		var tiCmd tea.Cmd
		m.ti, tiCmd = m.ti.Update(msg)
		m.syncInputFromTI()
		m.updateSuggestions()
		return m, tiCmd

		// ── Text-editing keys: forward directly to textinput, no swallowing ────
		// Suggestions are re-evaluated so moving the caret into a marker token
		// (or past it) re-projects the menu from the new cursor context.
	default:
		var tiCmd tea.Cmd
		m.ti, tiCmd = m.ti.Update(msg)
		m.syncInputFromTI()
		m.updateSuggestions()
		return m, tiCmd
	}
}

func (m *model) syncInputFromTI() {
	m.input.Reset()
	// Defensive strip: never let raw ANSI / mouse-tracking escape sequences
	// (e.g. \x1b[<0;26;37M) into the editable command buffer. Under normal
	// operation Bubble Tea parses mouse into tea.MouseMsg before it reaches the
	// textinput, but this guarantees the buffer stays clean regardless of
	// terminal raw-mode state during /build shell execution.
	m.input.WriteString(sanitizeInputBuffer(m.ti.Value()))
}

// submitEnter executes the canonical Enter submission path: it submits the
// current input buffer through handleInput with full shimmer, history, and
// runtime-command wiring. It is shared by the plain Enter handler and the
// autocomplete Enter path, which completes a unique whole-line suggestion
// before handing off here.
func (m *model) submitEnter() (tea.Model, tea.Cmd) {
	m.setScrollLocked(false)
	// A new user interaction reopens the activity surface sealed by /clear:
	// everything the user submits from here on is a fresh interaction whose
	// events belong in the viewport again (see lifecycle.go).
	m.unsealActivitySurface()

	userInput := m.ti.Value()
	m.dismissSuggestions()

	// ── Proposed shell command checkpoint ──────────────────────────────
	if m.proposedShellCmd != "" {
		cmd := m.proposedShellCmd
		m.proposedShellCmd = ""
		m.ti.SetValue("")
		m.ti.Reset()
		m.syncInputFromTI()
		m.push(roleUser, "$ "+cmd)
		m.lockTailToNewPrompt()
		// Live shell execution: start the running exec entry in the
		// activity tree, activate the loading dock, and dispatch the
		// shimmer + smooth ticks so the snowflake spinner animates for the
		// whole duration. Output streams into the tree and is expandable
		// via Ctrl+O; shellExitMsg stops the dock on completion.
		if m.activityTree != nil {
			m.activityTree.AppendOrUpdateExec(cmd, -1, 0, "")
		}
		m.startShimmer("Executing command...", "execute")
		return m, tea.Batch(
			m.streamShellCmd(cmd),
			m.smoothStreamTickCmd(),
			m.shimmerTickCmd(),
		)
	}

	if userInput != "" {
		m.currentPrompt = userInput
		if m.showBanner {
			m.showBanner = false
		}
		m.ti.SetValue("")
		m.ti.Reset()
		m.syncInputFromTI()

		m.history = append(m.history, userInput)
		m.historyIndex = len(m.history)
		m.saveHistory()

		m.push(roleUser, userInput)

		// ── T=0MS SHIMMER: instant visual feedback ─────────────────
		// Activate the loading shimmer BEFORE any orchestrator dispatch
		// or synchronous context planning (planContextForAsk, intent
		// classification) so the user sees the shimmer + tip dock
		// immediately on Enter — zero perceived lag. streamCmd() will
		// refine the shimmer text once the stream is ready; non-streaming
		// early-return paths call stopShimmer() to clean up.
		//
		// CRITICAL: the shimmer tick AND the smooth stream tick are
		// dispatched HERE, synchronously, as part of the returned batch —
		// NOT only downstream inside streamCmd(). If the Enter handler
		// returned without them, the first spinner.FrameMsg would be
		// scheduled only after the (now async) context prep resolves, so
		// the dock would sit frozen for the entire workspace scan. With
		// them in the batch the loading line animates from the very first
		// frame; streamCmd() re-schedules its own ticks when it runs.
		shimmerAlreadyActive := m.shimmerActive
		m.spinnerFrame = 0
		m.startShimmer("Working...", "analyze")

		m.streamStartTime = time.Now()
		cmd := m.handleInput(userInput)
		// ── CLEANUP: stop shimmer on non-streaming early returns ────
		// If handleInput returned nil or a non-stream command (error,
		// shell exec, pending confirm), the shimmer we started was never
		// consumed by streamCmd — deactivate it so the dock clears.
		// Guard: only stop if we freshly started it (not a leftover from
		// a previous stream that hasn't been reconciled yet).
		if cmd == nil && !shimmerAlreadyActive && m.shimmerActive {
			m.stopShimmer()
		}
		// ── APPLICATION-LAYER COMMAND RECORD ──────────────────────
		// The same submission is routed through the Runtime facade as a
		// SubmitPromptCmd so the canonical command/event contract observes
		// it. Nil-safe when no runtime is wired.
		cmd = tea.Batch(cmd, m.runtimeSubmitCmd(userInput))
		// ── INSTANT ANIMATION AT T=0MS ────────────────────────────
		// Dispatch the shimmer + smooth ticks alongside the submission so
		// the loading dock animates immediately, regardless of what the
		// submitted command does next (async prep, stream, engine run).
		// Both loops self-terminate when no background producer owns the
		// flags, so idle submits leak nothing.
		cmd = tea.Batch(cmd, m.shimmerTickCmd(), m.smoothStreamTickCmd())
		m.lockTailToNewPrompt()
		return m, cmd
	}
	m.ti.SetValue("")
	m.ti.Reset()
	m.syncInputFromTI()
	m.lockTailToNewPrompt()
	return m, nil
}

// lockTailToNewPrompt is the strict auto-scroll tail-lock performed on every
// prompt submission. It explicitly unlocks any user-scroll state, pins the
// app-owned scroll offset to the exact bottom tail (docScrollOffset =
// max(0, total - Viewport.Height) via the manual-slicing contract), and
// triggers a SYNCHRONOUS viewport flush so the new prompt appears at the
// bottom of the document instantly — never waiting for an external UI event
// or a throttled repaint tick.
func (m *model) lockTailToNewPrompt() {
	m.userScrolledAway = false
	m.userIsScrollingUp = false
	m.followTail()
	m.refreshViewportContentImmediate()
}

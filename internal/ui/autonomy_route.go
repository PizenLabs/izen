package ui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/hotfix"
	"github.com/PizenLabs/izen/internal/modes"
)

// runAutonomyRoutedCmd is the autonomy activation boundary. A human objective
// (e.g. "$prompt read @index.html and remove extra contents") flows through the
// autonomy runtime — intent classification, capability resolution, risk
// evaluation, autonomy controller, workspace selection — and only THEN executes.
// The runtime owns the decision; the mode engines own execution.
//
//   - conversation → DecisionDirectResponse → ASK chat (no workspace, no loop)
//   - read-only task → DecisionAutoContinue → execute the decided workspace
//   - mutation without grant → DecisionAskUser → proposal gate, then execute
//   - impossible action → DecisionBlock → explain
func (m *model) runAutonomyRoutedCmd(objective string) tea.Cmd {
	if m.autonomy == nil {
		return m.handleMessageContent(objective)
	}
	trace := m.autonomy.Decide(objective)
	return m.dispatchAutonomyTrace(trace)
}

// routeHotfixThroughAutonomy enters the autonomy runtime for a BUILD/hotfix
// execution request ("/build$hot check @index.html and remove redundant
// content"). The hotfix directive already expresses an execution intent, so it
// must NEVER bounce back to a mode command: the objective flows through
// intent → capability → workspace → decision, and the decided BUILD workspace
// executes with hotfix semantics.
func (m *model) routeHotfixThroughAutonomy(objective string) tea.Cmd {
	if m.autonomy == nil {
		// Legacy compatibility: no decision runtime wired — fall back to the
		// unified IntentGateway, which decides the execution path deterministically.
		return m.runHotExecution(objective)
	}
	m.autonomyHotfix = true
	m.pendingHotfixObjective = objective
	return m.runAutonomyRoutedCmd(objective)
}

// dispatchAutonomyTrace projects a decision trace onto the execution layer. It
// never guesses a workspace — it executes exactly what the runtime decided.
func (m *model) dispatchAutonomyTrace(trace autonomy.Trace) tea.Cmd {
	m.renderAutonomyDecision(trace)

	switch trace.Decision.Decision {
	case autonomy.DecisionDirectResponse:
		// Conversation: answer directly with no workspace switch, no timeline
		// and no autonomous loop. In /ask the full governed chat path runs; in
		// any other mode the generic chat stream answers without entering the
		// mode's execution engine.
		m.autonomyHotfix = false
		m.pendingHotfixObjective = ""
		if m.resolver.Current() == modes.ModeAsk {
			return m.handleMessageContent(trace.Input)
		}
		return m.streamCmd(trace.Input)
	case autonomy.DecisionAskUser:
		// ask_user is the ONLY verdict that renders the proposal surface
		// (authorization, risk acknowledgement, or target confirmation). The
		// human decides with ↑/↓ + Enter; Execute authorizes internally.
		return m.requestAutonomyProposal(trace)
	case autonomy.DecisionBlock:
		m.autonomyHotfix = false
		m.pendingHotfixObjective = ""
		m.push(roleError, "[autonomy] blocked: "+trace.Decision.Reason)
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	default: // auto_continue
		return m.executeAutonomyWorkspace(trace)
	}
}

// executeAutonomyWorkspace switches into the workspace the runtime selected and
// dispatches the objective to that workspace's engine. Workspace selection is
// capability-driven (autonomy package); the UI only executes.
func (m *model) executeAutonomyWorkspace(trace autonomy.Trace) tea.Cmd {
	mode, ok := modeForAutonomyWorkspace(trace.Route.Workspace)
	if !ok {
		return m.handleMessageContent(trace.Input)
	}
	if mode != m.resolver.Current() {
		m.modeChangeAuthorized = true
		m.currentResult = nil
		m.setMode(mode)
	}
	switch mode {
	case modes.ModeInvestigate:
		return m.runInvestigateCmd(trace.Input)
	case modes.ModeBuild:
		// A BUILD workspace executes through the bounded autonomous Driver when
		// it is wired (Phase 6): the driver owns resolve → observe → decide →
		// execute → interpret → approval → complete through the RuntimeExecutor
		// and publishes the canonical loop.transition events. The UI projects
		// results, renders the parked human boundary and resumes it. When the
		// driver is not wired (harness), the single-shot executor submission
		// below is the compatibility path.
		if m.autonomousDriver != nil {
			return m.executeAutonomyViaDriver(trace)
		}
		return m.executeAutonomyViaRuntime(trace)
	case modes.ModeReview:
		return m.runReviewCmd("")
	default: // plan / ask
		return m.handleMessageContent(trace.Input)
	}
}

// compileAutonomyBuildEvidence compiles the deterministic structural evidence
// for the resolved mutation target: the general Context Evidence Ledger
// (orphan text, invalid regions) plus, for HTML targets, the redundancy ledger
// (exact redundant blocks with line ranges). The model reasons over this
// ledger — it never re-discovers structural facts or redundant content from
// raw text (§9/§10). Returns "" when the target cannot be read.
func (m *model) compileAutonomyBuildEvidence(target string) string {
	content, err := os.ReadFile(target)
	if err != nil {
		return ""
	}
	var parts []string
	if m.autonomy != nil {
		if ledger := m.autonomy.CompileContext(target, string(content)).FormatEvidenceLedger(); ledger != "" {
			parts = append(parts, ledger)
		}
	}
	if isHTMLTarget(target) {
		if redundant, ok := hotfix.ResolveRedundantTargets(string(content)); ok && len(redundant) > 0 {
			if ledger := formatRedundancyLedger(target, redundant); ledger != "" {
				parts = append(parts, ledger)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

// renderAutonomyDecision presents the runtime's decision before execution so
// the human always sees the capability basis for the chosen workspace.
func (m *model) renderAutonomyDecision(trace autonomy.Trace) {
	var b strings.Builder
	b.WriteString(boldSapphireStyle.Render(Icon.Blueprint+" AUTONOMY DECISION") + "\n")
	fmt.Fprintf(&b, "  intent      : %s (%.0f%%)\n", trace.Intent.Intent, trace.Intent.Confidence*100)
	if len(trace.Intent.Targets) > 0 {
		fmt.Fprintf(&b, "  targets     : %s\n", strings.Join(trace.Intent.Targets, ", "))
	}
	req := trace.Intent.Required.String()
	if req == "" {
		req = "none"
	}
	fmt.Fprintf(&b, "  required    : %s\n", req)
	fmt.Fprintf(&b, "  workspace   : %s\n", trace.Route.Workspace)
	marker, label := autonomyMarker(trace.Decision.Decision)
	fmt.Fprintf(&b, "  decision    : %s%s (%s)\n", marker, label, trace.Decision.Reason)
	m.push(roleStatus, b.String())
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
}

// modeForAutonomyWorkspace maps a capability workspace onto the UI mode that
// executes it. The Ask workspace covers read-only explanation/analysis (e.g.
// "read file and explain" → READ + ANALYZE); it returns ok=true so the runtime
// switches into the ask workspace rather than leaking into whatever mode the
// user happened to be in.
func modeForAutonomyWorkspace(w autonomy.Workspace) (modes.Mode, bool) {
	switch w {
	case autonomy.WorkspaceAsk:
		return modes.ModeAsk, true
	case autonomy.WorkspaceInvestigate:
		return modes.ModeInvestigate, true
	case autonomy.WorkspacePlan:
		return modes.ModePlan, true
	case autonomy.WorkspaceBuild:
		return modes.ModeBuild, true
	case autonomy.WorkspaceReview:
		return modes.ModeReview, true
	default:
		return modes.ModeAsk, false
	}
}

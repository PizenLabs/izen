package ui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/command"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// runAutonomyRoutedCmd is the autonomy activation boundary. A human objective
// (e.g. "$prompt read @index.html and remove extra contents") flows through the
// autonomy runtime — intent classification, capability resolution, risk
// evaluation, autonomy controller, workspace selection — and only THEN executes.
// The runtime owns the decision; the mode engines own execution.
//
//   - conversation → DecisionDirectResponse → ASK chat (no workspace, no loop)
//   - read-only task → DecisionAutoContinue → execute the decided workspace
//   - mutation without grant → DecisionAskUser → grant gate, then execute
//   - impossible action → DecisionBlock → explain
func (m *model) runAutonomyRoutedCmd(objective string) tea.Cmd {
	if m.autonomy == nil {
		return m.handleMessageContent(objective)
	}
	trace := m.autonomy.Decide(objective)
	return m.dispatchAutonomyTrace(trace)
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
		if m.resolver.Current() == modes.ModeAsk {
			return m.handleMessageContent(trace.Input)
		}
		return m.streamCmd(trace.Input)
	case autonomy.DecisionAskUser:
		if len(trace.Decision.Missing) > 0 {
			return m.requestAutonomyGrant(trace)
		}
		m.push(roleSystem, infoStyle.Render("[autonomy] "+trace.Decision.Reason))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	case autonomy.DecisionBlock:
		m.push(roleError, "[autonomy] blocked: "+trace.Decision.Reason)
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	default: // auto_continue
		return m.executeAutonomyWorkspace(trace)
	}
}

// requestAutonomyGrant stages a capability grant request: the runtime asks the
// human for the missing mutation capability exactly once. The /grant command
// consumes pendingAutonomyGrant, issues the grant, re-runs the decision and
// executes the decided workspace — no repeated approval for the granted scope.
func (m *model) requestAutonomyGrant(trace autonomy.Trace) tea.Cmd {
	m.pendingAutonomyGrant = &trace

	var b strings.Builder
	b.WriteString(boldSapphireStyle.Render(Icon.Blueprint+" AUTONOMY GRANT REQUEST") + "\n")
	fmt.Fprintf(&b, "  workspace   : %s\n", trace.Route.Workspace)
	fmt.Fprintf(&b, "  required    : %s\n", trace.Intent.Required.String())
	fmt.Fprintf(&b, "  missing     : %s\n", strings.Join(capNames(trace.Decision.Missing), " + "))
	fmt.Fprintf(&b, "  reason      : %s\n", trace.Decision.Reason)
	b.WriteString("\n  " + infoStyle.Render("Approve with /grant — the runtime then executes inside this boundary without asking again.") + "\n")
	m.push(roleStatus, b.String())
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
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
	m.publishAutonomyLoop(trace)
	switch mode {
	case modes.ModeInvestigate:
		return m.runInvestigateCmd(trace.Input)
	case modes.ModeBuild:
		return m.executeAutonomyBuild(trace)
	case modes.ModeReview:
		return m.runReviewCmd("")
	default: // plan / ask
		return m.handleMessageContent(trace.Input)
	}
}

// publishAutonomyLoop records the canonical execution chain the runtime owns
// for the decided workspace. The loop state machine drives the transitions; the
// UI only executes inside the current position. Transitions are published on
// the shared bus so the event truth pipeline observes the loop.
func (m *model) publishAutonomyLoop(trace autonomy.Trace) {
	if m.autonomy == nil {
		return
	}
	loop := autonomy.NewAutonomousLoop(3)
	var trans []autonomy.LoopTransition
	trans = append(trans, loop.Start("user objective: "+trace.Input)...)

	switch trace.Intent.Intent {
	case autonomy.IntentInvestigation, autonomy.IntentDebugging:
		trans = append(trans, loop.EvidenceReady("evidence collected in INVESTIGATE workspace")...)
	case autonomy.IntentPlanning:
		trans = append(trans, loop.EvidenceReady("evidence sufficient — entering PLAN")...)
	case autonomy.IntentVerification:
		trans = append(trans, loop.EvidenceReady("evidence collected — entering REVIEW")...)
	case autonomy.IntentModification, autonomy.IntentRefactoring:
		trans = append(trans, loop.EvidenceReady("target resolved — entering PLAN")...)
		trans = append(trans, loop.AuthorizeBuild("capability granted — entering BUILD")...)
		trans = append(trans, loop.BuildDone("mutation dispatched")...)
		trans = append(trans, loop.VerifyPassed("verification queued")...)
	}
	if len(trans) > 0 {
		m.autonomy.PublishTransitions(trans)
	}
}

// executeAutonomyBuild stages the deterministic FILE_MUTATE plan the mutation
// target requires and hands it to the build engine. The engine owns the
// workflow (resolve → read → detect → propose → apply → verify); the runtime
// already authorized the BUILD capability domain through the grant gate.
//
// CONTEXT COMPILER VALIDATION (§6): before the build engine asks the model to
// propose a mutation, the runtime compiles the target's structural evidence
// (orphan text nodes, invalid regions, duplicate content, malformed structure)
// and appends the Context Evidence Ledger to the task description. The model
// never discovers deterministic facts on its own — it reasons over the ledger.
func (m *model) executeAutonomyBuild(trace autonomy.Trace) tea.Cmd {
	var tasks []plan.Task
	if target := trace.Intent.Target(); target != "" {
		tasks = command.GenerateFallbackPlan(command.FallbackPlanTarget{
			File:        target,
			Description: trace.Input,
			TaskType:    "FILE_MUTATE",
		})
		// Compile structural evidence for the resolved target so the proposal
		// the build engine generates is grounded in deterministic findings.
		if content, err := os.ReadFile(target); err == nil {
			ctx := m.autonomy.CompileContext(target, string(content))
			if ledger := ctx.FormatEvidenceLedger(); ledger != "" {
				for i := range tasks {
					tasks[i].Evidence = ledger
				}
			}
		}
	}
	if len(tasks) == 0 {
		m.push(roleSystem, infoStyle.Render("[autonomy] build workspace selected but no target resolved — describing objective to build engine."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m.runBuildCmd(trace.Input)
	}
	return func() tea.Msg {
		return planResultMsg{Tasks: tasks, IsFastTrack: true}
	}
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
	if trace.Decision.Decision == autonomy.DecisionAutoContinue {
		if loop := NewAutonomyLoopPreview(trace.Intent.Intent); len(loop) > 0 {
			fmt.Fprintf(&b, "  loop        : %s\n", strings.Join(loop, " → "))
		}
	}
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

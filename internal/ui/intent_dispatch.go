package ui

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/command"
	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/parser"
	cmdreg "github.com/PizenLabs/izen/pkg/domain/command"
)

// intentFromInput parses the raw input line against the active workspace. It
// is the sole entry point of the deterministic parser pipeline in the TUI
// dispatcher: every user input passes through it BEFORE any string-prefix
// command lookup or intent dispatch, so multi-directive quick-chains
// (/build$hot$test …) and marker-based intent resolve natively. The active
// session workspace supplies the default permission context when the line
// carries no /workspace marker.
//
// CONTINUOUS EXECUTION: when the current workspace lacks a directive's
// required permission AND the line declares no explicit /workspace marker,
// the parse is re-attempted against the directive's canonical execution
// context (directive_contract.go). The returned AST then declares that
// context, so dispatchASTIntent performs the internal mode transition and
// continues — the user is never forced to repeat the mode command. A line
// that explicitly declares /ask (or any other workspace) still honors that
// declaration and is rejected by the parser's permission policy unchanged.
func (m *model) intentFromInput(line string) (*parser.IntentAST, error) {
	current := workspaceForMode(m.resolver.Current())
	ast, err := parser.ParseInWorkspace(line, cmdreg.Default(), current)
	if err == nil {
		return m.alignDirectiveContext(ast, line), nil
	}

	var pe *parser.ParseError
	if !errors.As(err, &pe) || pe.Kind != parser.ErrPermissionDenied || pe.Marker != cmdreg.MarkerDollar {
		return nil, err
	}
	// An explicit /workspace marker overrides any auto-transition: the user
	// declared the context, so its permission boundary is authoritative.
	if _, explicit := workspaceFromInput(line, cmdreg.Default()); explicit {
		return nil, err
	}
	mode, ok := executionModeForDirective(pe.Name)
	if !ok || directiveWorksIn(pe.Name, m.resolver.Current()) {
		return nil, err
	}
	ast, retryErr := parser.ParseInWorkspace(line, cmdreg.Default(), workspaceForMode(mode))
	if retryErr != nil {
		return nil, err
	}
	return m.alignDirectiveContext(ast, line), nil
}

// alignDirectiveContext performs the continuous-execution mode alignment for a
// successfully parsed intent: when the intent carries directives and the
// active mode is not a native context for any of them, the AST's workspace is
// re-resolved to the single unambiguous execution context the directives
// share (directive_contract.go). dispatchASTIntent then performs the internal
// mode transition and execution continues — the user never repeats the mode
// command. An explicit /workspace marker in the line is always authoritative
// and prevents any alignment; directives with conflicting execution contexts
// (e.g. "$test $env" from one mode) are left untouched.
func (m *model) alignDirectiveContext(ast *parser.IntentAST, line string) *parser.IntentAST {
	if len(ast.Directives) == 0 {
		return ast
	}
	if _, explicit := workspaceFromInput(line, cmdreg.Default()); explicit {
		return ast
	}
	current := m.resolver.Current()
	target := modes.ModeAsk
	found := false
	for _, d := range ast.Directives {
		if directiveWorksIn(d.Name, current) {
			// At least one directive dispatches natively here: the mode is a
			// valid execution context — do not force a transition.
			return ast
		}
		mode, ok := executionModeForDirective(d.Name)
		if !ok {
			return ast
		}
		if !found {
			target = mode
			found = true
			continue
		}
		if mode != target {
			// Conflicting execution contexts: leave routing to the legacy
			// mode-scoped handler rather than guessing.
			return ast
		}
	}
	if found && target != current {
		ast.Workspace = workspaceForMode(target)
	}
	return ast
}

// dispatchASTIntent executes a parsed IntentAST through the active executor.
// It is the structured dispatch seam of the parser pipeline:
//
//  1. Workspace transition first: when the AST's effective workspace differs
//     from the active session mode, the mode is switched BEFORE any directive
//     or global command runs. The user explicitly typed the /workspace marker,
//     so the switch is authorized regardless of the plan-approval gate.
//  2. Global commands (/undo, /help, …) dispatch through the system command
//     handler.
//  3. Directives ($hot, $test, $prompt, …) dispatch through the mode-scoped
//     directive handlers, with $prompt routed to the /ask refinement router.
//
// The AST was already permission-validated by parser.ParseInWorkspace, so no
// re-validation occurs here.
func (m *model) dispatchASTIntent(ast *parser.IntentAST) tea.Cmd {
	var cmds []tea.Cmd

	// ── 1. WORKSPACE TRANSITION (BEFORE directives/globals) ──────────────
	target := modeForWorkspace(ast.Workspace)
	if target != m.resolver.Current() {
		m.modeChangeAuthorized = true
		m.setMode(target)
		cmds = append(cmds, m.runtimeSwitchCmd(target))
	}

	// ── COMPOSITE: /review $test ───────────────────────────────────────
	// The dynamic-test-then-review shortcut must run as a single pipeline, so
	// it intercepts before individual directive dispatch.
	if ast.Workspace == cmdreg.WorkspaceReview && hasDirective(ast, "test") {
		return tea.Batch(append(cmds, m.runReviewTestComposite())...)
	}

	// ── 2. GLOBAL COMMANDS ──────────────────────────────────────────────
	for _, g := range ast.GlobalCommands {
		cmds = append(cmds, m.handleCommand("/"+g.Name))
	}

	// ── 3. DIRECTIVES ───────────────────────────────────────────────────
	if len(ast.Directives) > 0 {
		cmds = append(cmds, m.dispatchDirectives(ast))
	}

	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// dispatchDirectives routes the AST's $ directives through the mode-scoped
// directive handlers. Directives are dispatched together as a single chained
// line so multi-directive quick-chaining (/build$hot$test …) routes through
// the same mode-specific handler the bare $ syntax uses. The $prompt directive
// is special-cased: it is the global /ask router, not a mode-scoped action.
func (m *model) dispatchDirectives(ast *parser.IntentAST) tea.Cmd {
	tail := directiveTail(ast)
	for _, d := range ast.Directives {
		if d.Name == "prompt" {
			return m.routePromptDirective(tail)
		}
	}

	var line strings.Builder
	for i, d := range ast.Directives {
		if i > 0 {
			line.WriteByte(' ')
		}
		line.WriteByte('$')
		line.WriteString(d.Name)
	}
	if tail != "" {
		line.WriteByte(' ')
		line.WriteString(tail)
	}
	if line.Len() == 0 {
		return nil
	}
	return m.handleReviewDollar(line.String())
}

// directiveTail renders the goal and @ scopes of an intent as the trailing
// argument payload passed to its directives, e.g. "$hot fix syntax in
// @index.html" → tail "fix syntax in @index.html".
func directiveTail(ast *parser.IntentAST) string {
	var b strings.Builder
	if ast.Goal != "" {
		b.WriteString(ast.Goal)
	}
	for _, s := range ast.Scopes {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('@')
		b.WriteString(s.Target)
	}
	return b.String()
}

// hasDirective reports whether the AST carries a directive with the given
// canonical name.
func hasDirective(ast *parser.IntentAST, name string) bool {
	for _, d := range ast.Directives {
		if d.Name == name {
			return true
		}
	}
	return false
}

// routePromptDirective implements the $prompt global router: a raw idea is
// refined into a structured /ask handoff. From ANY active mode it transitions
// cleanly to /ask, injecting the query as /ask input. It NEVER executes
// /build, /review, /plan, or /investigate logic inside the originating mode —
// the only allowed action is the transition to /ask. It is shared by the
// bare $prompt syntax and the AST-directive dispatch seam.
func (m *model) routePromptDirective(rawInput string) tea.Cmd {
	m.cancelStaleAgentOps()
	rawInput = strings.TrimSpace(rawInput)
	if rawInput == "" {
		m.push(roleError, "[Usage] $prompt <your raw architectural idea or description>")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// ── COMPRESSOR FAST-TRACK ──────────────────────────────────────────
	// Check the prompt compressor first. If it signals a direct mutation
	// (BypassInvest=true) with a target file, skip ALL Architect prompts, skip
	// /investigate mode routing entirely, and route directly to BUILD with a
	// staged FILE_MUTATE task.
	if compressed := gateway.CompressPrompt(rawInput); compressed != nil && compressed.BypassInvest && compressed.Target != "" {
		m.push(roleSystem, accentStyle.Render("[Fast-Track] Direct file mutation detected by compressor. Bypassing architect analysis."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		targets := gateway.ExtractDirectMutationTargets(rawInput)
		if len(targets) > 1 {
			var tasks []plan.Task
			for i, f := range targets {
				tasks = append(tasks, plan.Task{
					StepNum: i + 1,

					Status:      "idle",
					Type:        "FILE_MUTATE",
					Target:      f,
					Description: rawInput,
					Rationale:   fmt.Sprintf("Fast-Track multi-file decomposition: target %d of %d", i+1, len(targets)),
					IsHardcoded: true,
				})
			}
			return func() tea.Msg {
				return planResultMsg{
					Tasks:       tasks,
					IsFastTrack: true,
				}
			}
		}
		target := command.FallbackPlanTarget{
			File:        compressed.Target,
			Description: rawInput,
			TaskType:    "FILE_MUTATE",
		}
		tasks := command.GenerateFallbackPlan(target)
		return func() tea.Msg {
			return planResultMsg{
				Tasks:       tasks,
				IsFastTrack: true,
			}
		}
	}

	// ── INTENT PRE-GUARD: Fast-track direct file mutations ──────────────
	// If the user is requesting a simple single-file mutation on a non-code
	// file (e.g. $prompt rename author in @LICENSE), classify it and route
	// directly to /build as a FILE_MUTATE task with zero LLM involvement.
	if target, isDirect := gateway.ClassifyDirectMutation(rawInput); isDirect {
		m.push(roleSystem, accentStyle.Render("[Fast-Track] Direct file mutation detected. Bypassing architect analysis."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		multiTargets := gateway.ExtractDirectMutationTargets(rawInput)
		if len(multiTargets) > 1 {
			var tasks []plan.Task
			for i, f := range multiTargets {
				tasks = append(tasks, plan.Task{
					StepNum: i + 1,

					Status:      "idle",
					Type:        "FILE_MUTATE",
					Target:      f,
					Description: rawInput,
					Rationale:   fmt.Sprintf("Fast-Track multi-file decomposition: target %d of %d", i+1, len(multiTargets)),
					IsHardcoded: true,
				})
			}
			return func() tea.Msg {
				return planResultMsg{
					Tasks:       tasks,
					IsFastTrack: true,
				}
			}
		}
		tasks := command.GenerateFallbackPlan(target)
		return func() tea.Msg {
			return planResultMsg{
				Tasks:       tasks,
				IsFastTrack: true,
			}
		}
	}

	// ── MODE GUARD: transition to /ask, then refine ─────────────────────
	// Preserve the lean ask handoff prompt — we MUST NOT re-enter handleInput
	// because the raw input no longer carries the $prompt prefix and would be
	// routed to the normal AskContract() streaming path, producing
	// conversational noise instead of the structured handoff prompt.
	currentMode := m.resolver.Current()
	if currentMode != modes.ModeAsk {
		m.push(roleSystem, infoStyle.Render(fmt.Sprintf(
			"$prompt from /%s — transitioning to /ask for structured analysis...", currentMode)))
		m.modeChangeAuthorized = true
		cmd := m.setMode(modes.ModeAsk)
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return tea.Batch(cmd, m.runAskPromptHandoffCmd(rawInput))
	}

	m.push(roleSystem, infoStyle.Render("Refining prompt through ask handoff..."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return m.runAskPromptHandoffCmd(rawInput)
}

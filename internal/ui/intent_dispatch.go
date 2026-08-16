package ui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/modes"
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
	// An explicit /workspace marker overrides any fallback: the user declared
	// the context, so its permission boundary is authoritative.
	if _, explicit := workspaceFromInput(line, cmdreg.Default()); explicit {
		return nil, err
	}
	// Execution directives ($prompt/$hot) are EXECUTION REQUESTS resolved by
	// the runtime, never by the presentation mode: re-parse in the permissive
	// execution workspace WITHOUT any mode alignment.
	if pe.Name == "prompt" || pe.Name == "hot" {
		ast, retryErr := parser.ParseInWorkspace(line, cmdreg.Default(), cmdreg.WorkspaceBuild)
		if retryErr != nil {
			return nil, err
		}
		return ast, nil
	}
	// Legacy mode-scoped directives ($test/$fix/...) keep their continuous
	// execution alignment: re-parse in the directive's execution workspace.
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
// successfully parsed intent carrying legacy mode-scoped directives: when the
// active mode is not a native context for any of them, the AST's workspace is
// re-resolved to the single unambiguous execution context the directives share.
// Execution directives ($prompt/$hot) NEVER align — they are resolved by the
// runtime. An explicit /workspace marker in the line is always authoritative
// and prevents any alignment.
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
		if d.Name == "prompt" || d.Name == "hot" {
			// Execution directives never align to a mode.
			continue
		}
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
			return ast
		}
	}
	if found && target != current {
		ast.Workspace = workspaceForMode(target)
	}
	return ast
}

// hasExecutionDirective reports whether the intent carries an execution
// directive ($prompt/$hot) that the runtime resolves.
func hasExecutionDirective(ast *parser.IntentAST) bool {
	for _, d := range ast.Directives {
		if d.Name == "prompt" || d.Name == "hot" {
			return true
		}
	}
	return false
}

// dispatchASTIntent executes a parsed IntentAST through the execution gateway
// and the system command handler. It is the structured dispatch seam of the
// parser pipeline:
//
//  1. Workspace transition for an explicit /workspace marker OR legacy
//     directive alignment — NEVER for an execution directive ($prompt/$hot).
//     Modes are presentation contexts only; the unified gateway + RuntimeExecutor
//     decide the execution path.
//  2. Global commands (/undo, /help, …) dispatch through the system command
//     handler.
//  3. Directives dispatch through the execution gateway or the mode-scoped
//     legacy handlers.
func (m *model) dispatchASTIntent(ast *parser.IntentAST, line string) tea.Cmd {
	var cmds []tea.Cmd

	// ── 1. WORKSPACE TRANSITION (presentation only, never for execution) ──
	target := modeForWorkspace(ast.Workspace)
	if target != m.resolver.Current() {
		_, explicit := workspaceFromInput(line, cmdreg.Default())
		// Execution directives never transition the mode — the runtime decides
		// the path. Explicit markers and legacy directive alignment do.
		if !hasExecutionDirective(ast) || explicit {
			// CONTINUOUS $fix: preserve the transient test context across the
			// internal transition so $fix still consumes lastTestOutput.
			var savedOut, savedTarget string
			savedFailed := m.lastTestFailed
			if hasDirective(ast, "fix") {
				savedOut, savedTarget = m.lastTestOutput, m.lastTestTarget
			}
			m.modeChangeAuthorized = true
			m.setMode(target)
			if hasDirective(ast, "fix") {
				m.lastTestOutput, m.lastTestTarget, m.lastTestFailed = savedOut, savedTarget, savedFailed
			}
			cmds = append(cmds, m.runtimeSwitchCmd(target))
		}
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

// dispatchDirectives routes the AST's $ directives through the execution
// gateway or the mode-scoped directive handlers. Execution directives
// ($prompt, $hot) are EXECUTION REQUESTS: they cross the unified IntentGateway
// (unconditional Strategy.Select → ExecuteRequest) and never route by mode. The
// remaining mode-scoped directives ($test, $fix, ...) keep their legacy
// handlers.
func (m *model) dispatchDirectives(ast *parser.IntentAST) tea.Cmd {
	tail := directiveTail(ast)
	for _, d := range ast.Directives {
		switch d.Name {
		case "prompt":
			return m.routePromptDirective(tail)
		case "hot":
			return m.runHotExecution(tail)
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

// routePromptDirective implements the $prompt execution directive. A $prompt is
// an EXECUTION REQUEST — not a message command. It crosses the unified
// IntentGateway, which selects the strategy deterministically (Strategy.Select
// unconditional) and produces an ExecuteRequest. The runtime decides the
// execution path; the UI never routes by mode, never triggers a hidden /build,
// and never invokes a provider here.
func (m *model) routePromptDirective(rawInput string) tea.Cmd {
	return m.runPromptExecution(rawInput)
}

package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/parser"
	cmdreg "github.com/PizenLabs/izen/pkg/domain/command"
)

// ── DoD: /build$hot quick-chain from /ask ────────────────────────────────

// TestIntentFromInputDirectiveChain verifies the parse seam of the
// deterministic parser pipeline: submitting "/build$hot fix syntax in
// @index.html" parses into a structured IntentAST carrying the build
// workspace, the $hot directive, and the @index.html file scope.
func TestIntentFromInputDirectiveChain(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)

	ast, err := m.intentFromInput("/build$hot fix syntax in @index.html")
	if err != nil {
		t.Fatalf("intentFromInput(%q) unexpected error: %v", "/build$hot fix syntax in @index.html", err)
	}
	if ast.Workspace != cmdreg.WorkspaceBuild {
		t.Errorf("Workspace = %v, want build", ast.Workspace)
	}
	if len(ast.Directives) != 1 || ast.Directives[0].Name != "hot" {
		t.Errorf("Directives = %+v, want [$hot]", ast.Directives)
	}
	if len(ast.Scopes) != 1 {
		t.Fatalf("Scopes = %+v, want [@index.html]", ast.Scopes)
	}
	if ast.Scopes[0].Target != "index.html" {
		t.Errorf("Scope target = %q, want %q", ast.Scopes[0].Target, "index.html")
	}
	if ast.Scopes[0].Type != parser.ScopeFile {
		t.Errorf("Scope type = %v, want file", ast.Scopes[0].Type)
	}
	if ast.Goal != "fix syntax in" {
		t.Errorf("Goal = %q, want %q", ast.Goal, "fix syntax in")
	}
}

// TestHandleInputDirectiveChainFromAsk drives the full dispatcher with the
// DoD input: it must parse cleanly (no "unknown command" fallback),
// auto-transition the workspace from /ask to /build BEFORE dispatching, and
// route the $hot directive into the hotfix pipeline.
func TestHandleInputDirectiveChainFromAsk(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	cmd := m.handleInput("/build$hot fix syntax in @index.html")
	if cmd == nil {
		t.Fatal("handleInput returned nil cmd — directive dispatch did not fire")
	}

	// Workspace automatically transitions to /build.
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Errorf("workspace = /%s, want /build after parser-pipeline transition", got)
	}

	// No raw "unknown command: <full raw input>" fallback is ever emitted.
	for _, r := range m.records {
		if strings.Contains(r.text, "unknown command") {
			t.Errorf("unknown-command fallback emitted: %q", r.text)
		}
	}

	// The $hot directive dispatched the hotfix pipeline with the goal + scope.
	found := false
	for _, r := range m.records {
		if strings.Contains(r.text, "[HOTFIX] Urgent hotfix: fix syntax in @index.html") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected $hot to dispatch the hotfix pipeline with the goal and @index.html scope")
	}
}

// TestHandleInputDirectiveChainSameWorkspace verifies the chain also executes
// when the declared workspace already matches the active mode (no redundant
// transition, directive still dispatched).
func TestHandleInputDirectiveChainSameWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := newTestModel()
	m.resolver.Set(modes.ModeBuild)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	cmd := m.handleInput("/build$hot fix syntax in @index.html")
	if cmd == nil {
		t.Fatal("handleInput returned nil cmd — directive dispatch did not fire")
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Errorf("workspace = /%s, want /build (unchanged)", got)
	}
	for _, r := range m.records {
		if strings.Contains(r.text, "unknown command") {
			t.Errorf("unknown-command fallback emitted: %q", r.text)
		}
	}
}

// ── Parse-error handling: structured errors, no raw dumps ────────────────

// TestHandleInputUnknownCommandShowsParseError verifies a genuinely unknown
// command surfaces the parser's formatted error instead of the legacy raw
// "unknown command: <full input>" dump, and execution stops.
func TestHandleInputUnknownCommandShowsParseError(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)

	cmd := m.handleInput("/bogus")
	if cmd != nil {
		t.Fatalf("handleInput(/bogus) returned a cmd (%T) — execution must stop after parse error", cmd)
	}
	if got := m.resolver.Current(); got != modes.ModeAsk {
		t.Errorf("workspace = /%s, want /ask (unchanged on parse error)", got)
	}
	found := false
	for _, r := range m.records {
		if strings.Contains(r.text, `parser: unknown command "/bogus"`) {
			found = true
		}
		if strings.Contains(r.text, "unknown command: /bogus") {
			t.Errorf("raw unknown-command fallback emitted: %q", r.text)
		}
	}
	if !found {
		t.Error("expected the formatted parser error to be surfaced in the chat log")
	}
}

// TestHandleInputHotFromAskAutoTransitionsToBuild verifies a $hot directive
// typed inside /ask (no /workspace marker) does NOT dead-end on a permission
// error: the parser pipeline re-resolves the directive's execution context
// (/build) and the dispatcher transitions internally and continues — the user
// is never forced to repeat `/build`.
func TestHandleInputHotFromAskAutoTransitionsToBuild(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	cmd := m.handleInput("$hot fix login timeout @index.html")
	if cmd == nil {
		t.Fatal("handleInput($hot in ask) returned nil cmd — continuous execution must dispatch")
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Errorf("workspace = /%s, want /build after internal auto-transition", got)
	}
	found := false
	for _, r := range m.records {
		if strings.Contains(r.text, "[HOTFIX] Urgent hotfix:") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected $hot to dispatch the hotfix pipeline after the internal /build transition")
	}
}

// ── Directive routing preserved through the AST dispatch ────────────────

// TestHandleInputHotFromBuild verifies the bare $hot directive inside /build
// still dispatches through the mode-scoped handler after the parser pipeline.
// The directive tail is the canonical reconstruction (goal then @scopes), so
// the hotfix prompt carries the goal and the LICENSE scope.
func TestHandleInputHotFromBuild(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := newTestModel()
	m.resolver.Set(modes.ModeBuild)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	cmd := m.handleInput("$hot rename author in @LICENSE to Hashirama")
	if cmd == nil {
		t.Fatal("handleInput($hot in build) returned nil cmd — hotfix dispatch did not fire")
	}
	found := false
	for _, r := range m.records {
		if strings.Contains(r.text, "[HOTFIX] Urgent hotfix:") && strings.Contains(r.text, "Hashirama") && strings.Contains(r.text, "@LICENSE") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected $hot to dispatch the hotfix pipeline with the goal and @LICENSE scope")
	}
}

// TestHandleInputPromptRoutesToAsk verifies $prompt still transitions to /ask
// after moving its routing into the AST dispatch (routePromptDirective).
func TestHandleInputPromptRoutesToAsk(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeBuild)

	cmd := m.handleInput("$prompt design a plugin architecture")
	if cmd == nil {
		t.Fatal("handleInput($prompt) returned nil cmd — /ask handoff did not fire")
	}
	if got := m.resolver.Current(); got != modes.ModeAsk {
		t.Errorf("workspace = /%s, want /ask after $prompt router", got)
	}
}

// TestDispatchASTIntentCompositeReviewTest verifies /review $test routes to
// the dynamic-test-then-review composite as a single pipeline.
func TestDispatchASTIntentCompositeReviewTest(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)

	cmd := m.handleInput("/review $test")
	if cmd == nil {
		t.Fatal("handleInput(/review $test) returned nil cmd — composite did not fire")
	}
	if got := m.resolver.Current(); got != modes.ModeReview {
		t.Errorf("workspace = /%s, want /review", got)
	}
	if !m.reviewRunning {
		t.Error("reviewRunning not set — composite pipeline must claim the spinner")
	}
}

// TestDispatchASTIntentGlobalCommand verifies /build /undo dispatches the
// global command after transitioning workspace, without a raw command dump.
func TestDispatchASTIntentGlobalCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	m.handleInput("/build /undo")
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Errorf("workspace = /%s, want /build", got)
	}
	found := false
	for _, r := range m.records {
		if strings.Contains(r.text, "no checkpoints to undo") {
			found = true
		}
		if strings.Contains(r.text, "unknown command") {
			t.Errorf("unknown-command fallback emitted: %q", r.text)
		}
	}
	if !found {
		t.Error("expected /undo to dispatch through the global-command handler")
	}
}

// TestHandleInputTestFromInvestigate verifies the $test directive typed inside
// /investigate still dispatches through the parser pipeline: investigate
// grants Execute (bounded validation) but not Write, so test/run parse while
// hot/fix stay denied.
func TestHandleInputTestFromInvestigate(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeInvestigate)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	cmd := m.handleInput("$test ./...")
	if cmd == nil {
		t.Fatal("handleInput($test in investigate) returned nil cmd — test dispatch did not fire")
	}
	if got := m.resolver.Current(); got != modes.ModeInvestigate {
		t.Errorf("workspace = /%s, want /investigate", got)
	}
}

// TestHandleInputLogFromReview verifies the $log directive (registry-backed)
// dispatches through the parser pipeline in /review.
func TestHandleInputLogFromReview(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeReview)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	cmd := m.handleInput("$log")
	if cmd == nil {
		t.Fatal("handleInput($log in review) returned nil cmd — log dispatch did not fire")
	}
	for _, r := range m.records {
		if strings.Contains(r.text, "unknown command") {
			t.Errorf("unknown-command fallback emitted: %q", r.text)
		}
	}
}

// TestHandleInputHotFromInvestigateAutoTransitionsToBuild verifies a $hot
// directive typed inside /investigate auto-transitions into /build (the
// directive's execution context) and continues, instead of dead-ending on a
// write-permission denial. An explicit /ask / /investigate marker in the same
// line still honors the declared context (see TestHandleInputExplicitAskMarker
// below).
func TestHandleInputHotFromInvestigateAutoTransitionsToBuild(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := newTestModel()
	m.resolver.Set(modes.ModeInvestigate)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	cmd := m.handleInput("$hot fix x @index.html")
	if cmd == nil {
		t.Fatal("handleInput($hot in investigate) returned nil cmd — continuous execution must dispatch")
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Errorf("workspace = /%s, want /build after internal auto-transition", got)
	}
	found := false
	for _, r := range m.records {
		if strings.Contains(r.text, "[HOTFIX] Urgent hotfix:") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected $hot to dispatch the hotfix pipeline after the internal /build transition")
	}
}

// TestHandleInputExplicitAskMarkerStillDenied verifies an explicit /workspace
// marker in the line is authoritative: "$ask $hot x" is rejected by the
// parser's permission policy unchanged — the user declared /ask, so /ask's
// read-only boundary applies.
func TestHandleInputExplicitAskMarkerStillDenied(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeBuild)

	cmd := m.handleInput("/ask $hot fix x")
	if cmd != nil {
		t.Fatalf("handleInput(/ask $hot) returned a cmd (%T) — explicit-marker permission denial must stop dispatch", cmd)
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Errorf("workspace = /%s, want /build (unchanged on explicit-marker denial)", got)
	}
	found := false
	for _, r := range m.records {
		if strings.Contains(r.text, `requires write`) && strings.Contains(r.text, `/ask`) {
			found = true
		}
	}
	if !found {
		t.Error("expected a permission-denied parse error referencing /ask in the chat log")
	}
}

// TestHandleInputBareWorkspaceSwitch verifies a plain /plan switch still works
// through the legacy string routing after the parse pipeline falls through.
func TestHandleInputBareWorkspaceSwitch(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false

	m.handleInput("/plan")
	if got := m.resolver.Current(); got != modes.ModePlan {
		t.Errorf("workspace = /%s, want /plan", got)
	}
}

// TestDirectiveTailRendersGoalAndScopes verifies the directive argument
// payload reconstruction preserves goal + @scope tokens.
func TestDirectiveTailRendersGoalAndScopes(t *testing.T) {
	ast := &parser.IntentAST{
		Goal: "fix syntax in",
		Scopes: []parser.SemanticScope{
			{Type: parser.ScopeFile, Target: "index.html"},
		},
	}
	if got := directiveTail(ast); got != "fix syntax in @index.html" {
		t.Errorf("directiveTail = %q, want %q", got, "fix syntax in @index.html")
	}
}

// TestModeForWorkspaceInverse verifies modeForWorkspace is the exact inverse of
// workspaceForMode across every workspace.
func TestModeForWorkspaceInverse(t *testing.T) {
	for _, m := range []modes.Mode{modes.ModeAsk, modes.ModePlan, modes.ModeBuild, modes.ModeInvestigate, modes.ModeReview} {
		if got := modeForWorkspace(workspaceForMode(m)); got != m {
			t.Errorf("modeForWorkspace(workspaceForMode(%v)) = %v, want %v", m, got, m)
		}
	}
}

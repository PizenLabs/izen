package parser

import (
	"errors"
	"reflect"
	"testing"

	"github.com/PizenLabs/izen/pkg/domain/command"
)

func parseOK(t *testing.T, input string) *IntentAST {
	t.Helper()
	ast, err := Parse(input, command.Default())
	if err != nil {
		t.Fatalf("Parse(%q) unexpected error: %v", input, err)
	}
	return ast
}

func parseErrKind(t *testing.T, input string, want ErrorKind) *ParseError {
	t.Helper()
	_, err := Parse(input, command.Default())
	if err == nil {
		t.Fatalf("Parse(%q) expected error kind %q, got nil", input, want)
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("Parse(%q) error %T, want *ParseError", input, err)
	}
	if pe.Kind != want {
		t.Fatalf("Parse(%q) error kind = %q (%v), want %q", input, pe.Kind, err, want)
	}
	return pe
}

func directive(t *testing.T, name string) command.CommandDescriptor {
	t.Helper()
	d, ok := command.Default().Lookup(command.MarkerDollar, name)
	if !ok {
		t.Fatalf("Lookup($%s) failed", name)
	}
	return *d
}

// TestParsePrimaryExample is the canonical spec example: workspace, two
// chained directives, a scope, and a goal.
func TestParsePrimaryExample(t *testing.T) {
	ast := parseOK(t, "/build$hot$test @auth.go fix deadlock")
	if ast.Workspace != command.WorkspaceBuild {
		t.Errorf("Workspace = %v, want build", ast.Workspace)
	}
	wantDirs := []command.CommandDescriptor{directive(t, "hot"), directive(t, "test")}
	if !reflect.DeepEqual(ast.Directives, wantDirs) {
		t.Errorf("Directives = %v, want %v", ast.Directives, wantDirs)
	}
	wantScopes := []SemanticScope{{Type: ScopeFile, Target: "auth.go"}}
	if !reflect.DeepEqual(ast.Scopes, wantScopes) {
		t.Errorf("Scopes = %v, want %v", ast.Scopes, wantScopes)
	}
	if ast.Goal != "fix deadlock" {
		t.Errorf("Goal = %q, want %q", ast.Goal, "fix deadlock")
	}
	if ast.Metadata != (ASTMetadata{}) {
		t.Errorf("Metadata = %+v, want zero value", ast.Metadata)
	}
}

// TestParseOrderIndependence verifies the three equivalent forms from the
// interaction-language spec all yield the same AST.
func TestParseOrderIndependence(t *testing.T) {
	forms := []string{
		"/build $hot fix login timeout @auth.go",
		"/build fix login timeout @auth.go $hot",
		"/build @auth.go $hot fix login timeout",
	}
	first := parseOK(t, forms[0])
	for _, form := range forms[1:] {
		got := parseOK(t, form)
		if !reflect.DeepEqual(got, first) {
			t.Errorf("form %q produced %v, want %v (same as %q)", form, got, first, forms[0])
		}
	}
}

// TestParseDirectiveBeforeWorkspace verifies the permission check is deferred
// until the workspace is known, so $test /build equals /build $test.
func TestParseDirectiveBeforeWorkspace(t *testing.T) {
	a := parseOK(t, "$test /build run tests")
	b := parseOK(t, "/build $test run tests")
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("$test /build (%v) != /build $test (%v)", a, b)
	}
	if a.Workspace != command.WorkspaceBuild {
		t.Errorf("Workspace = %v, want build", a.Workspace)
	}
}

func TestParseDefaultWorkspaceAsk(t *testing.T) {
	ast := parseOK(t, "fix login timeout")
	if ast.Workspace != command.WorkspaceAsk {
		t.Errorf("Workspace = %v, want ask (default)", ast.Workspace)
	}
	if ast.Goal != "fix login timeout" {
		t.Errorf("Goal = %q, want %q", ast.Goal, "fix login timeout")
	}
	if len(ast.Directives) != 0 || len(ast.Scopes) != 0 {
		t.Errorf("unexpected directives/scopes: %+v", ast)
	}
}

// TestParseCanonicalString checks the compact rendering of a parsed intent.
func TestParseCanonicalString(t *testing.T) {
	ast := parseOK(t, "/build$hot$test @auth.go fix deadlock")
	if got := ast.String(); got != "/build $hot $test @auth.go fix deadlock" {
		t.Errorf("String() = %q, want %q", got, "/build $hot $test @auth.go fix deadlock")
	}
	if got := parseOK(t, "fix x").String(); got != "/ask fix x" {
		t.Errorf("bare text String() = %q, want %q", got, "/ask fix x")
	}
}

// TestParseAllowedDirectivesByWorkspace walks every workspace×directive pair
// through the parser and asserts it matches GetAllowedDirectives.
func TestParseAllowedDirectivesByWorkspace(t *testing.T) {
	reg := command.Default()
	workspaces := []command.WorkspaceType{
		command.WorkspaceAsk,
		command.WorkspaceInvestigate,
		command.WorkspacePlan,
		command.WorkspaceBuild,
		command.WorkspaceReview,
	}
	directives := reg.GetAllowedDirectives(command.WorkspaceBuild)
	for _, ws := range workspaces {
		allowed := map[string]bool{}
		for _, d := range reg.GetAllowedDirectives(ws) {
			allowed[d.Name] = true
		}
		for _, d := range directives {
			input := "/" + ws.String() + " $" + d.Name + " task"
			if allowed[d.Name] {
				ast := parseOK(t, input)
				if len(ast.Directives) != 1 || ast.Directives[0].Name != d.Name {
					t.Errorf("%s: directive %q not collected: %v", ws, d.Name, ast)
				}
			} else {
				pe := parseErrKind(t, input, ErrPermissionDenied)
				if pe.Name != d.Name {
					t.Errorf("%s: denied directive name = %q, want %q", ws, pe.Name, d.Name)
				}
			}
		}
	}
}

func TestParsePermissionDeniedMessages(t *testing.T) {
	pe := parseErrKind(t, "/ask $hot fix x", ErrPermissionDenied)
	want := "parser: directive \"$hot\" requires write but workspace /ask grants read at 1:6"
	if got := pe.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestParseUnknownCommand(t *testing.T) {
	for _, input := range []string{"/bogus", "$bogus", "/build $wat"} {
		_ = parseErrKind(t, input, ErrUnknownCommand)
	}
}

func TestParseEmptyName(t *testing.T) {
	for _, input := range []string{"/build $", "/", "$", "@", "/build @"} {
		_ = parseErrKind(t, input, ErrEmptyName)
	}
}

func TestParseEmptyInput(t *testing.T) {
	for _, input := range []string{"", "   ", "\n\t"} {
		_ = parseErrKind(t, input, ErrEmptyInput)
	}
}

func TestParseMultipleWorkspaces(t *testing.T) {
	_ = parseErrKind(t, "/build /plan", ErrMultipleWorkspaces)
	_ = parseErrKind(t, "/plan $hot /build", ErrMultipleWorkspaces)
}

func TestParseGlobalCommandRejected(t *testing.T) {
	for _, input := range []string{"/build /help", "/help", "/usage", "/build /commit $hot"} {
		_ = parseErrKind(t, input, ErrUnsupportedCommand)
	}
}

// TestParseCaseInsensitivity verifies marker names match regardless of case
// and resolve to their canonical lowercase descriptor.
func TestParseCaseInsensitivity(t *testing.T) {
	ast := parseOK(t, "/BUILD $HOT fix")
	if ast.Workspace != command.WorkspaceBuild {
		t.Errorf("Workspace = %v, want build", ast.Workspace)
	}
	if ast.Directives[0].Name != "hot" {
		t.Errorf("Directive name = %q, want hot", ast.Directives[0].Name)
	}
}

func TestParseDeduplication(t *testing.T) {
	ast := parseOK(t, "/build $test $test @auth.go @auth.go run")
	if len(ast.Directives) != 1 {
		t.Errorf("Directives = %v, want 1 (deduped)", ast.Directives)
	}
	if len(ast.Scopes) != 1 {
		t.Errorf("Scopes = %v, want 1 (deduped)", ast.Scopes)
	}
}

// TestParseScopeClassification exercises the deterministic File/Symbol/Diff
// classifier.
func TestParseScopeClassification(t *testing.T) {
	tests := []struct {
		target string
		want   SemanticScopeType
	}{
		{"auth.go", ScopeFile},
		{"internal/auth.go", ScopeFile},
		{"pkg/domain/command", ScopeFile},
		{"Server.Handle", ScopeSymbol},
		{"pkg.Validate", ScopeSymbol},
		{"auth.go:42", ScopeSymbol},
		{"HEAD~1..HEAD", ScopeDiff},
		{"main..feature", ScopeDiff},
	}
	for _, tc := range tests {
		ast := parseOK(t, "/build @"+tc.target+" task")
		if len(ast.Scopes) != 1 {
			t.Fatalf("scopes for @%s = %v, want 1", tc.target, ast.Scopes)
		}
		if ast.Scopes[0].Type != tc.want {
			t.Errorf("classifyScope(%q) = %v, want %v", tc.target, ast.Scopes[0].Type, tc.want)
		}
	}
}

func TestParseWorkspaceAlone(t *testing.T) {
	ast := parseOK(t, "/build")
	if ast.Workspace != command.WorkspaceBuild || ast.Goal != "" {
		t.Errorf("got %v, want build workspace with empty goal", ast)
	}
}

func TestParseDefaultWrapper(t *testing.T) {
	ast, err := ParseDefault("/plan design schema")
	if err != nil {
		t.Fatalf("ParseDefault error: %v", err)
	}
	if ast.Workspace != command.WorkspacePlan {
		t.Errorf("Workspace = %v, want plan", ast.Workspace)
	}
}

func TestParseNilRegistryFallsBackToDefault(t *testing.T) {
	ast, err := Parse("/build $env", nil)
	if err != nil {
		t.Fatalf("Parse with nil registry error: %v", err)
	}
	if ast.Workspace != command.WorkspaceBuild {
		t.Errorf("Workspace = %v, want build", ast.Workspace)
	}
}

func TestParseReviewTestComposite(t *testing.T) {
	ast := parseOK(t, "/review $test")
	if ast.Workspace != command.WorkspaceReview {
		t.Errorf("Workspace = %v, want review", ast.Workspace)
	}
	if len(ast.Directives) != 1 || ast.Directives[0].Name != "test" {
		t.Errorf("Directives = %v, want [$test]", ast.Directives)
	}
}

func TestParseChainSupport(t *testing.T) {
	// Every registered directive and workspace supports chaining, so a chain
	// parses cleanly and the chain contract holds.
	ast := parseOK(t, "/review $run $test verify")
	if len(ast.Directives) != 2 {
		t.Errorf("Directives = %v, want [$run $test]", ast.Directives)
	}
}

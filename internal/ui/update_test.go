package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/templates"
)

func TestRenderMITLicense(t *testing.T) {
	tests := []struct {
		name        string
		description string
		wantAuthor  string
		wantYear    string
	}{
		{
			name:        "quoted author with year",
			description: `create MIT LICENSE with author 'TOMATO' 2026`,
			wantAuthor:  "TOMATO",
			wantYear:    "2026",
		},
		{
			name:        "unquoted author after author keyword",
			description: "MIT LICENSE author TOMATO 2026",
			wantAuthor:  "TOMATO",
			wantYear:    "2026",
		},
		{
			name:        "no author uses git config default",
			description: "create MIT LICENSE",
			wantAuthor:  "",
			wantYear:    "2026",
		},
		{
			name:        "double quoted author",
			description: `create MIT license with author "Jane Doe" 2025`,
			wantAuthor:  "Jane Doe",
			wantYear:    "2025",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content, ok := templates.RenderLicense("mit", tc.description)
			if !ok {
				t.Fatal("RenderLicense returned false for mit type")
			}
			if !strings.Contains(content, "MIT License") {
				t.Errorf("content missing MIT License header")
			}
			if !strings.Contains(content, tc.wantYear) {
				t.Errorf("content missing year %q", tc.wantYear)
			}
			if tc.wantAuthor != "" && !strings.Contains(content, tc.wantAuthor) {
				t.Errorf("content missing author %q\ncontent:\n%s", tc.wantAuthor, content)
			}
		})
	}
}

func TestGenerateTrivialContent(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		desc    string
		wantErr bool
	}{
		{"MIT LICENSE", "LICENSE", "MIT LICENSE author TOMATO 2026", false},
		{"lowercase license", "license", "create license", false},
		{".gitignore", ".gitignore", "create gitignore", false},
		{".env", ".env", "create env file", false},
		{"unknown returns empty", "main.go", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := generateTrivialContent(tc.target, tc.desc)
			if tc.wantErr && got != "" {
				t.Errorf("expected empty, got %q", got)
			}
			if !tc.wantErr && got == "" {
				t.Errorf("expected non-empty content")
			}
		})
	}
}

func TestIsYear(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"2026", true},
		{"1999", true},
		{"2099", true},
		{"1800", false},
		{"0000", false},
		{"abc", false},
		{"20", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.s, func(t *testing.T) {
			got := isYear(tc.s)
			if got != tc.want {
				t.Errorf("isYear(%q) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

func TestGenerateTrivialContentCanonical(t *testing.T) {
	tests := []struct {
		name   string
		target string
		desc   string
	}{
		{"lowercase license", "license", "create license"},
		{"uppercase LICENSE", "LICENSE", "create LICENSE"},
		{"lowercase gitignore", "gitignore", "create gitignore"},
		{"uppercase .GITIGNORE", ".GITIGNORE", "create .gitignore"},
		{"lowercase env", "env", "create env"},
		{"uppercase .ENV", ".ENV", "create .env"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := generateTrivialContent(tc.target, tc.desc)
			if got == "" {
				t.Errorf("expected non-empty content for target %q", tc.target)
			}
		})
	}
}

func TestRenderLicenseFallback(t *testing.T) {
	_, ok := templates.RenderLicense("unknown-xyz", "some description")
	if ok {
		t.Error("expected false for unknown license type")
	}
}

func TestSynthesizeBuildTodosFromMutation_StaticWebsite(t *testing.T) {
	content := "i want to create a static website with html css and js"
	todos := synthesizeBuildTodosFromMutation(content)
	if len(todos) != 3 {
		t.Fatalf("expected 3 todos, got %d", len(todos))
	}
	for _, todo := range todos {
		if !strings.Contains(todo, "[FILE_MUTATE]") {
			t.Errorf("expected FILE_MUTATE in todo, got: %s", todo)
		}
	}
	if !strings.Contains(todos[0], "index.html") {
		t.Errorf("expected first todo to reference index.html, got: %s", todos[0])
	}
	if !strings.Contains(todos[1], "styles.css") {
		t.Errorf("expected second todo to reference styles.css, got: %s", todos[1])
	}
	if !strings.Contains(todos[2], "script.js") {
		t.Errorf("expected third todo to reference script.js, got: %s", todos[2])
	}
}

func TestSynthesizeBuildTodosFromMutation_GenericFallback(t *testing.T) {
	content := "refactor the authentication module"
	todos := synthesizeBuildTodosFromMutation(content)
	if len(todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(todos))
	}
	if !strings.Contains(todos[0], "[FILE_MUTATE]") {
		t.Errorf("expected FILE_MUTATE in todo, got: %s", todos[0])
	}
	if !strings.Contains(todos[0], "refactor the authentication module") {
		t.Errorf("expected todo to contain original intent, got: %s", todos[0])
	}
	// MUST NOT contain "workspace" as a file target — this was the bug:
	// the placeholder would leak into the build parser as a literal file path.
	if strings.Contains(todos[0], "workspace") {
		t.Errorf("todo MUST NOT contain 'workspace' as target, got: %s", todos[0])
	}
}

func TestSynthesizeBuildTodosFromMutation_Empty(t *testing.T) {
	todos := synthesizeBuildTodosFromMutation("")
	if todos != nil {
		t.Errorf("expected nil for empty content, got %v", todos)
	}
	todos = synthesizeBuildTodosFromMutation("   ")
	if todos != nil {
		t.Errorf("expected nil for whitespace-only content, got %v", todos)
	}
}

func TestHasMutationIntent_FrontendUIGuard(t *testing.T) {
	cases := []struct {
		content string
		want    bool
	}{
		// UI creation / rewrite intents MUST NOT be treated as mutations —
		// "write" is a substring of "rewrite", which previously misrouted
		// these to /build instead of /plan.
		{"Please rewrite for me a personal profile website", false},
		{"rewrite my portfolio website", false},
		{"create a landing page with CSS", false},
		{"fix the layout of the homepage", false},
		// Genuine mutations still classify as such.
		{"add error handling to the payment service", true},
		{"implement the login handler", true},
		{"write unit tests for the parser", true},
	}
	for _, tc := range cases {
		if got := hasMutationIntent(tc.content); got != tc.want {
			t.Errorf("hasMutationIntent(%q) = %v, want %v", tc.content, got, tc.want)
		}
	}
}

func TestHasExecutableBuildTarget(t *testing.T) {
	m := &model{handoffCtx: HandoffContext{}}

	// No file paths, no pending todos → NOT ready for /build.
	if hasExecutableBuildTarget("implement the login handler", m) {
		t.Error("expected false for bare mutation intent with no file refs")
	}

	// Explicit @file path refs → ready for /build.
	if !hasExecutableBuildTarget("add error handling to @handler.go", m) {
		t.Error("expected true for mutation intent with explicit @file ref")
	}

	// Actionable pending todos staged (e.g. from /plan approval) → ready.
	m.handoffCtx.PendingTodos = []string{"[FILE_MUTATE] handler.go — add error handling"}
	if !hasExecutableBuildTarget("implement the login handler", m) {
		t.Error("expected true when actionable pending todos exist")
	}
}

func TestBuildMutationHandoffPayload(t *testing.T) {
	todos := []string{
		"\uf05c [FILE_MUTATE] index.html — Create main HTML page",
		"\uf05c [FILE_MUTATE] styles.css — Create responsive stylesheet",
	}
	payload := buildMutationHandoffPayload(todos)
	if payload == "" {
		t.Fatal("expected non-empty payload")
	}
	if !strings.Contains(payload, "MUTATION HANDOFF") {
		t.Errorf("payload missing MUTATION HANDOFF header")
	}
	if !strings.Contains(payload, "BEGIN EXECUTION NOW") {
		t.Errorf("payload missing execution directive")
	}
	if strings.Contains(payload, "[FILE_MUTATE]") {
		t.Errorf("payload should strip icon prefixes from todo display")
	}
	if strings.Contains(payload, "\uf05c") {
		t.Errorf("payload should not contain raw icon characters")
	}
	// Verify task numbering.
	if !strings.Contains(payload, "Task 1:") {
		t.Errorf("payload missing Task 1")
	}
	if !strings.Contains(payload, "Task 2:") {
		t.Errorf("payload missing Task 2")
	}
}

func TestBuildMutationHandoffPayload_Empty(t *testing.T) {
	payload := buildMutationHandoffPayload(nil)
	if payload != "" {
		t.Errorf("expected empty payload for nil todos, got %q", payload)
	}
	payload = buildMutationHandoffPayload([]string{})
	if payload != "" {
		t.Errorf("expected empty payload for empty todos, got %q", payload)
	}
}

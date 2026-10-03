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

func TestRenderLicenseFallback(t *testing.T) {
	_, ok := templates.RenderLicense("unknown-xyz", "some description")
	if ok {
		t.Error("expected false for unknown license type")
	}
}

// TestSynthesizeBuildTodosFromMutation_NoHardcodedFilenames pins the §13 boundary.
//
// This test used to REQUIRE that a "static website" objective produce three tasks
// naming index.html, styles.css and script.js. That is `html -> index.html` target
// mapping: benchmark-specific intelligence baked into production presentation code,
// guessing a workspace layout from an English phrase. It has been removed, and this
// test now asserts its absence for every wording — including the exact wording that
// used to trigger it.
func TestSynthesizeBuildTodosFromMutation_NoHardcodedFilenames(t *testing.T) {
	markupWordings := []string{
		"i want to create a static website with html css and js",
		"build a static website",
		"html css js",
		"redesign the portfolio website including styles and scripts",
		"create index.html, styles.css and script.js",
	}
	forbidden := []string{"index.html", "styles.css", "script.js", "style.css"}

	for _, content := range markupWordings {
		todos := synthesizeBuildTodosFromMutation(content)
		if len(todos) != 1 {
			t.Fatalf("%q: expected exactly one domain-neutral task, got %d: %v", content, len(todos), todos)
		}
		if !strings.Contains(todos[0], "[FILE_MUTATE]") {
			t.Errorf("%q: expected FILE_MUTATE in task, got: %s", content, todos[0])
		}
		if !strings.Contains(todos[0], content) {
			t.Errorf("%q: task must carry the original intent, got: %s", content, todos[0])
		}
		// The runtime's OWN prefix must never name a file. The objective text is
		// echoed verbatim — if the HUMAN wrote "index.html", that is their target,
		// not an inference — so only the runtime-authored portion is asserted on.
		prefix := strings.TrimSuffix(todos[0], content)
		for _, f := range forbidden {
			if strings.Contains(prefix, f) {
				t.Errorf("%q: runtime must never infer the target filename %q from the objective wording; prefix: %q",
					content, f, prefix)
			}
		}
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

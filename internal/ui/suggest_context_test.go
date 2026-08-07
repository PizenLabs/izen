package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/parser"
	"github.com/PizenLabs/izen/pkg/domain/command"
)

// ── CursorContext analyzer ───────────────────────────────────────────────

func TestAnalyzeCursor(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		cursor   int
		marker   rune
		partial  string
		hasToken bool
	}{
		{"empty input", "", 0, 0, "", false},
		{"bare word", "fix", 3, 0, "", false},
		{"slash complete at start", "/build", 6, '/', "build", true},
		{"slash partial", "/bu", 3, '/', "bu", true},
		{"dollar directly after slash chain", "/build$", 7, '$', "", true},
		{"dollar partial after chain", "/build$ho", 9, '$', "ho", true},
		{"at scope complete", "fix deadlock @auth.go", 21, '@', "auth.go", true},
		{"at scope with path separator", "fix @internal/au", 16, '@', "internal/au", true},
		{"slash anywhere in line", "fix /bu", 7, '/', "bu", true},
		{"word after completed commands is not a marker", "/build $hot fix deadl", 21, 0, "", false},
		{"trailing space past slash token", "fix /build ", 11, 0, "", false},
		{"trailing space past dollar token", "/build$ ", 8, 0, "", false},
		{"cursor mid workspace token", "/buil", 4, '/', "bui", true},
		{"at scope mid path", "@internal/", 10, '@', "internal/", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := analyzeCursor(tc.input, tc.cursor)
			if ctx.ActiveMarker != tc.marker {
				t.Errorf("ActiveMarker = %q, want %q", ctx.ActiveMarker, tc.marker)
			}
			if ctx.PartialTerm != tc.partial {
				t.Errorf("PartialTerm = %q, want %q", ctx.PartialTerm, tc.partial)
			}
		})
	}
}

// ── Workspace resolution ──────────────────────────────────────────────────

func TestWorkspaceFromInput(t *testing.T) {
	tests := []struct {
		input string
		want  command.WorkspaceType
		ok    bool
	}{
		{"/build$", command.WorkspaceBuild, true},
		{"/build $hot fix x", command.WorkspaceBuild, true},
		{"fix /plan $env", command.WorkspacePlan, true},
		{"$hot", 0, false},
		{"/help $env", 0, false}, // global command selects no workspace
		{"", 0, false},
	}
	for _, tc := range tests {
		got, ok := workspaceFromInput(tc.input, command.Default())
		if got != tc.want || ok != tc.ok {
			t.Errorf("workspaceFromInput(%q) = %v, %v; want %v, %v", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestActiveWorkspaceFor(t *testing.T) {
	build := &model{resolver: modes.NewResolver()}
	build.resolver.Set(modes.ModeBuild)

	ask := &model{resolver: modes.NewResolver()}
	ask.resolver.Set(modes.ModeAsk)

	if got := build.activeWorkspaceFor("/ask$"); got != command.WorkspaceAsk {
		t.Errorf("line-declared workspace should win; got %v, want ask", got)
	}
	if got := build.activeWorkspaceFor("$"); got != command.WorkspaceBuild {
		t.Errorf("session fallback should be build; got %v", got)
	}
	if got := build.activeWorkspaceFor("/build$"); got != command.WorkspaceBuild {
		t.Errorf("/build$ should resolve to build; got %v", got)
	}
	if got := ask.activeWorkspaceFor("$ho"); got != command.WorkspaceAsk {
		t.Errorf("session fallback should be ask; got %v", got)
	}
}

// ── Registry-driven projections ───────────────────────────────────────────

func TestProjectSlashSuggestions(t *testing.T) {
	items := projectSlashSuggestions(command.Default(), command.WorkspaceBuild, "")
	if len(items) < 15 {
		t.Fatalf("slash projection returned only %d items", len(items))
	}
	var sawWorkspace, sawGlobal bool
	for _, it := range items {
		if !strings.HasPrefix(it.Token, "/") {
			t.Errorf("token %q must carry the '/' marker", it.Token)
		}
		switch it.Kind {
		case SuggestionWorkspace:
			sawWorkspace = true
		case SuggestionGlobal:
			sawGlobal = true
		}
	}
	if !sawWorkspace || !sawGlobal {
		t.Errorf("projection must include both workspaces and globals (ws=%v global=%v)", sawWorkspace, sawGlobal)
	}

	// Workspace contexts precede global commands.
	if items[0].Kind != SuggestionWorkspace {
		t.Errorf("first item %q should be a workspace context", items[0].Label)
	}

	byPrefix := func(p string) []string {
		out := make([]string, 0, 4)
		for _, it := range projectSlashSuggestions(command.Default(), command.WorkspaceBuild, p) {
			out = append(out, it.Token)
		}
		return out
	}
	if got := byPrefix("bu"); len(got) != 1 || got[0] != "/build" {
		t.Errorf("prefix 'bu' = %v, want [/build]", got)
	}
	if got := byPrefix("hel"); len(got) != 1 || got[0] != "/help" {
		t.Errorf("prefix 'hel' = %v, want [/help]", got)
	}
}

// TestProjectSlashSuggestionsAskHidesMutation verifies the primary DoD: in the
// read-only ask workspace the '/' menu must expose workspace switchers and
// read-only globals but hide /undo, /commit, and /checkpoint entirely.
func TestProjectSlashSuggestionsAskHidesMutation(t *testing.T) {
	items := projectSlashSuggestions(command.Default(), command.WorkspaceAsk, "")
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.Token] = true
	}
	for _, forbidden := range []string{"/undo", "/commit", "/checkpoint"} {
		if seen[forbidden] {
			t.Errorf("ask '/' menu must not show %s", forbidden)
		}
	}
	for _, present := range []string{"/ask", "/plan", "/build", "/investigate", "/review", "/arch", "/clear", "/drop", "/usage", "/model", "/help"} {
		if !seen[present] {
			got := make([]string, 0, len(seen))
			for k := range seen {
				got = append(got, k)
			}
			t.Errorf("ask '/' menu must show %s, got %v", present, got)
		}
	}
}

// TestProjectSlashSuggestionsBuildExposesMutation verifies the flip side: the
// build workspace grants the mutation globals.
func TestProjectSlashSuggestionsBuildExposesMutation(t *testing.T) {
	items := projectSlashSuggestions(command.Default(), command.WorkspaceBuild, "")
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.Token] = true
	}
	for _, present := range []string{"/undo", "/commit", "/checkpoint"} {
		if !seen[present] {
			t.Errorf("build '/' menu must show %s", present)
		}
	}
}

func TestProjectDollarSuggestionsBuild(t *testing.T) {
	items := projectDollarSuggestions(command.Default(), command.WorkspaceBuild, "")
	got := map[string]bool{}
	for _, it := range items {
		if it.Kind != SuggestionDirective {
			t.Errorf("item %q is not a directive", it.Label)
		}
		if strings.Contains(it.Token, " ") {
			t.Errorf("directive %q must be base-only, never a flag string", it.Token)
		}
		if !strings.HasPrefix(it.Token, "$") {
			t.Errorf("token %q must carry the '$' marker", it.Token)
		}
		got[it.Token] = true
	}
	for _, want := range []string{"$hot", "$fix", "$test", "$trace", "$prompt", "$env", "$run", "$diagnose"} {
		if !got[want] {
			t.Errorf("build directive projection missing %s", want)
		}
	}
}

func TestProjectDollarSuggestionsAskIsReadOnly(t *testing.T) {
	items := projectDollarSuggestions(command.Default(), command.WorkspaceAsk, "")
	if len(items) != 2 {
		t.Fatalf("ask projection returned %d items, want exactly 2 read-only directives", len(items))
	}
	got := []string{items[0].Token, items[1].Token}
	if !equalSlices(got, []string{"$env", "$prompt"}) {
		t.Errorf("ask projection = %v, want [$env $prompt]", got)
	}
}

func TestProjectAtSuggestionsPrioritizesModified(t *testing.T) {
	modified := []string{"internal/auth.go", "cmd/main.go"}
	items := projectAtSuggestions("auth", modified)
	if len(items) == 0 {
		t.Fatal("no @ suggestions projected")
	}
	if items[0].Token != "@internal/auth.go" {
		t.Errorf("modified file should rank first; got %q", items[0].Token)
	}
	if items[0].Detail != "modified" {
		t.Errorf("modified file detail = %q, want modified", items[0].Detail)
	}
}

// ── Section grouping ──────────────────────────────────────────────────────

func TestBuildSuggestionSections(t *testing.T) {
	slash := projectSlashSuggestions(command.Default(), command.WorkspaceBuild, "")
	items := append(append([]Suggestion{}, slash...), projectDollarSuggestions(command.Default(), command.WorkspaceBuild, "")...)
	sections := buildSuggestionSections(items)

	indexOf := func(title string) int {
		for i, s := range sections {
			if s.Title == title {
				return i
			}
		}
		return -1
	}

	ws := indexOf("WORKSPACE CONTEXTS")
	globals := indexOf("GLOBAL COMMANDS")
	mutation := indexOf("MUTATION")
	validation := indexOf("VALIDATION")
	observation := indexOf("OBSERVATION")
	activation := indexOf("ACTIVATION")

	if ws < 0 || globals < 0 {
		t.Fatalf("missing workspace/global sections: %+v", sections)
	}
	if mutation < 0 || validation < 0 || observation < 0 || activation < 0 {
		t.Fatalf("missing directive category sections: %+v", sections)
	}
	if ws > globals {
		t.Errorf("workspace section must precede globals")
	}
	if mutation >= validation || validation >= observation || observation >= activation {
		t.Errorf("category sections must follow Mutation < Validation < Observation < Activation order")
	}
}

// ── updateSuggestions model integration ──────────────────────────────────

func TestUpdateSuggestionsSlash(t *testing.T) {
	m := newTestModel()
	m.ti.SetValue("/")
	m.ti.SetCursor(1)
	m.syncInputFromTI()
	m.updateSuggestions()

	if !m.showSuggestions {
		t.Fatal("typing '/' must open the menu")
	}
	if m.suggestionType != "command" {
		t.Errorf("suggestionType = %q, want command", m.suggestionType)
	}
	if len(m.suggestions) < 15 {
		t.Errorf("'/' projection returned %d items", len(m.suggestions))
	}
}

// TestUpdateSuggestionsSlashAskHidesMutation is the TUI-level DoD: typing '/'
// in the ask workspace must never surface /checkpoint or /undo.
func TestUpdateSuggestionsSlashAskHidesMutation(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)
	m.ti.SetValue("/")
	m.ti.SetCursor(1)
	m.syncInputFromTI()
	m.updateSuggestions()

	if !m.showSuggestions {
		t.Fatal("typing '/' in ask must open the menu")
	}
	seen := map[string]bool{}
	for _, s := range m.suggestions {
		seen[s.Token] = true
	}
	for _, forbidden := range []string{"/checkpoint", "/undo", "/commit"} {
		if seen[forbidden] {
			t.Errorf("ask '/' menu must not show %s", forbidden)
		}
	}
	for _, present := range []string{"/arch", "/clear", "/usage", "/model", "/help"} {
		if !seen[present] {
			t.Errorf("ask '/' menu must show %s", present)
		}
	}
}

func TestUpdateSuggestionsDollarBuild(t *testing.T) {
	m := newTestModel() // build mode
	m.ti.SetValue("/build$")
	m.ti.SetCursor(7)
	m.syncInputFromTI()
	m.updateSuggestions()

	if !m.showSuggestions {
		t.Fatal("'$' after /build must open the menu")
	}
	if m.suggestionType != "directive" {
		t.Errorf("suggestionType = %q, want directive", m.suggestionType)
	}
	labels := make([]string, 0, len(m.suggestions))
	for _, s := range m.suggestions {
		labels = append(labels, s.Token)
	}
	for _, want := range []string{"$hot", "$fix", "$test", "$trace"} {
		found := false
		for _, l := range labels {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("build '$' menu missing %s (got %v)", want, labels)
		}
	}
}

func TestUpdateSuggestionsDollarAskReadOnly(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)
	m.ti.SetValue("$")
	m.ti.SetCursor(1)
	m.syncInputFromTI()
	m.updateSuggestions()

	if !m.showSuggestions {
		t.Fatal("'$' in ask must open the menu")
	}
	labels := make([]string, 0, len(m.suggestions))
	for _, s := range m.suggestions {
		labels = append(labels, s.Token)
	}
	if !equalSlices(labels, []string{"$env", "$prompt"}) {
		t.Errorf("ask '$' menu = %v, want only read-compatible [$env $prompt]", labels)
	}
}

func TestUpdateSuggestionsAt(t *testing.T) {
	m := newTestModel()
	m.ti.SetValue("@")
	m.ti.SetCursor(1)
	m.syncInputFromTI()
	m.updateSuggestions()

	if !m.showSuggestions {
		t.Fatal("'@' must open the scope menu")
	}
	if m.suggestionType != "scope" {
		t.Errorf("suggestionType = %q, want scope", m.suggestionType)
	}
	if len(m.suggestions) == 0 {
		t.Fatal("scope menu projected no files")
	}
	for _, s := range m.suggestions {
		if !strings.HasPrefix(s.Token, "@") {
			t.Errorf("scope token %q must carry the '@' marker", s.Token)
		}
	}
}

func TestUpdateSuggestionsDismissesOnPlainWord(t *testing.T) {
	m := newTestModel()
	m.ti.SetValue("fix login timeout")
	m.ti.SetCursor(16)
	m.syncInputFromTI()
	m.updateSuggestions()

	if m.showSuggestions {
		t.Fatal("plain words must not open the menu")
	}
	if m.autocompleteActive {
		t.Fatal("autocomplete must stay inactive for plain words")
	}
}

// ── Insertion → parser consistency ────────────────────────────────────────

func TestCompleteAutocompleteInsertsTokenAtCursor(t *testing.T) {
	m := newTestModel()
	m.ti.SetValue("/build $")
	m.ti.SetCursor(8)
	m.syncInputFromTI()

	m.autocompleteActive = true
	m.autocompleteType = "directive"
	m.autocompleteItems = []Suggestion{{Token: "$hot", Label: "$hot", Kind: SuggestionDirective, Category: command.CategoryMutation.String()}}
	m.autocompleteIdx = 0

	if commit := m.completeAutocomplete(false); commit {
		t.Error("mid-sentence completion must not request an immediate submit")
	}

	got := m.ti.Value()
	if got != "/build $hot " {
		t.Errorf("inserted value = %q, want %q", got, "/build $hot ")
	}
	if pos := m.ti.Position(); pos != len("/build $hot ") {
		t.Errorf("cursor = %d, want %d", pos, len("/build $hot "))
	}
	if m.autocompleteActive {
		t.Error("autocomplete must close after insertion")
	}

	// Insertion Consistency: the composed line must parse cleanly through the
	// Phase 2 parser with the inserted directive resolved and permitted.
	ast, err := parser.Parse("/build $hot fix deadlock", command.Default())
	if err != nil {
		t.Fatalf("parsing composed line failed: %v", err)
	}
	if ast.Workspace != command.WorkspaceBuild {
		t.Errorf("workspace = %v, want build", ast.Workspace)
	}
	if len(ast.Directives) != 1 || ast.Directives[0].Name != "hot" {
		t.Errorf("directives = %+v, want [$hot]", ast.Directives)
	}
}

func TestCompleteAutocompleteScopeAttachesFile(t *testing.T) {
	m := newTestModel()
	m.ti.SetValue("inspect @auth")
	m.ti.SetCursor(13)
	m.syncInputFromTI()

	m.autocompleteActive = true
	m.autocompleteType = "scope"
	m.autocompleteItems = []Suggestion{{Token: "@auth.go", Label: "auth.go", Kind: SuggestionScope}}
	m.autocompleteIdx = 0

	if commit := m.completeAutocomplete(true); commit {
		t.Error("scope completion must never auto-submit")
	}

	if got := m.ti.Value(); got != "inspect @auth.go " {
		t.Errorf("inserted value = %q, want %q", got, "inspect @auth.go ")
	}
	if len(m.pendingFileRefs) != 1 || m.pendingFileRefs[0] != "auth.go" {
		t.Errorf("pendingFileRefs = %v, want [auth.go]", m.pendingFileRefs)
	}
	if len(m.attachedFiles) != 1 || m.attachedFiles[0] != "auth.go" {
		t.Errorf("attachedFiles = %v, want [auth.go]", m.attachedFiles)
	}
}

// ── Smart space + inline @ trigger + prefix completion ────────────────────

// TestCompleteAutocompleteAppendsTrailingSpace is the DoD smart-space case:
// completing @main inside a sentence leaves a trailing space so the user can
// keep typing without tokens sticking together.
func TestCompleteAutocompleteAppendsTrailingSpace(t *testing.T) {
	m := newTestModel()
	m.ti.SetValue("$fix check in @main")
	m.ti.SetCursor(len("$fix check in @main"))
	m.syncInputFromTI()

	m.autocompleteActive = true
	m.autocompleteType = "scope"
	m.autocompleteItems = []Suggestion{{Token: "@main.go", Label: "main.go", Kind: SuggestionScope}}
	m.autocompleteIdx = 0

	if commit := m.completeAutocomplete(true); commit {
		t.Error("sentence-scope completion must not auto-submit")
	}
	if got := m.ti.Value(); got != "$fix check in @main.go " {
		t.Errorf("inserted value = %q, want %q", got, "$fix check in @main.go ")
	}
	if pos := m.ti.Position(); pos != len("$fix check in @main.go ") {
		t.Errorf("cursor = %d, want %d", pos, len("$fix check in @main.go "))
	}
}

// TestCompleteAutocompleteWholeLineCommit is the DoD fast-execution case: a
// unique whole-line "/q" completion commits to "/quit" (no trailing space) and
// reports the caller should submit immediately.
func TestCompleteAutocompleteWholeLineCommit(t *testing.T) {
	m := newTestModel()
	m.ti.SetValue("/q")
	m.ti.SetCursor(2)
	m.syncInputFromTI()

	m.autocompleteActive = true
	m.autocompleteType = "command"
	m.autocompleteItems = []Suggestion{{Token: "/quit", Label: "/quit", Kind: SuggestionGlobal}}
	m.autocompleteIdx = 0

	if !m.completeAutocomplete(true) {
		t.Error("whole-line command completion must request an immediate submit")
	}
	if got := m.ti.Value(); got != "/quit" {
		t.Errorf("committed value = %q, want %q", got, "/quit")
	}
}

// TestCompleteAutocompleteTabKeepsTrailingSpace verifies Tab completion of a
// whole-line token keeps the trailing space (the user may keep typing) and
// never auto-submits.
func TestCompleteAutocompleteTabKeepsTrailingSpace(t *testing.T) {
	m := newTestModel()
	m.ti.SetValue("/q")
	m.ti.SetCursor(2)
	m.syncInputFromTI()

	m.autocompleteActive = true
	m.autocompleteType = "command"
	m.autocompleteItems = []Suggestion{{Token: "/quit", Label: "/quit", Kind: SuggestionGlobal}}
	m.autocompleteIdx = 0

	if commit := m.completeAutocomplete(false); commit {
		t.Error("Tab completion must never auto-submit")
	}
	if got := m.ti.Value(); got != "/quit " {
		t.Errorf("tab-completed value = %q, want %q", got, "/quit ")
	}
}

// TestCompleteAutocompleteAmbiguousDoesNotCommit verifies a whole-line prefix
// with multiple candidate suggestions ("/u" → undo/usage) never auto-submits:
// Enter completes the highlighted token with a trailing space instead.
func TestCompleteAutocompleteAmbiguousDoesNotCommit(t *testing.T) {
	m := newTestModel()
	m.ti.SetValue("/u")
	m.ti.SetCursor(2)
	m.syncInputFromTI()

	m.autocompleteActive = true
	m.autocompleteType = "command"
	m.autocompleteItems = []Suggestion{
		{Token: "/undo", Label: "/undo", Kind: SuggestionGlobal},
		{Token: "/usage", Label: "/usage", Kind: SuggestionGlobal},
	}
	m.autocompleteIdx = 0

	if commit := m.completeAutocomplete(true); commit {
		t.Error("ambiguous whole-line prefix must not auto-submit")
	}
	if got := m.ti.Value(); got != "/undo " {
		t.Errorf("completed value = %q, want %q", got, "/undo ")
	}
}

// TestUpdateSuggestionsInlineAtTrigger verifies typing @ mid-sentence (not at
// the start of the line) opens the scope menu at the caret.
func TestUpdateSuggestionsInlineAtTrigger(t *testing.T) {
	m := newTestModel()
	m.ti.SetValue("$fix check in @sugg")
	m.ti.SetCursor(len("$fix check in @sugg"))
	m.syncInputFromTI()
	m.updateSuggestions()

	if !m.showSuggestions {
		t.Fatal("inline @ mid-sentence must open the scope menu")
	}
	if m.suggestionType != "scope" {
		t.Errorf("suggestionType = %q, want scope", m.suggestionType)
	}
}

// TestProjectSlashSuggestionsQuitPrefix verifies typing "/q" surfaces only
// /quit in the '/' menu.
func TestProjectSlashSuggestionsQuitPrefix(t *testing.T) {
	items := projectSlashSuggestions(command.Default(), command.WorkspaceBuild, "q")
	if len(items) != 1 || items[0].Token != "/quit" {
		t.Errorf("prefix 'q' = %v, want [/quit]", labels(items))
	}
}

// TestResolveCommandToken verifies handleCommand's alias/prefix resolution:
// "/q" canonicalizes to "/quit", unambiguous prefixes resolve, and ambiguous
// or unknown tokens pass through unchanged.
func TestResolveCommandToken(t *testing.T) {
	tests := []struct {
		token string
		want  string
	}{
		{"/q", "/quit"},      // registered alias
		{"/qu", "/quit"},     // unambiguous prefix
		{"/hel", "/help"},    // unambiguous prefix
		{"/help", "/help"},   // already valid
		{"/?", "/?"},         // already valid
		{"/u", "/u"},         // ambiguous (undo + usage)
		{"/bogus", "/bogus"}, // unknown
	}
	for _, tc := range tests {
		if got := resolveCommandToken(tc.token); got != tc.want {
			t.Errorf("resolveCommandToken(%q) = %q, want %q", tc.token, got, tc.want)
		}
	}
}

// TestSubmitEnterQuitOpensConfirm is the fast-execution flow: submitting a
// completed "/quit" through the canonical Enter path must open the exit-safety
// modal (input cleared, shutdown deferred) instead of dumping "unknown
// command" or exiting without confirmation.
func TestSubmitEnterQuitOpensConfirm(t *testing.T) {
	m := newTestModel()
	m.ti.SetValue("/quit")
	m.ti.SetCursor(len("/quit"))
	m.syncInputFromTI()

	m.submitEnter()
	if !m.pendingQuitConfirm {
		t.Fatal("submitEnter(/quit) must open the quit-confirm modal")
	}
	if m.quitConfirmYes {
		t.Error("submitEnter(/quit) must default focus to [ No ]")
	}
	if m.ti.Value() != "" {
		t.Errorf("input buffer not cleared after submit, got %q", m.ti.Value())
	}
}

// TestRenderAutocompleteDropdownIndentation verifies the visual hierarchy:
// section headers render with bold + distinct-color styling, and items are
// indented 2 spaces under their header (the "▶ " cursor is followed by
// 2-space indentation).
func TestRenderAutocompleteDropdownIndentation(t *testing.T) {
	m := newTestModel()
	m.autocompleteActive = true
	m.autocompleteType = "command"
	m.autocompleteItems = projectSlashSuggestions(command.Default(), command.WorkspaceBuild, "")
	m.autocompleteIdx = 0

	// Force a true-color profile so lipgloss emits real ANSI SGR attributes
	// (tests otherwise render plain text on a non-TTY). Restored after.
	prevProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prevProfile)

	out := m.renderAutocompleteDropdown(80)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var sawStyledHeader, sawIndented bool
	for _, ln := range lines {
		if strings.Contains(ln, "WORKSPACE CONTEXTS") {
			if !strings.Contains(ln, "\x1b[") {
				t.Error("WORKSPACE CONTEXTS header must render with distinct styling (bold + color)")
			}
			sawStyledHeader = true
		}
		// Active row renders as "│ ▶   /ask ..." — cursor, then 2-space indent.
		if strings.Contains(ln, "▶   /ask") {
			sawIndented = true
		}
	}
	if !sawStyledHeader {
		t.Error("dropdown missing WORKSPACE CONTEXTS header")
	}
	if !sawIndented {
		t.Error("dropdown items must be indented 2 spaces under their header (e.g. '  ▶ /ask')")
	}
}

func labels(items []Suggestion) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Token)
	}
	return out
}

// ── Dropdown rendering smoke ─────────────────────────────────────────────

func TestRenderAutocompleteDropdownSmoke(t *testing.T) {
	m := newTestModel()
	m.autocompleteActive = true
	m.autocompleteIdx = 0

	cases := []struct {
		name  string
		typ   string
		items []Suggestion
	}{
		{
			name:  "slash command menu",
			typ:   "command",
			items: projectSlashSuggestions(command.Default(), command.WorkspaceBuild, ""),
		},
		{
			name:  "dollar directive menu",
			typ:   "directive",
			items: projectDollarSuggestions(command.Default(), command.WorkspaceBuild, ""),
		},
		{
			name:  "scope file menu",
			typ:   "scope",
			items: []Suggestion{{Token: "@auth.go", Label: "auth.go", Kind: SuggestionScope}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m.autocompleteType = tc.typ
			m.autocompleteItems = tc.items
			out := m.renderAutocompleteDropdown(80)
			if out == "" {
				t.Fatal("dropdown rendered empty")
			}
			if !strings.Contains(out, "Context Selection") {
				t.Error("dropdown missing title")
			}
			if h := m.getAutocompleteHeight(); h < 3 {
				t.Errorf("getAutocompleteHeight = %d, want >= 3", h)
			}
		})
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

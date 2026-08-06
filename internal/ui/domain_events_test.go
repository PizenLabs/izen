package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/retrieval"
)

func TestHandleDomainEventProjection(t *testing.T) {
	tests := []struct {
		name string
		ev   events.DomainEvent
		want string
	}{
		{"command received", events.NewCommandReceived("refactor LICENSE", "plan"),
			"[plan] received command: refactor LICENSE"},
		{"intent parsed", events.NewIntentParsed("direct_mutation", "refactor LICENSE", 1.0),
			"[intent] parsed: direct_mutation (100% confidence)"},
		{"plan staged", events.NewPlanStaged(3, []string{"a", "b", "c"}, "plan"),
			"[plan] staged 3 tasks"},
		{"patch attempted", events.NewPatchAttempted("x.go", "ATOMIC_REPLACE", 2),
			"[build] patch attempt 2: x.go (ATOMIC_REPLACE)"},
		{"patch applied", events.NewPatchApplied("x.go", 12, 4, 350*time.Millisecond),
			"[build] applied patch to x.go (+12/-4 lines)"},
		{"execution failed", events.NewExecutionFailed(events.FailureRecoverable, errors.New("boom"), "build.compilation"),
			"[error][recoverable] boom (stage: build.compilation)"},
		{"self-healing attempt", events.NewSelfHealingAttempt(2, "worker.go", "TYPE_MISMATCH"),
			"[RETRY 2] [TYPE_MISMATCH] worker.go"},
		{"self-healing exhausted", events.NewSelfHealingExhausted(4, "./x.go:5: undefined: foo"),
			"[EXHAUSTED] self-healing stopped after 4 attempt(s); workspace rolled back clean — ./x.go:5: undefined: foo"},
		{"self-healing exhausted (empty output)", events.NewSelfHealingExhausted(3, "  \n\t\n"),
			"[EXHAUSTED] self-healing stopped after 3 attempt(s); workspace rolled back clean"},
		{"stage completed", events.NewStageCompleted("review", 5*time.Millisecond, "ok"),
			"[stage] review completed (ok)"},
		{"engine activity", events.NewActivity("[ OK ] search \"query\": 3 results"),
			"[ OK ] search \"query\": 3 results"},
		{"engine activity (multiline escapes)", events.NewActivity("step 1\\nstep 2\\tvalue"),
			"step 1\\nstep 2\\tvalue"},
		{"intent classified", events.NewIntentClassified("build", "write a fix", 0.91, "en", "code mutation", false),
			"[intent] classified: /build (91%, code mutation)"},
		{"intent classified (ambiguous)", events.NewIntentClassified("plan", "what should we do", 0.42, "en", "ambiguous request", true),
			"[intent] ambiguous: /plan (42%, ambiguous request) — asking user"},
		{"phase changed", events.NewPhaseChanged("plan", "build"),
			"[phase] plan → build"},
		{"patch parsed", events.NewPatchParsed("x.go", "STRUCTURED_DIFF", 1),
			"[patch] parsed x.go (strategy=STRUCTURED_DIFF, tier=1)"},
		{"patch validated", events.NewPatchValidated("x.go", "SEARCH_REPLACE", 2),
			"[patch] validated x.go (strategy=SEARCH_REPLACE, tier=2)"},
		{"patch rejected", events.NewPatchRejected("x.go", "unsafe full rewrite", 3),
			"[patch] rejected x.go (tier 3): unsafe full rewrite"},
		{"approval requested (tier 4)", events.NewApprovalRequested("x.go", "full-file rewrite needs approval", ""),
			"[approval] requested for x.go: full-file rewrite needs approval"},
		{"approval requested (intent disambiguation)", events.NewApprovalRequested("", "unclear intent", ""),
			"[approval] requested for intent disambiguation: unclear intent"},
		{"stream usage interrupted", events.NewStreamUsage("cohere/north-mini-code", 512, 240, true, "context deadline exceeded"),
			"[stream] interrupted: 512 tok input + 240 tok output (context deadline exceeded)"},
		{"stream usage clean", events.NewStreamUsage("gpt-4o-mini", 100, 200, false, ""),
			"[stream] finished: 100 tok input + 200 tok output ()"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &model{}
			m.handleDomainEvent(tc.ev)

			if len(m.records) != 1 {
				t.Fatalf("got %d records, want 1", len(m.records))
			}
			if got := m.records[0].text; got != tc.want {
				t.Errorf("record = %q, want %q", got, tc.want)
			}
			if m.records[0].role != roleActivity {
				t.Errorf("record role = %v, want roleActivity", m.records[0].role)
			}
		})
	}
}

func TestHandleDomainEventPatchAppliedAppendsActivityTree(t *testing.T) {
	m := &model{activityTree: NewActivityTree()}
	m.handleDomainEvent(events.NewPatchApplied("x.go", 12, 4, 350*time.Millisecond))

	entries := m.activityTree.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d tree entries, want 1", len(entries))
	}
	ev := entries[0]
	if ev.Kind != EventFileMutate {
		t.Errorf("entry kind = %v, want EventFileMutate", ev.Kind)
	}
	if ev.FileMutate == nil {
		t.Fatal("FileMutate is nil")
	}
	if ev.FileMutate.File != "x.go" || ev.FileMutate.LinesAdd != 12 || ev.FileMutate.LinesDel != 4 {
		t.Errorf("FileMutate = %+v", ev.FileMutate)
	}
}

func TestHandleDomainEventNilIsNoop(t *testing.T) {
	m := &model{activityTree: NewActivityTree()}
	m.handleDomainEvent(nil)
	if len(m.records) != 0 {
		t.Errorf("got %d records for nil event, want 0", len(m.records))
	}
	if m.activityTree.Len() != 0 {
		t.Errorf("got %d tree entries for nil event, want 0", m.activityTree.Len())
	}
}

func TestTruncateForActivity(t *testing.T) {
	short := "hello world"
	if got := truncateForActivity(short); got != short {
		t.Errorf("short input = %q, want unchanged", got)
	}
	long := strings.Repeat("x", 200)
	got := truncateForActivity(long)
	if len(got) != 90 {
		t.Errorf("long input truncated to %d chars, want 90", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated output missing ellipsis: %q", got)
	}
	whitespace := "   padded   "
	if got := truncateForActivity(whitespace); got != "padded" {
		t.Errorf("whitespace input = %q, want trimmed", got)
	}
}

func TestHandleDomainEventEngineTelemetryProjectsToTree(t *testing.T) {
	m := &model{activityTree: NewActivityTree()}

	// retrieval.SearchEvent wrapped on the bus must land in the ActivityTree
	// exactly like the old direct callback path.
	m.handleDomainEvent(events.NewEngineTelemetry(retrieval.SearchEvent{Query: "foo", Hits: 7}))

	entries := m.activityTree.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d tree entries, want 1", len(entries))
	}
	ev := entries[0]
	if ev.Kind != EventSearch {
		t.Errorf("entry kind = %v, want EventSearch", ev.Kind)
	}
	if ev.Search == nil || ev.Search.Query != "foo" || ev.Search.Hits != 7 {
		t.Errorf("Search = %+v", ev.Search)
	}
	if len(m.records) != 0 {
		t.Errorf("got %d records for telemetry event, want 0", len(m.records))
	}
}

func TestHandleDomainEventEngineTelemetryFileMutate(t *testing.T) {
	m := &model{activityTree: NewActivityTree()}
	m.handleDomainEvent(events.NewEngineTelemetry(execution.FileMutateEvent{
		File:     "x.go",
		LinesAdd: 3,
		LinesDel: 1,
		Elapsed:  12 * time.Millisecond,
	}))

	entries := m.activityTree.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d tree entries, want 1", len(entries))
	}
	if entries[0].Kind != EventFileMutate || entries[0].FileMutate == nil {
		t.Fatalf("entry = %+v, want EventFileMutate", entries[0])
	}
	if entries[0].FileMutate.File != "x.go" || entries[0].FileMutate.LinesAdd != 3 {
		t.Errorf("FileMutate = %+v", entries[0].FileMutate)
	}
}

func TestStyleActivityLineSelfHealingBadges(t *testing.T) {
	m := &model{width: 100}

	// Retry badge line: badge + category + file, all preserved after styling.
	attempt := m.styleActivityLine("[RETRY 2] [TYPE_MISMATCH] worker.go")
	if !strings.Contains(attempt, "[RETRY 2]") {
		t.Errorf("retry badge missing from %q", attempt)
	}
	if !strings.Contains(attempt, "[TYPE_MISMATCH]") {
		t.Errorf("category badge missing from %q", attempt)
	}
	if !strings.Contains(attempt, "worker.go") {
		t.Errorf("file missing from %q", attempt)
	}
	if stripped := ansi.Strip(attempt); stripped != "[RETRY 2] [TYPE_MISMATCH] worker.go" {
		t.Errorf("stripped attempt = %q", stripped)
	}

	// Exhausted badge line.
	exhausted := m.styleActivityLine("[EXHAUSTED] self-healing stopped after 4 attempt(s); workspace rolled back clean")
	if !strings.Contains(exhausted, "[EXHAUSTED]") {
		t.Errorf("exhausted badge missing from %q", exhausted)
	}
	if stripped := ansi.Strip(exhausted); !strings.Contains(stripped, "after 4 attempt(s)") {
		t.Errorf("stripped exhausted = %q", stripped)
	}

	// Ordinary activity lines keep the markdown pipeline.
	ok := m.styleActivityLine("[ OK ] search: found 3 results")
	if !strings.Contains(ok, "[ OK ]") {
		t.Errorf("OK badge missing from %q", ok)
	}
}

func TestFailureCategoryStyle(t *testing.T) {
	for _, cat := range []string{"SYNTAX_ERROR", "TYPE_MISMATCH", "MISSING_IMPORT", "TEST_FAILURE", "SYSTEM_PERMISSION", "UNKNOWN", "whatever"} {
		// Every category must yield a renderable (non-empty) style.
		if got := failureCategoryStyle(cat).Render("[" + cat + "]"); got == "" {
			t.Errorf("failureCategoryStyle(%q) produced empty render", cat)
		}
	}
}

func TestSanitizeEscapes(t *testing.T) {
	got := sanitizeEscapes(`line1\nline2\tvalue`)
	if !strings.Contains(got, "\n") || !strings.Contains(got, "\t") {
		t.Errorf("sanitizeEscapes = %q, want real \\n and \\t", got)
	}
	if strings.Contains(got, "\\n") || strings.Contains(got, "\\t") {
		t.Errorf("sanitizeEscapes left raw escapes: %q", got)
	}
	// No backslashes — unchanged.
	plain := "hello world"
	if got := sanitizeEscapes(plain); got != plain {
		t.Errorf("plain input = %q, want unchanged", got)
	}
}

func TestSelfHealOutputSuffix(t *testing.T) {
	if got := selfHealOutputSuffix(""); got != "" {
		t.Errorf("empty output = %q, want empty", got)
	}
	if got := selfHealOutputSuffix("  \n\t\n"); got != "" {
		t.Errorf("whitespace output = %q, want empty", got)
	}
	got := selfHealOutputSuffix("a.go:5:2: undefined: foo\nnext line")
	if !strings.HasPrefix(got, " — ") {
		t.Errorf("suffix = %q, want leading separator", got)
	}
	if !strings.Contains(got, "undefined: foo") {
		t.Errorf("suffix = %q, want first output line", got)
	}
	// Literal escape sequences are sanitized before collapsing; the first
	// meaningful line after conversion wins.
	esc := selfHealOutputSuffix("a.go:2:\\n  syntax error")
	if !strings.HasPrefix(esc, " — ") || !strings.Contains(esc, "a.go:2:") {
		t.Errorf("escaped suffix = %q, want first collapsed line", esc)
	}
	// Long lines are truncated.
	long := selfHealOutputSuffix(strings.Repeat("x", 200))
	if len(long) > 70 {
		t.Errorf("suffix too long: %d", len(long))
	}
}

func TestWrapIndentedLinePreservesIndent(t *testing.T) {
	// Wide text with a leading indent must wrap onto continuation lines that
	// keep the same indent — never collapse onto the margin.
	line := "      " + strings.Repeat("word ", 40)
	wrapped := wrapIndentedLine(line, 40)
	if len(wrapped) < 2 {
		t.Fatalf("expected multiple wrapped lines, got %d", len(wrapped))
	}
	for i, wl := range wrapped {
		if !strings.HasPrefix(wl, "      ") {
			t.Errorf("line %d lost indent: %q", i, wl)
		}
		if lipgloss.Width(wl) > 40 {
			t.Errorf("line %d exceeds width: %q (%d cells)", i, wl, lipgloss.Width(wl))
		}
	}

	// No-indent short text returns a single line.
	if got := wrapIndentedLine("hi", 40); len(got) != 1 || got[0] != "hi" {
		t.Errorf("short text = %v, want [hi]", got)
	}
	// Blank line keeps its (empty) prefix.
	if got := wrapIndentedLine("", 40); len(got) != 1 || got[0] != "" {
		t.Errorf("blank = %v, want empty line", got)
	}
	// Whitespace-only line keeps the whitespace prefix.
	if got := wrapIndentedLine("   ", 40); len(got) != 1 || got[0] != "   " {
		t.Errorf("whitespace = %v, want [   ]", got)
	}
	// Overlong unbreakable token is hard-chunked under the width.
	token := strings.Repeat("z", 120)
	for _, wl := range wrapIndentedLine(token, 30) {
		if lipgloss.Width(wl) > 30 {
			t.Errorf("hard-chunked line exceeds width: %q (%d)", wl, lipgloss.Width(wl))
		}
	}
}

func TestTruncateMiddle(t *testing.T) {
	short := "a/b/c.go"
	if got := truncateMiddle(short, 30); got != short {
		t.Errorf("short = %q, want unchanged", got)
	}
	long := "very/long/path/with/many/segments/worker.go"
	got := truncateMiddle(long, 30)
	if len(got) >= len(long) {
		t.Errorf("truncateMiddle did not shorten: %q", got)
	}
	if !strings.Contains(got, "...") {
		t.Errorf("truncateMiddle missing ellipsis: %q", got)
	}
	if lipgloss.Width(got) > 30 {
		t.Errorf("truncated length %d exceeds 30", lipgloss.Width(got))
	}
}

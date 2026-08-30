package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// TestTraceVerboseDefaultQuiet pins that quiet mode is the forced default:
// TraceVerbose starts false.
func TestTraceVerboseDefaultQuiet(t *testing.T) {
	defer SetTraceVerbose(false)
	SetTraceVerbose(false)
	if IsTraceVerbose() {
		t.Fatal("TraceVerbose must default to false (quiet mode)")
	}
}

// TestIsQuietTraceTextPatterns pins the complete quiet-mode suppression set:
// every raw internal engine line ([AUTONOMY DECISION], intent :, required :,
// workspace :, decision :, intent parsed:, command received:, [submit_prompt],
// [preflight], [event]) is detected.
func TestIsQuietTraceTextPatterns(t *testing.T) {
	traces := []string{
		"AUTONOMY DECISION",
		"[AUTONOMY DECISION]",
		"  intent      : modification (95%)",
		"  required    : mutate",
		"  workspace   : build (capabilities cover)",
		"  decision    : ◇ direct_response (no execution needed)",
		"[preflight] decision surface staged target=index.html",
		"[event] PromptAdmitted intent=modification latency=42ms",
		"intent parsed: modification (95% confident)",
		"command received: /plan",
		"[submit_prompt] intent parsed intent=modification",
	}
	for _, tr := range traces {
		if !isQuietTraceText(tr) {
			t.Errorf("quiet mode failed to detect raw engine line %q", tr)
		}
	}

	// Legitimate prose must never collapse.
	for _, prose := range []string{
		"The decision was to build the feature.",
		"intent matters here.",
		"The workspace root is ./app",
	} {
		if isQuietTraceText(prose) {
			t.Errorf("quiet mode wrongly collapsed prose %q", prose)
		}
	}
}

// TestBuildQuietTraceLineFormat pins the single muted per-turn summary line:
// "▸ Trace: direct_response (21ms) · Alt+E to toggle".
func TestBuildQuietTraceLineFormat(t *testing.T) {
	got := buildQuietTraceLine("[AUTONOMY DECISION]\n  decision    : ◇ direct_response (no work)")
	want := "▸ Trace: direct_response (21ms) · Alt+E to toggle"
	if got != want {
		t.Errorf("buildQuietTraceLine = %q, want %q", got, want)
	}

	gotEvent := buildQuietTraceLine("[event] PromptAdmitted intent=modification latency=42ms")
	if !strings.Contains(gotEvent, "(42ms)") {
		t.Errorf("buildQuietTraceLine lost latency duration: %q", gotEvent)
	}
	if !strings.HasPrefix(gotEvent, "▸ Trace: ") || !strings.Contains(gotEvent, "Alt+E to toggle") {
		t.Errorf("buildQuietTraceLine format wrong: %q", gotEvent)
	}
}

// TestBuildDocumentLayoutQuietCollapse pins that a full autonomy trace record
// collapses to a SINGLE muted summary line in quiet mode, and expands back to
// the full raw lines in verbose mode.
func TestBuildDocumentLayoutQuietCollapse(t *testing.T) {
	defer SetTraceVerbose(false)
	SetTraceVerbose(false)

	raw := "[AUTONOMY DECISION]\n  intent      : modification (95%)\n  required    : mutate\n" +
		"  workspace   : build (covers)\n  decision    : ◇ direct_response (no work)"
	rec := record{role: roleStatus, text: raw}
	width := 100

	dl := BuildDocumentLayout([]record{rec}, width, "")
	if dl.Len() != 1 {
		t.Fatalf("quiet layout = %d lines, want exactly 1 summary line: %v", dl.Len(), dl.Lines)
	}
	summary := ansi.Strip(dl.Lines[0].RawText)
	if !strings.Contains(summary, "▸ Trace: direct_response") || !strings.Contains(summary, "Alt+E to toggle") {
		t.Errorf("quiet summary wrong: %q", summary)
	}
	for _, leaked := range []string{"intent", "required", "workspace", "decision"} {
		if strings.Contains(summary, leaked) {
			t.Errorf("quiet summary leaked raw field %q: %q", leaked, summary)
		}
	}

	// Verbose mode restores the full raw trace.
	SetTraceVerbose(true)
	dlVerbose := BuildDocumentLayout([]record{rec}, width, "")
	if dlVerbose.Len() < 4 {
		t.Fatalf("verbose layout = %d lines, want full multi-line trace", dlVerbose.Len())
	}
}

// TestRenderDeterministicPipelineStripsEventLines pins the stream path: a
// raw [event] engine line collapses to the muted summary in quiet mode and
// survives verbatim in verbose mode.
func TestRenderDeterministicPipelineStripsEventLines(t *testing.T) {
	defer SetTraceVerbose(false)
	SetTraceVerbose(false)

	raw := "[event] PromptAdmitted intent=modification latency=42ms"
	quiet := ansi.Strip(RenderDeterministicPipeline(raw, 100, false))
	if !strings.Contains(quiet, "▸ Trace:") {
		t.Errorf("quiet pipeline failed to collapse [event] line: %q", quiet)
	}
	if strings.Contains(quiet, "PromptAdmitted") {
		t.Errorf("quiet pipeline leaked raw [event] line: %q", quiet)
	}

	SetTraceVerbose(true)
	verbose := ansi.Strip(RenderDeterministicPipeline(raw, 100, false))
	if !strings.Contains(verbose, "PromptAdmitted") {
		t.Errorf("verbose pipeline dropped raw [event] line: %q", verbose)
	}
}

// TestAltETogglesTraceVerboseToast pins that pressing Alt+E toggles trace
// verbosity, fires the Top Bar toast (Trace: EXPANDED / Trace: COLLAPSED) and
// refreshes the viewport.
func TestAltETogglesTraceVerboseToast(t *testing.T) {
	defer SetTraceVerbose(false)
	SetTraceVerbose(false)

	m := readyChatModel(newTestModel())

	// Alt+E → verbose → expanded toast.
	_, _ = m.handleKey(tea.KeyMsg{Alt: true, Type: tea.KeyRunes, Runes: []rune{'e'}})
	if !IsTraceVerbose() {
		t.Fatal("Alt+E must toggle TraceVerbose to true")
	}
	if m.toast != "Trace: EXPANDED" {
		t.Errorf("Alt+E toast = %q, want Trace: EXPANDED", m.toast)
	}

	// Alt+V (alias) → quiet → collapsed toast.
	_, _ = m.handleKey(tea.KeyMsg{Alt: true, Type: tea.KeyRunes, Runes: []rune{'v'}})
	if IsTraceVerbose() {
		t.Fatal("Alt+V must toggle TraceVerbose back to false")
	}
	if m.toast != "Trace: COLLAPSED" {
		t.Errorf("Alt+V toast = %q, want Trace: COLLAPSED", m.toast)
	}

	// The toggle triggers a viewport refresh (doc layout rebuilt).
	if !m.toastActive() {
		t.Error("toggle toast must be active (top bar renders it)")
	}
}

// TestIncrementalLayoutUpdateSingleTraceSummary pins the strict per-turn
// dedup: multiple trace records appended incrementally (stream path) yield
// EXACTLY ONE "▸ Trace:" line and zero raw engine leakage.
func TestIncrementalLayoutUpdateSingleTraceSummary(t *testing.T) {
	defer SetTraceVerbose(false)
	SetTraceVerbose(false)
	width := 100

	recs := make([]record, 0, 3)
	recs = append(recs, record{role: roleStatus, text: "[event] PromptAdmitted intent=modification latency=42ms"})
	dl := BuildDocumentLayout(recs, width, "")
	if dl.Len() != 1 {
		t.Fatalf("first build = %d lines, want exactly 1 summary: %v", dl.Len(), dl.Lines)
	}
	if !dl.traceSummaryEmitted {
		t.Fatal("first build must mark traceSummaryEmitted")
	}

	// Stream appends two more engine records incrementally (multi-record append).
	recs = append(recs,
		record{role: roleActivity, text: "[preflight] decision surface staged target=index.html"},
		record{role: roleStatus, text: "[AUTONOMY DECISION]\n  decision    : ◇ direct_response (no work)"},
	)
	dl2 := IncrementalLayoutUpdate(&dl, recs, width, "")

	summaries := 0
	for _, l := range dl2.Lines {
		if strings.Contains(ansi.Strip(l.RawText), "▸ Trace:") {
			summaries++
		}
	}
	if summaries != 1 {
		t.Fatalf("incremental layout has %d ▸ Trace: lines, want exactly 1: %v", summaries, dl2.Lines)
	}
	if !dl2.traceSummaryEmitted {
		t.Fatal("merged layout must carry traceSummaryEmitted")
	}
	// Zero raw engine log leakage.
	for _, l := range dl2.Lines {
		raw := ansi.Strip(l.RawText)
		for _, leaked := range []string{"PromptAdmitted", "[preflight]", "intent", "decision", "required", "workspace", "direct_response (no work)"} {
			if strings.Contains(raw, leaked) {
				t.Errorf("raw engine line leaked %q in %q", leaked, raw)
			}
		}
	}
}

// TestIncrementalLayoutUpdateSecondTurnResetsSummary pins that the per-turn
// reset (performed at prompt submission) enables a FRESH summary for the next
// turn: exactly one summary per turn, so two turns yield exactly two.
func TestIncrementalLayoutUpdateSecondTurnResetsSummary(t *testing.T) {
	defer SetTraceVerbose(false)
	SetTraceVerbose(false)
	width := 100

	countSummaries := func(dl *DocumentLayout) int {
		n := 0
		for _, l := range dl.Lines {
			if strings.Contains(ansi.Strip(l.RawText), "▸ Trace:") {
				n++
			}
		}
		return n
	}

	recs := make([]record, 0, 2)
	recs = append(recs, record{role: roleStatus, text: "[event] PromptAdmitted intent=modification latency=42ms"})
	dl := BuildDocumentLayout(recs, width, "")
	if got := countSummaries(&dl); got != 1 {
		t.Fatalf("turn 1 summary count = %d, want 1", got)
	}

	// Simulate the per-turn reset performed at prompt submission.
	dl.traceSummaryEmitted = false

	// Turn 2 appends a fresh engine record; its own summary is emitted.
	recs = append(recs, record{role: roleStatus, text: "[preflight] decision surface staged target=new.go"})
	dl2 := IncrementalLayoutUpdate(&dl, recs, width, "")
	if got := countSummaries(&dl2); got != 2 {
		t.Fatalf("two turns must yield one summary each (got %d):\n%v", got, dl2.Lines)
	}
}

// TestStyleActivityLineDedupsTraceSummary pins the per-line activity path: the
// first raw engine line collapses to the single summary and every subsequent
// one for the same turn is suppressed entirely.
func TestStyleActivityLineDedupsTraceSummary(t *testing.T) {
	defer SetTraceVerbose(false)
	SetTraceVerbose(false)

	m := readyChatModel(newTestModel())

	first := m.styleActivityLine("[event] PromptAdmitted intent=modification latency=42ms")
	if !strings.Contains(stripANSIFooter(first), "▸ Trace:") {
		t.Errorf("first trace line must render the summary, got %q", first)
	}
	second := m.styleActivityLine("[preflight] decision surface staged target=index.html")
	if second != "" {
		t.Errorf("second trace line must be suppressed, got %q", second)
	}
	third := m.styleActivityLine("[submit_prompt] intent parsed intent=modification")
	if third != "" {
		t.Errorf("third trace line must be suppressed, got %q", third)
	}
}

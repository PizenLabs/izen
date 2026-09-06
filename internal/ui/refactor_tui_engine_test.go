package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/autonomy"
)

// Requirement A: Telemetry & Execution Observation Demuxing (P0)
func TestTelemetryDemuxerAndTraceBuffer(t *testing.T) {
	demuxer := NewTelemetryDemuxer()
	demuxer.Reset(1)

	telemetryLogs := []string{
		"[loop] observing -> deciding",
		"[grant] grant-42 requested mutate",
		"[runtime] execution loop step 1 latency=12ms",
		"[preflight] decision surface staged target=internal/auth/token.go",
	}

	for _, log := range telemetryLogs {
		isTelem, anchor := demuxer.Ingest(log)
		if !isTelem {
			t.Errorf("expected %q to be classified as telemetry", log)
		}
		if !strings.HasPrefix(anchor, "▸ Trace") {
			t.Errorf("expected anchor to start with '▸ Trace', got %q", anchor)
		}
	}

	if demuxer.StepCount() != len(telemetryLogs) {
		t.Errorf("step count = %d, want %d", demuxer.StepCount(), len(telemetryLogs))
	}

	anchorText := demuxer.AnchorText()
	if !strings.Contains(anchorText, "steps") || !strings.Contains(anchorText, "Press Alt+T to expand") {
		t.Errorf("anchorText wrong format: %q", anchorText)
	}

	// Verify overlay rendering
	overlay := demuxer.RenderOverlay(80, 24)
	if !strings.Contains(overlay, "Telemetry Trace Buffer") {
		t.Errorf("overlay missing title: %s", overlay)
	}
	if !strings.Contains(overlay, "observing -> deciding") {
		t.Errorf("overlay missing demuxed step log: %s", overlay)
	}
	if !strings.Contains(overlay, "Close Trace Overlay") {
		t.Errorf("overlay missing dismissal hint: %s", overlay)
	}
}

// Requirement A: Alt+T Toggle Trace Overlay Drawer
func TestAltTTogglesTraceOverlay(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.telemetryDemuxer.Ingest("[loop] observing -> deciding")

	if m.showTraceOverlay {
		t.Fatal("trace overlay should be hidden initially")
	}

	// Alt+T opens overlay
	res, _ := m.handleKey(tea.KeyMsg{Alt: true, Type: tea.KeyRunes, Runes: []rune{'t'}})
	m2 := res.(*model)
	if !m2.showTraceOverlay {
		t.Fatal("Alt+T must toggle showTraceOverlay to true")
	}

	// Esc closes overlay
	res, _ = m2.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	m3 := res.(*model)
	if m3.showTraceOverlay {
		t.Fatal("Esc must dismiss trace overlay")
	}

	// Alt+T toggles it back open
	res, _ = m3.handleKey(tea.KeyMsg{Alt: true, Type: tea.KeyRunes, Runes: []rune{'t'}})
	m4 := res.(*model)
	if !m4.showTraceOverlay {
		t.Fatal("Alt+T must toggle showTraceOverlay to true")
	}
}

// Requirement B: Compact Autonomy Gate Banner (P0)
func TestCompactAutonomyGateBannerLayout(t *testing.T) {
	m := readyChatModel(newTestModel())
	prop := &autonomy.Proposal{
		Input:         "modify internal/auth/token.go",
		Target:        "internal/auth/token.go",
		Intent:        autonomy.IntentModification,
		Workspace:     autonomy.WorkspaceBuild,
		Risk:          autonomy.RiskLow,
		AffectedScope: 1,
		Actions:       []string{"Read", "Propose", "Mutate", "Verify"},
		Rollback:      true,
	}
	m.pendingAutonomyProposal = prop

	view := m.renderAutonomyProposalBlock(80)
	raw := ansi.Strip(view)
	lines := strings.Split(raw, "\n")

	// Must be compact: 4-5 lines (or up to 6 with borders)
	if len(lines) > 7 {
		t.Errorf("banner height too tall (%d lines), want <= 6 lines:\n%s", len(lines), view)
	}

	// Check content requirements
	for _, want := range []string{
		"AUTONOMY REQUEST",
		"Target:", "internal/auth/token.go",
		"Risk:", "LOW",
		"Scope:", "1 file",
		"Plan:", "Read -> Propose -> Mutate -> Verify",
		"[Enter]", "Approve & Run",
		"[I]", "Inspect Diff",
		"[Esc]", "Reject",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("compact banner missing %q:\n%s", want, raw)
		}
	}
}

// Requirement C: Viewport Lifecycle & Header Management (P1)
func TestViewportLifecycleIdleVsActiveSession(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.showBanner = true
	m.records = nil

	// Idle state: banner is rendered when records == 0 and showBanner == true
	prefixH := m.viewportContentPrefixHeight()
	if prefixH <= 0 {
		t.Fatal("idle state must have non-zero prefix height (banner)")
	}

	// Active session state: upon receiving first intent/record, banner collapses into single status line
	m.records = []record{{role: roleUser, text: "build the feature", turnID: 1}}
	m.showBanner = false

	header := m.renderWorkspaceHeader()
	rawHeader := ansi.Strip(header)
	if !strings.Contains(rawHeader, "izen v0.1.0") {
		t.Errorf("active header missing version: %q", rawHeader)
	}
	if !strings.Contains(rawHeader, "git:") {
		t.Errorf("active header missing git branch: %q", rawHeader)
	}
	if !strings.Contains(rawHeader, "Mode") {
		t.Errorf("active header missing mode tag: %q", rawHeader)
	}
}

// Requirement D: Humanized Recovery & Error Rendering (P1)
func TestPresentationMiddlewareBoundedPatchRecovery(t *testing.T) {
	mw := NewPresentationMiddleware()

	intercepted, badge := mw.InterceptError("OUTPUT_EXHAUSTED: token limit reached in executor")
	if !intercepted {
		t.Fatal("expected OUTPUT_EXHAUSTED to be intercepted by middleware")
	}
	rawBadge := ansi.Strip(badge)
	if !strings.Contains(rawBadge, "Token limit reached. Auto-recovering via bounded patch...") {
		t.Errorf("recovery badge format wrong: %q", rawBadge)
	}

	// Section 30 completion tuple
	completion := FormatCompletionState("COMPLETED", "PARTIALLY_VERIFIED")
	rawComp := ansi.Strip(completion)
	if rawComp != "COMPLETED · PARTIALLY_VERIFIED" {
		t.Errorf("completion tuple = %q, want 'COMPLETED · PARTIALLY_VERIFIED'", rawComp)
	}
}

// Requirement E: Patch & Diff Block Highlighting (P2)
func TestPatchAndDiffBlockHighlighting(t *testing.T) {
	diffContent := `diff --git a/internal/auth/token.go b/internal/auth/token.go
--- a/internal/auth/token.go
+++ b/internal/auth/token.go
@@ -10,4 +10,6 @@
 func ValidateToken() bool {
+    // added line 1
+    // added line 2
-    return false
+    return true
 }`

	docLines := renderCodeBlockToLines("diff", strings.Split(diffContent, "\n"), 80)
	if len(docLines) == 0 {
		t.Fatal("expected rendered DocumentLines for diff")
	}

	firstLine := docLines[0].RenderedStr
	rawFirst := ansi.Strip(firstLine)

	if !strings.Contains(rawFirst, "Patch: internal/auth/token.go") {
		t.Errorf("header missing patch target: %q", rawFirst)
	}
	if !strings.Contains(rawFirst, "(+3, -1)") {
		t.Errorf("header missing line delta (+3, -1): %q", rawFirst)
	}

	// Check background styling (Catppuccin Mantle) on body lines
	foundMantleBg := false
	for _, dl := range docLines[1 : len(docLines)-1] {
		if strings.Contains(dl.RenderedStr, "48;2;24;24;37m") {
			foundMantleBg = true
			break
		}
	}
	if !foundMantleBg {
		t.Error("diff lines must contain Catppuccin Mantle background escape code")
	}
}

package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/modes"
)

// headerTestModel builds a model with the minimal runtime/workflow surface the
// fixed Top Bar needs, plus an indexed status and /build mode badge.
func headerTestModel() *model {
	m := readyChatModel(newTestModel())
	m.runtimeCtx = &runtime.RuntimeContext{}
	m.workflowSM = workflow.NewWorkflowStateMachine()
	m.indexingStatus = "indexed"
	return m
}

// TestTopBarToastOverlayRightBoundary pins the toast overlay contract: the
// message renders on the far-right boundary of the fixed Top Bar, replacing
// the mode badge without ever expanding the bar's height.
func TestTopBarToastOverlayRightBoundary(t *testing.T) {
	m := headerTestModel()
	m.setToast("Copied selection to clipboard")

	width := 100
	withToast := m.renderTopBar(width)
	withToastStripped := stripANSIFooter(withToast)

	// The toast survives intact inside the rendered bar.
	if !strings.Contains(withToastStripped, "Copied selection to clipboard") {
		t.Errorf("toast not rendered on Top Bar:\n%q", withToastStripped)
	}
	// Left region survives the overlay: workflow state + indexing badge.
	if !strings.Contains(withToastStripped, "IDLE") {
		t.Errorf("workflow-state badge lost during toast overlay:\n%q", withToastStripped)
	}
	if !strings.Contains(withToastStripped, "Indexed") {
		t.Errorf("indexing badge lost during toast overlay:\n%q", withToastStripped)
	}
	// The right-side mode badge is replaced while a toast is live.
	if strings.Contains(withToastStripped, "[WRITE]") {
		t.Errorf("mode badge leaked during toast overlay:\n%q", withToastStripped)
	}

	// Height invariant: the toast must never expand the Top Bar. Both renders
	// are exactly 2 lines (content + bottom border chrome).
	if got := countLines(withToast); got != 2 {
		t.Errorf("toast overlay expanded Top Bar height to %d lines (want 2):\n%q", got, withToast)
	}

	// Width invariant: the rendered bar never exceeds the terminal width.
	for _, line := range strings.Split(withToastStripped, "\n") {
		if lipgloss.Width(line) > width {
			t.Errorf("Top Bar line exceeds width %d: %q", width, line)
		}
	}

	// The toast is right-aligned: it sits strictly to the right of the
	// workflow badge content.
	content := strings.SplitN(withToastStripped, "\n", 2)[0]
	if !strings.HasSuffix(strings.TrimRight(content, " "), "]") {
		t.Errorf("toast not on far-right boundary: %q", content)
	}
}

// TestTopBarIdleShowsModeBadge pins the idle Top Bar: when no toast is active
// the right side renders the Mode Badge instead of static R/W/X/T/P/C/B flags.
func TestTopBarIdleShowsModeBadge(t *testing.T) {
	m := headerTestModel()

	rendered := stripANSIFooter(m.renderTopBar(100))
	if !strings.Contains(rendered, "[WRITE]") {
		t.Errorf("idle Top Bar missing mode badge:\n%q", rendered)
	}
	if !strings.Contains(rendered, "IDLE") {
		t.Errorf("idle Top Bar missing workflow state:\n%q", rendered)
	}
	if !strings.Contains(rendered, "Indexed") {
		t.Errorf("idle Top Bar missing indexing badge:\n%q", rendered)
	}
	// Static capability flags are gone completely (padded " R " badge form).
	for _, flag := range []string{" R ", " W ", " X ", " T ", " P ", " C ", " B "} {
		if strings.Contains(rendered, flag) {
			t.Errorf("static capability flag %q leaked into Top Bar:\n%q", flag, rendered)
		}
	}
}

// TestTopBarAskModeShowsReadOnlyBadge pins that the Top Bar right side shows
// the Mode Badge — for read-only modes that is "[READ-ONLY]" — while the footer
// never duplicates it.
func TestTopBarAskModeShowsReadOnlyBadge(t *testing.T) {
	m := headerTestModel()
	m.resolver.Set(modes.ModeAsk)

	rendered := stripANSIFooter(m.renderTopBar(100))
	if !strings.Contains(rendered, "[READ-ONLY]") {
		t.Errorf("Top Bar missing [READ-ONLY] badge for ask mode:\n%q", rendered)
	}

	// The footer must NOT echo the badge (it belongs exclusively to the Top Bar).
	footer := stripANSIFooter(m.renderFixedFooter(120, nil))
	if strings.Contains(footer, "[READ-ONLY]") {
		t.Errorf("footer must not contain the mode badge:\n%q", footer)
	}
	if !strings.HasPrefix(strings.TrimSpace(footer), "qwen2.5-coder:7b") {
		t.Errorf("footer must start directly with the model name:\n%q", footer)
	}
}

// TestTopBarToastExpiryRestoresModeBadge pins the 2500ms auto-clear window:
// once the toast expires, toastActive() flips false and the mode badge returns
// to the far-right boundary.
func TestTopBarToastExpiryRestoresModeBadge(t *testing.T) {
	m := headerTestModel()
	m.setToast("Transient notice")

	if !m.toastActive() {
		t.Fatal("freshly set toast must be active")
	}

	// Age the toast past its window.
	m.toastSetAt = time.Now().Add(-toastDuration - time.Second)
	if m.toastActive() {
		t.Fatal("toast must auto-clear after toastDuration")
	}

	rendered := stripANSIFooter(m.renderTopBar(100))
	if strings.Contains(rendered, "Transient notice") {
		t.Errorf("expired toast still rendered:\n%q", rendered)
	}
	// The mode badge comes back once the toast clears.
	if !strings.Contains(rendered, "[WRITE]") {
		t.Errorf("mode badge not restored after toast expiry:\n%q", rendered)
	}
}

// TestTopBarToastClearedNeverRenders pins clearToast: an explicit clear
// removes the toast even while the window is still open.
func TestTopBarToastClearedNeverRenders(t *testing.T) {
	m := headerTestModel()
	m.setToast("Pending toast")
	m.clearToast()

	if m.toastActive() {
		t.Fatal("cleared toast must not be active")
	}
	rendered := stripANSIFooter(m.renderTopBar(100))
	if strings.Contains(rendered, "Pending toast") {
		t.Errorf("cleared toast rendered:\n%q", rendered)
	}
}

// TestPadRightOverlayWidthInvariant pins the overlay helper: the combined base
// + overlay line never exceeds the target width.
func TestPadRightOverlayWidthInvariant(t *testing.T) {
	for _, width := range []int{20, 40, 80, 120} {
		base := "● IDLE  ● Indexed"
		overlay := renderToast("copied to clipboard and verified ok")
		line := padRightOverlay(base, overlay, width)
		if lipgloss.Width(stripANSIFooter(line)) > width {
			t.Errorf("width %d: overlay line exceeds: %q", width, stripANSIFooter(line))
		}
		if !strings.Contains(line, base) {
			t.Errorf("width %d: base content lost: %q", width, line)
		}
	}
}

// TestViewportGeometrySingleLineFooter pins the clean-layout constraint: the
// idle-telemetry status row and the toast row are eliminated, so the viewport
// reclaims the exact 1-line footer + 1 dropped status row (2 lines total) vs
// the old layout. The prompt bar anchors directly above that single line.
func TestViewportGeometrySingleLineFooter(t *testing.T) {
	m := headerTestModel()
	m.height = 40
	m.width = 100

	geo := m.viewportGeometry()

	// The old layout reserved: header (2) + status (1) + input (3) + footer
	// chrome+content (2) = 8 fixed rows. The new layout drops the status row
	// and the footer chrome, leaving header (2) + input (3) + footer (1) = 6.
	// Viewport height = height - fixed = 40 - 6 - proposal(0) = 34.
	if geo.Height != 34 {
		t.Errorf("viewport height = %d, want 34 (1-line footer, no status row)", geo.Height)
	}
	// The footer is a single line and sits immediately above the terminal
	// bottom; its content never wraps to a second row.
	footer := m.renderFixedFooter(m.width, nil)
	if countLines(footer) != 1 {
		t.Errorf("footer rendered %d lines (want 1):\n%q", countLines(footer), footer)
	}
}

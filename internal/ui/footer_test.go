package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/modes"
)

// TestStatusBar_TelemetryContextualRender pins the footer telemetry contract:
// under a read-only mode (ask) with zero-value budget usage the full counter
// trio condenses to a single subtle indicator, the footer layout string length
// is minimized, and a status toast never overlaps the telemetry region.
func TestStatusBar_TelemetryContextualRender(t *testing.T) {
	width := 80
	rc := &runtime.RuntimeContext{Budget: budget.DefaultBudget()}
	toast := "Copied selection to clipboard"

	ask := renderFixedFooter(rc, modes.ModeAsk, toast, width)
	build := renderFixedFooter(rc, modes.ModeBuild, toast, width)

	askStripped := ansi.Strip(ask)
	buildStripped := ansi.Strip(build)

	// Read-only + zero usage: verbose labels must be hidden entirely.
	for _, label := range []string{"Files", "Diffs", "Attempts"} {
		if strings.Contains(askStripped, label) {
			t.Errorf("ask footer leaked verbose label %q:\n%q", label, askStripped)
		}
	}
	// ...and condensed into a single subtle indicator.
	if !strings.Contains(askStripped, "·") {
		t.Errorf("ask footer missing condensed subtle indicator:\n%q", askStripped)
	}

	// The toast must survive intact (never clipped by the condensed telemetry).
	if !strings.Contains(askStripped, toast) {
		t.Errorf("toast clipped in ask footer:\n%q", askStripped)
	}
	if !strings.Contains(buildStripped, toast) {
		t.Errorf("toast clipped in build footer:\n%q", buildStripped)
	}

	// Width invariant: neither footer may exceed the terminal width.
	if lipgloss.Width(askStripped) > width {
		t.Errorf("ask footer exceeds width %d: %q (%d)", width, askStripped, lipgloss.Width(askStripped))
	}
	if lipgloss.Width(buildStripped) > width {
		t.Errorf("build footer exceeds width %d: %q (%d)", width, buildStripped, lipgloss.Width(buildStripped))
	}

	// Minimization: the condensed ask footer's content line (below the chrome
	// top border) is strictly shorter than the build-mode footer's content
	// line with the same zero-value counters.
	askContent := footerContentLine(askStripped)
	buildContent := footerContentLine(buildStripped)
	if lipgloss.Width(strings.TrimRight(askContent, " ")) >= lipgloss.Width(strings.TrimRight(buildContent, " ")) {
		t.Errorf("ask footer not minimized: ask=%d build=%d\nask=%q\nbuild=%q",
			lipgloss.Width(strings.TrimRight(askContent, " ")),
			lipgloss.Width(strings.TrimRight(buildContent, " ")),
			askContent, buildContent)
	}

	// Active coding mode shows the compact icon counters with '/' ratios.
	if !strings.Contains(buildStripped, "/") {
		t.Errorf("build footer missing compact counters:\n%q", buildStripped)
	}
	for _, glyph := range []string{telemetryFileIcon, telemetryDiffIcon, telemetryAttemptIcon} {
		if !strings.Contains(buildStripped, glyph) {
			t.Errorf("build footer missing icon glyph %q:\n%q", glyph, buildStripped)
		}
	}
}

// TestStatusBar_TelemetryThresholdStyling pins the Catppuccin threshold
// colours: muted #6c7086 at low usage, yellow #f9e2af above 70%, and red
// #f38ba8 above 90%.
func TestStatusBar_TelemetryThresholdStyling(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	cases := []struct {
		name    string
		files   int
		wantSGR string
	}{
		{"low 20%", 2, "38;2;108;112;134m"},      // muted #6c7086
		{"warn 80%", 8, "38;2;249;226;175m"},     // yellow #f9e2af
		{"danger 100%", 10, "38;2;243;139;168m"}, // red #f38ba8
	}
	for _, c := range cases {
		b := budget.NewBudget(10, 1000, 100000, 10, 30*time.Minute, 10)
		if err := b.Consume(budget.BudgetDelta{Files: c.files}); err != nil {
			t.Fatalf("%s: consume: %v", c.name, err)
		}
		got := renderBudgetCounters(b, modes.ModeBuild)
		if !strings.Contains(got, c.wantSGR) {
			t.Errorf("%s: counter missing SGR %q:\n%q", c.name, c.wantSGR, got)
		}
	}
}

// footerContentLine returns the content line below the footer's chrome top
// border (the padded "─" separator row is skipped).
func footerContentLine(stripped string) string {
	parts := strings.Split(strings.TrimSuffix(stripped, "\n"), "\n")
	if len(parts) > 1 {
		return parts[1]
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return ""
}

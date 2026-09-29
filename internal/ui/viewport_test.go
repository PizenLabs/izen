package ui

// ── PHASE 15: SIDEBAR HUD ────────────────────────────────────────────────────
//
// Telemetry that appends to the conversation is unreadable by construction: the
// previous value is already gone, so what the reader sees is a number with no
// reference point. The HUD replaces that failure mode with IN-PLACE REPLACE, and
// the tests below assert the property that makes it work — a value change is a
// rewrite of the same cells, never an append, so the surrounding chrome cannot
// reflow and the viewport cannot jitter.
//
// The two properties worth pinning:
//
//	FIXED SHAPE   Every render emits the same number of lines in the same order,
//	              whatever the values are. An empty slot renders a placeholder and
//	              is never dropped.
//	FIXED WIDTH   Every field is padded to a constant cell count, so two successive
//	              renders of different values are the same geometry.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestPhase15_HUDIsNotMountedBeforeAnyReading(t *testing.T) {
	hud := NewSidebarHUD()
	// A fresh session mounts no telemetry. This is the same rule the fixed footer
	// follows, and it is a MOUNT decision: Render still emits the full fixed shape,
	// so the geometry a value change rewrites is the geometry that was reserved.
	if hud.Active() {
		t.Error("a HUD with no reading reports itself mountable")
	}
	if got := len(strings.Split(hud.Render(80), "\n")); got != len(hudMetricOrder) {
		t.Errorf("Render lines = %d, want the full fixed shape %d even before mount", got, len(hudMetricOrder))
	}
	hud.SetTokens(1024)
	if !hud.Active() {
		t.Error("a HUD with a real reading is not mountable")
	}
	hud.SetHidden(true)
	if hud.Active() {
		t.Error("a hidden HUD is mountable")
	}
}

func TestPhase15_HUDRendersEverySlotBeforeAnyReading(t *testing.T) {
	hud := NewSidebarHUD()
	rendered := hud.Render(80)
	// A brand-new HUD has no readings and must still render its full shape.
	// Collapsing an empty slot would shift every slot below it upward, which is the
	// jitter this type exists to remove.
	for _, metric := range hudMetricOrder {
		if !strings.Contains(rendered, strings.ToUpper(string(metric))) {
			t.Errorf("a slot with no reading was dropped from the layout: %q missing from\n%s", metric, rendered)
		}
		if !strings.Contains(rendered, HUDUnknown) {
			t.Errorf("an empty slot did not render its placeholder:\n%s", rendered)
		}
	}
	if got := len(strings.Split(rendered, "\n")); got != len(hudMetricOrder) {
		t.Fatalf("lines = %d, want exactly one per slot (%d)", got, len(hudMetricOrder))
	}
}

func TestPhase15_HUDShapeIsInvariantUnderValueChanges(t *testing.T) {
	hud := NewSidebarHUD()
	before := hud.Render(80)

	hud.SetTokens(128)
	hud.SetCost("$0.0001")
	hud.SetMCPStatus("off")
	after := hud.Render(80)

	// Same line count, same order, same per-line geometry. Only the VALUES differ.
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")
	if len(beforeLines) != len(afterLines) {
		t.Fatalf("line count changed: %d -> %d", len(beforeLines), len(afterLines))
	}
	for i := range beforeLines {
		if ansi.StringWidth(beforeLines[i]) != ansi.StringWidth(afterLines[i]) {
			t.Fatalf("line %d width changed: %d -> %d\n%s\n%s",
				i, ansi.StringWidth(beforeLines[i]), ansi.StringWidth(afterLines[i]), before, after)
		}
		if !strings.Contains(afterLines[i], strings.ToUpper(string(hudMetricOrder[i]))) {
			t.Fatalf("line %d no longer names its slot:\n%s", i, after)
		}
	}
	// And it is genuinely a replacement, not a second line: the new value appears
	// and the old one does not.
	if !strings.Contains(after, "128") || strings.Contains(after, HUDUnknown) {
		t.Fatalf("the value was not replaced in place:\n%s", after)
	}
}

func TestPhase15_HUDNeverDropsAnUnwrittenSlot(t *testing.T) {
	// Setting one metric must not make the others vanish. A variable layout is how
	// the surrounding chrome reflows.
	hud := NewSidebarHUD()
	hud.SetCost("$1.00")
	rendered := hud.Render(80)
	if !strings.Contains(rendered, strings.ToUpper(string(HUDContextTokens))) {
		t.Errorf("writing one slot removed another:\n%s", rendered)
	}
	if !strings.Contains(rendered, strings.ToUpper(string(HUDMCPStatus))) {
		t.Errorf("writing one slot removed another:\n%s", rendered)
	}
	if hud.Get(HUDContextTokens) != HUDUnknown {
		t.Errorf("an unwritten slot was initialised to %q", hud.Get(HUDContextTokens))
	}
}

func TestPhase15_HUDEmptyValueRendersAsUnknownNotBlank(t *testing.T) {
	hud := NewSidebarHUD()
	hud.SetTokens(0)
	// A genuine zero and an unknown are DIFFERENT facts, and only one of them may
	// render as a bare "0".
	if got := hud.Get(HUDContextTokens); got != "0" {
		t.Errorf("a real zero rendered as %q, want 0 — zero is a measurement", got)
	}
	hud.SetTokens(-5)
	if got := hud.Get(HUDContextTokens); got != HUDUnknown {
		t.Errorf("a negative count rendered as %q — nonsense input must read as unknown", got)
	}
	hud.Set(HUDCost, "   ")
	if got := hud.Get(HUDCost); got != HUDUnknown {
		t.Errorf("a blank value rendered as %q, want the placeholder", got)
	}
}

func TestPhase15_HUDCompactCountFormatting(t *testing.T) {
	hud := NewSidebarHUD()
	cases := []struct {
		tokens int
		want   string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{12_345, "12.3k"},
		{999_999, "1000.0k"},
		{1_500_000, "1.5M"},
	}
	for _, tc := range cases {
		hud.SetTokens(tc.tokens)
		if got := hud.Get(HUDContextTokens); got != tc.want {
			t.Errorf("SetTokens(%d) = %q, want %q", tc.tokens, got, tc.want)
		}
	}
}

func TestPhase15_HUDTruncatesRatherThanWrapping(t *testing.T) {
	hud := NewSidebarHUD()
	hud.SetMCPStatus(strings.Repeat("very-long-server-name ", 20))
	// A wrapping HUD is a jittering HUD: a value that overflows must be truncated
	// on its own line, never carried onto a second one.
	rendered := hud.Render(40)
	if got := len(strings.Split(rendered, "\n")); got != len(hudMetricOrder) {
		t.Fatalf("lines = %d, want %d — an overflowing value must not wrap", got, len(hudMetricOrder))
	}
	if !strings.Contains(rendered, "…") {
		t.Errorf("the overflow was silently dropped instead of truncated:\n%s", rendered)
	}
}

func TestPhase15_HUDHiddenRetainsReadings(t *testing.T) {
	hud := NewSidebarHUD()
	hud.SetTokens(4096)
	hud.SetHidden(true)
	if hud.Active() {
		t.Error("a hidden HUD reports itself active")
	}
	if got := hud.Render(80); got != "" {
		t.Errorf("a hidden HUD rendered: %q", got)
	}
	hud.SetHidden(false)
	// The reading is retained verbatim in storage; the HUD's own compact formatting
	// is what a caller sees, so the assertion is on the round-trip through Render,
	// not on the internal representation.
	if hud.Get(HUDContextTokens) == HUDUnknown {
		t.Errorf("hiding the HUD discarded its readings: %q", hud.Get(HUDContextTokens))
	}
	rendered := hud.Render(80)
	if !strings.Contains(rendered, "4.1k") {
		t.Errorf("re-showing the HUD lost the reading:\n%s", rendered)
	}
}

func TestPhase15_HUDNilReceiverIsInert(t *testing.T) {
	var hud *SidebarHUD
	// Every method must be safe on a nil receiver: the model constructs the HUD
	// lazily, and a headless render path may ask before anything has been routed.
	hud.Set(HUDCost, "$1")
	hud.SetTokens(1)
	hud.SetCost("$1")
	hud.SetMCPStatus("on")
	hud.SetHidden(true)
	if hud.Get(HUDCost) != HUDUnknown {
		t.Error("a nil HUD returned a value")
	}
	if hud.Active() {
		t.Error("a nil HUD reports itself active")
	}
	if got := hud.Render(80); got != "" {
		t.Errorf("a nil HUD rendered: %q", got)
	}
}

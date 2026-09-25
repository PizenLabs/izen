package model_picker

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

// clippedBadgeRe matches a badge word cut mid-token by an ellipsis, e.g.
// "[Think…" or "[Age…". Capability truth must never be rendered that way.
var clippedBadgeRe = regexp.MustCompile(`\[[A-Za-z]{2,}…`)

// longIDModel is the DoD case: a model name long enough to overflow a narrow
// ID column, whose tail carries the distinguishing variant.
func longIDModel() registry.ModelDescriptor {
	return registry.ModelDescriptor{
		ID:            "models/gemini-3.8-pro-extended-thinking",
		Provider:      "openrouter",
		Name:          "Gemini 3.8 Pro Extended Thinking",
		ContextWindow: 1000000,
		Capabilities:  []registry.ModelCapability{registry.CapVision, registry.CapThinking, registry.CapTools},
	}
}

// twinModels are two sibling variants that right-truncation would collapse
// into the same visible prefix.
func twinModels() []registry.ModelDescriptor {
	return []registry.ModelDescriptor{
		{ID: "models/gemini-3.8-pro-extended-thinking", Provider: "openrouter", ContextWindow: 1000000,
			Capabilities: []registry.ModelCapability{registry.CapThinking, registry.CapTools}},
		{ID: "models/gemini-3.8-flash-lite-preview", Provider: "openrouter", ContextWindow: 1000000,
			Capabilities: []registry.ModelCapability{registry.CapVision, registry.CapTools}},
	}
}

// TestMiddleTruncateKeepsBothEnds: the ID column elides the middle, so the
// vendor prefix and the version/variant tail both stay readable.
func TestMiddleTruncateKeepsBothEnds(t *testing.T) {
	cases := []struct {
		in string
		w  int
	}{
		{"models/gemini-3.8-pro-extended-thinking", 34},
		{"openrouter/thinkingmachines/inkling-small:free", 24},
		{"models/gemini-3.8-pro-extended-thinking", 12},
	}
	for _, tc := range cases {
		got := middleTruncate(tc.in, tc.w)
		if w := runewidth.StringWidth(got); w > tc.w {
			t.Errorf("middleTruncate(%q, %d) width = %d, want <= %d", tc.in, tc.w, w, tc.w)
		}
		if runewidth.StringWidth(tc.in) <= tc.w {
			continue
		}
		// The elision sits in the middle: both ends survive verbatim.
		idx := strings.Index(got, "…")
		if idx <= 0 || idx >= len(got)-1 {
			t.Fatalf("middleTruncate(%q, %d) = %q, want a middle ellipsis", tc.in, tc.w, got)
		}
		head, tail := got[:idx], got[idx+len("…"):]
		if !strings.HasPrefix(tc.in, head) {
			t.Errorf("middleTruncate(%q, %d) head = %q, want a literal prefix of the ID", tc.in, tc.w, head)
		}
		if !strings.HasSuffix(tc.in, tail) {
			t.Errorf("middleTruncate(%q, %d) tail = %q, want a literal suffix of the ID", tc.in, tc.w, tail)
		}
		if runewidth.StringWidth(head) == 0 || runewidth.StringWidth(tail) == 0 {
			t.Errorf("middleTruncate(%q, %d) = %q, both ends must survive", tc.in, tc.w, got)
		}
	}
	// Short values pass through untouched; non-positive widths render empty.
	if got := middleTruncate("short", 10); got != "short" {
		t.Errorf("middleTruncate(short, 10) = %q", got)
	}
	if got := middleTruncate("short", 0); got != "" {
		t.Errorf("middleTruncate(short, 0) = %q, want empty", got)
	}
	// A width of 1 can only hold the ellipsis.
	if got := middleTruncate("abcdef", 1); runewidth.StringWidth(got) > 1 {
		t.Errorf("middleTruncate(abcdef, 1) = %q, width > 1", got)
	}
}

// TestModelRowMiddleTruncatesLongIDs: a long model ID is middle-truncated in the
// row itself, never right-truncated into an indistinguishable prefix.
func TestModelRowMiddleTruncatesLongIDs(t *testing.T) {
	m := New(seedSnapshot([]registry.ModelDescriptor{longIDModel()})).
		SetPaneFocus(PaneModels).SetSize(80, 24)
	row := m.renderModelRow(longIDModel(), 60)
	if !strings.Contains(row, "…") {
		t.Fatalf("a long model ID must be elided, got %q", row)
	}
	if !strings.HasPrefix(strings.TrimRight(row, " "), "models/") {
		t.Fatalf("row must keep the vendor prefix, got %q", row)
	}
	if !strings.Contains(row, "thinking") {
		t.Fatalf("row must keep the distinguishing tail, got %q", row)
	}
	if strings.HasPrefix(row, "models/gemini-3.8-pro-extended-thin…") {
		t.Fatalf("right-truncation is forbidden: %q", row)
	}
}

// TestSiblingVariantsStayDistinguishable: two long siblings that share a
// prefix must still read differently in the ID column.
func TestSiblingVariantsStayDistinguishable(t *testing.T) {
	models := twinModels()
	m := New(seedSnapshot(models)).SetPaneFocus(PaneModels).SetSize(80, 24)
	a := strings.TrimRight(m.renderModelRow(models[0], 60), " ")
	b := strings.TrimRight(m.renderModelRow(models[1], 60), " ")
	if a == b {
		t.Fatalf("sibling variants collapse to the same row: %q", a)
	}
	if !strings.Contains(a, "thinking") || !strings.Contains(b, "lite-preview") {
		t.Fatalf("tails must survive: %q / %q", a, b)
	}
}

// TestBadgeVocabularyByViewportWidth: the badge wording follows the total
// viewport width — compact at/below 100 columns, full above it.
func TestBadgeVocabularyByViewportWidth(t *testing.T) {
	cases := []struct {
		w       int
		agentic string
	}{
		{80, "[Ag]"},
		{100, "[Ag]"},
		{101, registry.AgenticBadge},
		{140, registry.AgenticBadge},
	}
	for _, tc := range cases {
		m := New(seedSnapshot([]registry.ModelDescriptor{agenticModel()})).SetSize(tc.w, 30)
		if got := m.badgeVocabulary().agentic; got != tc.agentic {
			t.Errorf("viewport %d: agentic badge = %q, want %q", tc.w, got, tc.agentic)
		}
	}
}

// TestBadgesColumnExpandsWithViewport: the badges container grows when the
// terminal allows it (dynamic column math, not a hardcoded width).
func TestBadgesColumnExpandsWithViewport(t *testing.T) {
	models := []registry.ModelDescriptor{{
		ID: "thinkingmachines/inkling-small:free", Provider: "openrouter",
		ContextWindow: 256000,
		Capabilities:  []registry.ModelCapability{registry.CapThinking, registry.CapTools},
	}}
	narrow := New(seedSnapshot(models)).SetSize(80, 24)
	wide := New(seedSnapshot(models)).SetSize(140, 40)

	if got := narrow.badgeLayout(56).width; got >= wide.badgeLayout(116).width {
		t.Fatalf("badges column must expand with the viewport: narrow=%d wide=%d", got, wide.badgeLayout(116).width)
	}
	// The wide layout uses the full vocabulary at its natural width.
	if got := wide.badgeLayout(116); got.vocab.thinking != "[Thinking]" {
		t.Fatalf("wide layout vocabulary = %+v, want the full labels", got.vocab)
	}
	if got := narrow.badgeLayout(56); got.vocab.thinking != "[Th]" {
		t.Fatalf("narrow layout vocabulary = %+v, want the compact labels", got.vocab)
	}
}

// TestNoClippedBadgesAtAnyViewport: across the DoD viewports the capability
// column is never rendered as a clipped word.
func TestNoClippedBadgesAtAnyViewport(t *testing.T) {
	models := []registry.ModelDescriptor{
		agenticModel(),
		{ID: "models/gemini-3.8-pro-extended-thinking", Provider: "openrouter", ContextWindow: 1000000,
			Capabilities: []registry.ModelCapability{registry.CapThinking, registry.CapVision, registry.CapTools}},
		standardModel(),
	}
	for _, size := range []struct{ w, h int }{{80, 24}, {100, 30}, {140, 40}} {
		m := New(seedSnapshot(models)).SetPaneFocus(PaneModels).SetSize(size.w, size.h)
		view := m.View()
		if hit := clippedBadgeRe.FindString(view); hit != "" {
			t.Errorf("size %dx%d: badge clipped mid-word: %q\n%s", size.w, size.h, hit, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if !strings.Contains(line, "│") {
				continue
			}
			if w := lipgloss.Width(line); w > m.innerWidth {
				t.Errorf("size %dx%d: row width %d exceeds inner width %d: %q", size.w, size.h, w, m.innerWidth, line)
			}
		}
	}
}

// TestCompactBadgesRenderAtNarrowViewport: an 80-column terminal renders the
// compact indicators instead of clipping words.
func TestCompactBadgesRenderAtNarrowViewport(t *testing.T) {
	models := []registry.ModelDescriptor{{
		ID: "thinkingmachines/inkling:free", Provider: "openrouter", ContextWindow: 256000,
		Capabilities: []registry.ModelCapability{registry.CapThinking, registry.CapTools},
	}}
	view := New(seedSnapshot(models)).SetPaneFocus(PaneModels).SetSize(80, 24).View()
	if !strings.Contains(view, "[Ag]") {
		t.Fatalf("narrow viewport must render the compact agentic badge, got:\n%s", view)
	}
	if !strings.Contains(view, "[Th]") {
		t.Fatalf("narrow viewport must render the compact thinking badge, got:\n%s", view)
	}
	if strings.Contains(view, "[Agentic]") || strings.Contains(view, "[Thinking]") {
		t.Fatalf("narrow viewport must not use the full badge vocabulary, got:\n%s", view)
	}
}

// TestFullBadgesRenderAtWideViewport: a >100-column terminal expands the
// badges container to the full vocabulary.
func TestFullBadgesRenderAtWideViewport(t *testing.T) {
	models := []registry.ModelDescriptor{{
		ID: "thinkingmachines/inkling:free", Provider: "openrouter", ContextWindow: 256000,
		Capabilities: []registry.ModelCapability{registry.CapThinking, registry.CapTools},
	}}
	view := New(seedSnapshot(models)).SetPaneFocus(PaneModels).SetSize(140, 40).View()
	if !strings.Contains(view, registry.AgenticBadge) || !strings.Contains(view, "[Thinking]") {
		t.Fatalf("wide viewport must render the full badge vocabulary, got:\n%s", view)
	}
}

// TestBadgeCellDropsInsteadOfClipping: a cell too narrow for every badge keeps
// the leading ones whole and drops the rest.
func TestBadgeCellDropsInsteadOfClipping(t *testing.T) {
	d := registry.ModelDescriptor{
		ID: "x", Provider: "openrouter",
		Capabilities: []registry.ModelCapability{registry.CapThinking, registry.CapVision, registry.CapTools},
	}
	if got := badgeCell(d, compactBadgeSet(), 20); got != "[Th] [Vis] [Tl]" {
		t.Errorf("badgeCell(20) = %q, want every compact badge", got)
	}
	if got := badgeCell(d, compactBadgeSet(), 10); got != "[Th] [Vis]" {
		t.Errorf("badgeCell(10) = %q, want the badges that fit", got)
	}
	if got := badgeCell(d, compactBadgeSet(), 4); got != "[Th]" {
		t.Errorf("badgeCell(4) = %q, want exactly one whole badge", got)
	}
	if got := badgeCell(d, compactBadgeSet(), 3); strings.Contains(got, "…") == false {
		t.Errorf("badgeCell(3) = %q, want an honest clip of the single token", got)
	}
	if got := badgeCell(registry.ModelDescriptor{ID: "x", Provider: "x"}, compactBadgeSet(), 12); got == "" {
		t.Error("a model with no capabilities must still render a placeholder")
	}
}

// TestNoCapabilityRendersDash: a model whose descriptor carries no capability
// and whose ID the classifier resolves to none (legacy text-only endpoint)
// renders the em-dash placeholder, never an empty or clipped cell.
func TestNoCapabilityRendersDash(t *testing.T) {
	d := registry.ModelDescriptor{ID: "vendor/text-davinci-003", Provider: "vendor"}
	if got := capabilityFlags(d); got != "—" {
		t.Fatalf("capabilityFlags = %q, want the em-dash placeholder", got)
	}
	if got := badgeCell(d, compactBadgeSet(), 12); got != "—" {
		t.Fatalf("badgeCell = %q, want the em-dash placeholder", got)
	}
}

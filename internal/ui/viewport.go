package ui

// ── Sidebar HUD: in-place telemetry surface (Phase 15) ───────────────────────
//
// Telemetry has exactly one job on this surface: be readable at a glance, at a
// stable position, while the thing the human is actually reading changes. That
// makes it a fundamentally different kind of content from the narrative, and
// treating it as narrative is what produced the reported defect: live token
// counts, cost and MCP status were appended as ordinary records, so every
// update added a line to the conversation and pushed the previous value out of
// view. A number that only exists as history is not telemetry, it is litter.
//
// The fix is IN-PLACE REPLACE, and it is a property of the layout rather than a
// rule anyone remembers:
//
//   - Every metric has a FIXED slot. A metric never moves because another metric
//     changed value, appeared, or disappeared.
//   - Every slot renders to a FIXED width. A value change is a rewrite of the
//     same cells, not an append, so the surrounding chrome cannot reflow and the
//     viewport cannot jitter.
//   - A slot with no reading renders its LABEL and a placeholder, never nothing.
//     Collapsing an empty slot would shift every slot below it upward, which is
//     the jitter this type exists to remove.
//
// The consequence worth stating: the HUD cannot leak into the Main Narrative.
// There is no method here that appends a record, and the Main Viewport's state
// has no field this type can write to. The separation is structural, not a
// convention.

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// HUDMetric names one fixed telemetry slot. The set is closed: a slot that does
// not exist cannot be added at runtime, which is what keeps the render
// deterministic.
type HUDMetric string

const (
	// HUDContextTokens is the compiled workspace context the model was given.
	HUDContextTokens HUDMetric = "context tokens"
	// HUDCost is the accumulated provider cost for the session.
	HUDCost HUDMetric = "cost"
	// HUDMCPStatus is the aggregate state of the configured MCP servers.
	HUDMCPStatus HUDMetric = "mcp status"
)

// hudMetricOrder is the fixed render order. Slots render in this order, always —
// it is the reason a value change can never move a neighbouring metric.
var hudMetricOrder = []HUDMetric{HUDContextTokens, HUDCost, HUDMCPStatus}

// HUDUnknown is the rendering of a slot that has never received a reading. It is
// explicit rather than blank so a missing reading is visibly missing, and
// distinct from a real zero.
const HUDUnknown = "—"

// hudSlotWidth is the fixed cell width of every slot's value field. The label is
// truncated to its own fixed width beside it, so a slot's total footprint is
// constant for the life of the session.
const (
	hudLabelWidth = 16
	hudValueWidth = 14
)

// SidebarHUD holds the fixed telemetry block. It is safe for concurrent use:
// live token counts arrive on a stream goroutine while cost and MCP status are
// written on the UI goroutine, and a torn read would render a half-updated
// block.
type SidebarHUD struct {
	mu     sync.RWMutex
	values map[HUDMetric]string
	// hidden suppresses rendering without discarding readings. A hidden HUD still
	// accumulates, so re-showing it never shows a stale value.
	hidden bool
	// reading records that at least one slot holds a real value. It is what makes
	// Active() answer "is there telemetry to show" rather than "does this object
	// exist" — the distinction is the whole reason a fresh session is not cluttered
	// with three rows of placeholders.
	reading bool
}

// NewSidebarHUD returns a HUD with every slot at its placeholder. The slots exist
// from the start on purpose: a slot that materializes on first use is a slot
// that shifts the layout the moment it appears.
func NewSidebarHUD() *SidebarHUD {
	h := &SidebarHUD{values: make(map[HUDMetric]string, len(hudMetricOrder))}
	for _, metric := range hudMetricOrder {
		h.values[metric] = HUDUnknown
	}
	return h
}

// Set writes one slot's value. An empty value writes the placeholder rather than
// clearing the slot, so the block's shape is invariant.
func (h *SidebarHUD) Set(metric HUDMetric, value string) {
	if h == nil || metric == HUDMetric("") {
		return
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		trimmed = HUDUnknown
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.values == nil {
		h.values = make(map[HUDMetric]string, len(hudMetricOrder))
		h.reading = false
	}
	// A slot that returns to its placeholder is no longer a reading. Tracking that
	// is what lets Active() mean "there is something to show" rather than "a
	// record was written at some point in the session".
	if trimmed != HUDUnknown && !h.reading {
		h.reading = true
	}
	h.values[metric] = trimmed
}

// SetTokens writes the context-token slot from a count. A negative count is
// nonsense input and is rendered as unknown rather than as a negative number
// that would read like a real measurement.
func (h *SidebarHUD) SetTokens(tokens int) {
	if tokens < 0 {
		h.Set(HUDContextTokens, "")
		return
	}
	h.Set(HUDContextTokens, formatHUDCount(tokens))
}

// SetCost writes the cost slot from a pre-formatted amount. Formatting belongs
// to the caller that owns the pricing registry; the HUD stores and places.
func (h *SidebarHUD) SetCost(amount string) { h.Set(HUDCost, amount) }

// SetMCPStatus writes the MCP status slot.
func (h *SidebarHUD) SetMCPStatus(status string) { h.Set(HUDMCPStatus, status) }

// Get returns one slot's current value.
func (h *SidebarHUD) Get(metric HUDMetric) string {
	if h == nil {
		return HUDUnknown
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if v, ok := h.values[metric]; ok && v != "" {
		return v
	}
	return HUDUnknown
}

// SetHidden shows or hides the block. Readings are retained either way.
func (h *SidebarHUD) SetHidden(hidden bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hidden = hidden
}

// Active reports whether the block should be MOUNTED in the viewport: it is
// visible, and at least one slot holds a real reading.
//
// A fresh session mounts nothing. That is the same rule the fixed footer already
// follows — "a brand-new session never clutters the footer with idle telemetry" —
// and it is a MOUNT decision, not a render one. Render() always emits the full
// fixed shape, so a value change is a rewrite of the same cells; Active() decides
// whether there is anything worth mounting. Keeping the two separate is what lets
// the shape be constant without the block appearing before it has content.
func (h *SidebarHUD) Active() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return !h.hidden && h.reading
}

// Render returns the fixed telemetry block. It emits exactly one line per
// metric, in the fixed order, with each field padded to its constant width — so
// two successive renders of different values are the same shape, which is the
// in-place-replace guarantee a caller can assert on.
//
// It returns "" for a hidden or nil HUD. Width is the available column budget;
// a value that would overflow it is truncated with an ellipsis rather than
// allowed to wrap onto a second line, because a wrapping HUD is a jittering HUD.
func (h *SidebarHUD) Render(width int) string {
	if h == nil {
		return ""
	}
	h.mu.RLock()
	hidden := h.hidden
	values := make(map[HUDMetric]string, len(hudMetricOrder))
	for k, v := range h.values {
		values[k] = v
	}
	h.mu.RUnlock()
	if hidden {
		return ""
	}
	// The indentation matches the narrative's two-space gutter so the block reads
	// as part of the same surface rather than as foreign chrome.
	const gutter = "  "
	lines := make([]string, 0, len(hudMetricOrder))
	for _, metric := range hudMetricOrder {
		label := padHUD(strings.ToUpper(string(metric)), hudLabelWidth)
		value := padHUD(values[metric], hudValueWidth)
		lines = append(lines, gutter+label+value)
	}
	out := strings.Join(lines, "\n")
	if width > 0 && ansi.StringWidth(out) > width {
		// Truncate line-wise so the block keeps its line count; a dropped line
		// would be a shifted block, which is the exact failure this whole type
		// exists to prevent.
		truncated := make([]string, 0, len(lines))
		for _, line := range lines {
			truncated = append(truncated, ansi.Truncate(line, width, "…"))
		}
		out = strings.Join(truncated, "\n")
	}
	return out
}

// padHUD left-aligns s in a field of exactly width cells, truncating with an
// ellipsis when it does not fit. The result is always exactly `width` cells for
// non-empty input, which is what makes the block's geometry constant.
func padHUD(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Strip(s)
	if s == "" {
		s = HUDUnknown
	}
	if ansi.StringWidth(s) > width {
		return ansi.Truncate(s, width, "…")
	}
	return s + strings.Repeat(" ", width-ansi.StringWidth(s))
}

// formatHUDCount renders a token count compactly. Large contexts are a
// four-or-five digit number in a fourteen-cell field, and spelling out
// thousands separators would consume half the slot for no added information at a
// glance.
func formatHUDCount(n int) string {
	if n < 0 {
		return HUDUnknown
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

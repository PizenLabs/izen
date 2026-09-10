package model_picker

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	coredomain "github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/provider/adapter"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

// effortStyle returns the Catppuccin Mocha semantic style for a reasoning
// option. Unknown options fall back to muted.
func effortStyle(opt string) lipgloss.Style {
	if s, ok := effortStyles[opt]; ok {
		return s
	}
	return mutedStyle
}

//nolint:unused // retained for legacy compatibility
func renderReasoningItem(level string, selected bool) string {
	style := effortStyle(level)
	if selected {
		return style.Underline(true).Render("[" + level + "]")
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086")).Render(level)
}

// ReasoningCapabilityFor derives the truthful capability record for a model
// via the registry CapabilityResolver (Model ID / Provider matching).
// Unknown capabilities are never inferred (I6): unknown providers map to
// ReasoningModeNone => Supported=false. Fixed => Supported but not
// Configurable. Enum/Toggle => Supported and Configurable with declared opts.
// Model-specific families (Claude effort ladder, Gemini 2.5 token budgets,
// DeepSeek R1 fixed, gpt-4o/dots denylist) resolve before provider fallback.
func ReasoningCapabilityFor(d registry.ModelDescriptor) coredomain.ReasoningCapability {
	return d.GetReasoningCapability()
}

// CapabilityForHighlighted reports the truthful capability of the active
// selection: the pinned detail model in StateDetail, the live highlight
// otherwise (SelectedModel is detail-aware; identical to Highlighted while
// browsing).
func (m Model) CapabilityForHighlighted() coredomain.ReasoningCapability {
	sel := m.SelectedModel()
	if sel == nil {
		return coredomain.ReasoningCapability{}
	}
	return ReasoningCapabilityFor(*sel)
}

// ReasoningModeFor resolves the provider-native reasoning mode for a model
// via adapter.CapabilityForProvider. Unknown providers yield ReasoningModeNone.
func ReasoningModeFor(d registry.ModelDescriptor) adapter.ReasoningMode {
	return adapter.CapabilityForProvider(d.Provider).ReasoningMode
}

// DefaultReasoningOption is the provider-factory fallback tier. When it is
// the selected option, CurrentReasoningSelection returns nil so downstream
// model API request payloads omit the reasoning_effort key entirely.
const DefaultReasoningOption = "default"

// ReasoningOptionsFor returns the selectable option IDs for a model derived
// from its real ReasoningCapability (Model ID / Provider matching):
//
//	Generic EnumStandard: default • low • medium • high
//	Generic EnumExtended: default • low • medium • high • xhigh • max
//	Generic ToggleAuto:   default • off • auto • on
//	Claude family:        low • medium • high • xhigh • max (concrete, no default)
//	Gemini 2.5 family:    off • budget_2k • budget_8k • budget_16k • budget_24k
//	Fixed/None:           zero options (no caller-selected effort)
//
// Fixed and None yield zero options so callers never force universal
// low/medium/high variants and the section collapses.
func ReasoningOptionsFor(d registry.ModelDescriptor) []string {
	caps := ReasoningCapabilityFor(d)
	if !caps.Supported || !caps.Configurable || len(caps.Options) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(caps.Options))
	for _, o := range caps.Options {
		out = append(out, o.ID)
	}
	return out
}

// clampedReasoningIdx keeps idx inside options (0 when empty).
func clampedReasoningIdx(idx int, options []string) int {
	if len(options) == 0 {
		return 0
	}
	if idx < 0 {
		return 0
	}
	if idx >= len(options) {
		return len(options) - 1
	}
	return idx
}

// CurrentReasoningOption reports the selected option ID for the highlighted
// model, or ("", false) when the mode carries no caller-selected option
// (Fixed / None). The initial selection is always the first capability option
// ("default" for generic modes, concrete first tier for Claude/Gemini).
func (m Model) CurrentReasoningOption() (string, bool) {
	sel := m.SelectedModel()
	if sel == nil {
		return "", false
	}
	options := ReasoningOptionsFor(*sel)
	if len(options) == 0 {
		return "", false
	}
	return options[clampedReasoningIdx(m.selectedReasoningOptIdx, options)], true
}

// CurrentReasoningSelection builds the domain ReasoningSelection for the
// highlighted model, or nil when the mode carries no caller-selected option
// (Fixed / None) or when the "default" fallback tier is selected. A nil
// selection omits the reasoning_effort key downstream, preserving provider
// factory behavior.
func (m Model) CurrentReasoningSelection() *adapter.ReasoningSelection {
	opt, ok := m.CurrentReasoningOption()
	if !ok || opt == DefaultReasoningOption {
		return nil
	}
	return &adapter.ReasoningSelection{Option: opt}
}

// CycleReasoning shifts the reasoning index within the highlighted model's
// permitted options only. Fixed/None models are a no-op.
func (m Model) CycleReasoning(delta int) Model {
	hl := m.Highlighted()
	if hl == nil {
		return m
	}
	options := ReasoningOptionsFor(*hl)
	if len(options) == 0 {
		m.reasoningIdx = 0
		m.selectedReasoningOptIdx = 0
		return m
	}
	next := clampedReasoningIdx(m.selectedReasoningOptIdx+delta, options)
	m.selectedReasoningOptIdx = next
	m.reasoningIdx = next
	m.syncReasoningPolicy()
	return m
}

// syncReasoningPolicy aligns the legacy reasoningPolicy string with the
// canonical selectedReasoningOptIdx so assignment payloads carry the concrete
// ReasoningOption.ID (e.g. "low", "high", "budget_8k"), not generic "default".
func (m *Model) syncReasoningPolicy() {
	if opt, ok := m.CurrentReasoningOption(); ok && opt != "" {
		m.reasoningPolicy = opt
		return
	}
	if m.reasoningPolicy == "" {
		m.reasoningPolicy = DefaultReasoningOption
	}
}

// ResetReasoning clamps the index for the active selection (call after
// cursor/filter/snapshot changes so a stale index never leaks across modes).
// In StateDetail it clamps against the pinned model so a background refilter
// can never leak a drifted highlight's option range into the assignment.
func (m *Model) resetReasoning() {
	sel := m.SelectedModel()
	if sel == nil {
		m.reasoningIdx = 0
		m.selectedReasoningOptIdx = 0
		return
	}
	next := clampedReasoningIdx(m.selectedReasoningOptIdx, ReasoningOptionsFor(*sel))
	// Preserve legacy reasoningIdx callers that never touched the new index:
	// if the legacy index is in range but the new one is stale-zero, adopt it.
	legacy := clampedReasoningIdx(m.reasoningIdx, ReasoningOptionsFor(*sel))
	if m.selectedReasoningOptIdx == 0 && m.reasoningIdx != 0 {
		next = legacy
	}
	m.reasoningIdx = next
	m.selectedReasoningOptIdx = next
	m.syncReasoningPolicy()
}

// RenderReasoningBar renders the dynamic reasoning effort bar for the
// highlighted model from its real ReasoningCapability.Options:
//
//	Generic EnumStandard: default • low • medium • high
//	Generic EnumExtended: default • low • medium • high • xhigh • max
//	Generic ToggleAuto:   default • off • auto • on
//	Claude family:        Low Effort • Medium Effort • High Effort • ...
//	Gemini 2.5:           Off (0 tokens) • 2,048 tokens • 8,192 tokens • ...
//	Fixed:                Fixed (Pure Chain-of-Thought) [Locked]
//	None:                 N/A (Standard Latency)
//
// The selected option renders highlighted; siblings render muted. Selecting
// "default" preserves provider factory behavior (reasoning_effort omitted).
// Non-reasoning models never render a variant line (caller shows PATH A).
func (m Model) RenderReasoningBar() string {
	hl := m.Highlighted()
	if hl == nil {
		return mutedStyle.Render(" N/A (Standard Latency)")
	}
	caps := ReasoningCapabilityFor(*hl)
	switch {
	case !caps.Supported:
		return mutedStyle.Render(" N/A (Standard Latency)")
	case !caps.Configurable || len(caps.Options) == 0:
		return mutedStyle.Render(" Fixed (Pure Chain-of-Thought) ") + otherBadge.Render("[Locked]")
	default:
		sel := clampedReasoningIdx(m.selectedReasoningOptIdx, ReasoningOptionsFor(*hl))
		cells := make([]string, 0, len(caps.Options))
		for i, opt := range caps.Options {
			label := opt.Label
			if label == "" {
				label = opt.ID
			}
			// Style by ID (provider-conformant), display human Label.
			if i == sel {
				cells = append(cells, effortStyle(opt.ID).Underline(true).Render("["+label+"]"))
			} else {
				cells = append(cells, lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086")).Render(label))
			}
		}
		return strings.Join(cells, mutedStyle.Render(" • "))
	}
}

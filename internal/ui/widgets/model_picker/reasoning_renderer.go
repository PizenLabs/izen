package model_picker

import (
	"strings"

	"github.com/PizenLabs/izen/internal/provider/adapter"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

// ReasoningModeFor resolves the provider-native reasoning mode for a model
// via adapter.CapabilityForProvider. Unknown providers yield ReasoningModeNone.
func ReasoningModeFor(d registry.ModelDescriptor) adapter.ReasoningMode {
	return adapter.CapabilityForProvider(d.Provider).ReasoningMode
}

// ReasoningOptionsFor returns the exact option list permitted for a model.
// It is strictly adapter.OptionsForMode(mode): Fixed and None yield zero
// options so callers never force universal low/medium/high variants.
func ReasoningOptionsFor(d registry.ModelDescriptor) []string {
	return adapter.OptionsForMode(ReasoningModeFor(d))
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

// CurrentReasoningOption reports the selected option for the highlighted
// model, or ("", false) when the mode carries no caller-selected option
// (Fixed / None).
func (m Model) CurrentReasoningOption() (string, bool) {
	hl := m.Highlighted()
	if hl == nil {
		return "", false
	}
	options := ReasoningOptionsFor(*hl)
	if len(options) == 0 {
		return "", false
	}
	return options[clampedReasoningIdx(m.reasoningIdx, options)], true
}

// CurrentReasoningSelection builds the domain ReasoningSelection for the
// highlighted model, or nil when the mode carries no caller-selected option.
func (m Model) CurrentReasoningSelection() *adapter.ReasoningSelection {
	opt, ok := m.CurrentReasoningOption()
	if !ok {
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
		return m
	}
	m.reasoningIdx = clampedReasoningIdx(m.reasoningIdx+delta, options)
	return m
}

// ResetReasoning clamps the index for the current highlight (call after
// cursor/filter/snapshot changes so a stale index never leaks across modes).
func (m *Model) resetReasoning() {
	hl := m.Highlighted()
	if hl == nil {
		m.reasoningIdx = 0
		return
	}
	m.reasoningIdx = clampedReasoningIdx(m.reasoningIdx, ReasoningOptionsFor(*hl))
}

// RenderReasoningBar renders the provider-native reasoning effort bar for the
// highlighted model:
//
//	EnumStandard: low • medium • high
//	EnumExtended: low • medium • high • xhigh • max
//	ToggleAuto:   off • auto • on
//	Fixed:        Fixed (Pure Chain-of-Thought) [Locked]
//	None:         N/A (Standard Latency)
//
// The selected option renders highlighted; siblings render muted.
func (m Model) RenderReasoningBar() string {
	hl := m.Highlighted()
	if hl == nil {
		return mutedStyle.Render(" N/A (Standard Latency)")
	}
	mode := ReasoningModeFor(*hl)
	switch mode {
	case adapter.ReasoningModeEnumStandard, adapter.ReasoningModeEnumExtended, adapter.ReasoningModeToggleAuto:
		options := adapter.OptionsForMode(mode)
		sel := clampedReasoningIdx(m.reasoningIdx, options)
		cells := make([]string, 0, len(options))
		for i, opt := range options {
			if i == sel {
				cells = append(cells, accentStyle.Render(opt))
			} else {
				cells = append(cells, mutedStyle.Render(opt))
			}
		}
		return strings.Join(cells, mutedStyle.Render(" • "))
	case adapter.ReasoningModeFixed:
		return mutedStyle.Render(" Fixed (Pure Chain-of-Thought) ") + otherBadge.Render("[Locked]")
	default:
		return mutedStyle.Render(" N/A (Standard Latency)")
	}
}

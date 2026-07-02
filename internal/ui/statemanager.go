package ui

import (
	"github.com/PizenLabs/izen/internal/modes"
)

// UIStateManager manages UI state for input locking
type UIStateManager struct {
	mode          modes.Mode
	hasStagedPlan bool
}

// NewUIStateManager creates a new state manager
func NewUIStateManager() *UIStateManager {
	return &UIStateManager{
		mode:          modes.ModeAsk,
		hasStagedPlan: false,
	}
}

// SetMode updates the current mode
func (m *UIStateManager) SetMode(mode modes.Mode) {
	m.mode = mode
}

// SetStagedPlan indicates whether a plan is currently staged
func (m *UIStateManager) SetStagedPlan(staged bool) {
	m.hasStagedPlan = staged
}

// IsInputLocked returns true if input should be locked (in plan/build mode with staged plan)
func (m *UIStateManager) IsInputLocked() bool {
	return (m.mode == modes.ModePlan || m.mode == modes.ModeBuild) && m.hasStagedPlan
}

// GetCurrentMode returns the current mode
func (m *UIStateManager) GetCurrentMode() modes.Mode {
	return m.mode
}

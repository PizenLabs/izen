package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/prompt"
	settings_widget "github.com/PizenLabs/izen/internal/ui/widgets/settings"
)

// persistGlobalConfigFn is the single persistence seam for settings commits.
// Tests can replace it without touching the user's home directory; production
// uses the canonical config persistence path.
var persistGlobalConfigFn = func(cfg *config.Config) error {
	return config.PersistGlobalConfig(cfg)
}

func mustStyle(raw string) prompt.StylePolicy {
	style, err := prompt.ParseStylePolicy(raw)
	if err != nil {
		return prompt.DefaultStylePolicy()
	}
	return style
}

// settingsConfig returns the live global configuration used by the settings
// surface. A headless test model may not have a config pointer, so fall back to
// the canonical global store rather than inventing a second owner.
func (m *model) settingsConfig() *config.Config {
	if m != nil && m.cfg != nil {
		return m.cfg
	}
	return config.GetGlobalConfig()
}

// openSettings opens only the reduced standalone settings surface. The modal
// never imports or mutates model_picker state and does not touch authorization,
// runtime authority, sessions, or shell paths.
func (m *model) openSettings() tea.Cmd {
	if m == nil {
		return nil
	}
	cfg := m.settingsConfig()
	// Ctrl+P is global and must take over the picker rather than leaving a
	// second modal active underneath the settings surface.
	m.showModelPicker = false
	m.settingsModel = settings_widget.NewFromConfig(cfg)
	if m.width > 0 && m.height > 0 {
		modalW, modalH := SettingsModalSize(m.width, m.height)
		m.settingsModel = m.settingsModel.SetSize(modalW, modalH)
	}
	m.showSettings = true
	m.ti.Blur()
	m.dismissSuggestions()
	return m.settingsModel.Init()
}

// closeSettings tears down the settings modal and returns keyboard focus to
// the primary workspace input. It is idempotent so a delayed CloseMsg cannot
// steal focus from a subsequently opened modal.
func (m *model) closeSettings() {
	if m == nil {
		return
	}
	m.showSettings = false
	m.ti.Focus()
}

// applySettingsCommit persists a changed value before applying it to runtime.
// This ordering prevents the UI from advertising a preference that failed to
// reach ~/.izen/config.yml.
func (m *model) applySettingsCommit(msg settings_widget.CommitMsg) {
	if m == nil {
		return
	}
	style := mustStyle(string(msg.Values.ResponseStyle))
	if msg.Field == settings_widget.FieldResponseStyle || msg.Field == "" {
		parsed, err := prompt.ParseStylePolicy(string(msg.Values.ResponseStyle))
		if err != nil {
			m.settingsModel = m.settingsModel.SetStatus("Invalid response style")
			return
		}
		style = parsed
	}
	autoScroll := config.NormalizeAutoScrollMode(string(msg.Values.AutoScroll))
	cfg := m.settingsConfig()
	if cfg == nil {
		m.settingsModel = m.settingsModel.SetStatus("Configuration unavailable")
		return
	}

	oldStyle := cfg.Style
	oldHideThinking := cfg.UI.HideThinking
	oldAutoScroll := cfg.UI.AutoScroll
	newStyle := oldStyle
	newHideThinking := oldHideThinking
	newAutoScroll := oldAutoScroll
	switch msg.Field {
	case settings_widget.FieldResponseStyle:
		newStyle = string(style)
	case settings_widget.FieldHideThinking:
		newHideThinking = msg.Values.HideThinking
	case settings_widget.FieldAutoScroll:
		newAutoScroll = autoScroll
	default:
		// A zero Field is accepted for small/headless hosts that construct a
		// commit message directly; in that case the complete value set is
		// authoritative.
		newStyle = string(style)
		newHideThinking = msg.Values.HideThinking
		newAutoScroll = autoScroll
	}
	cfg.Style = newStyle
	cfg.UI.HideThinking = newHideThinking
	cfg.UI.AutoScroll = newAutoScroll
	if err := persistGlobalConfigFn(cfg); err != nil {
		cfg.Style = oldStyle
		cfg.UI.HideThinking = oldHideThinking
		cfg.UI.AutoScroll = oldAutoScroll
		m.settingsModel = m.settingsModel.SetValues(settings_widget.Values{
			ResponseStyle: mustStyle(oldStyle),
			HideThinking:  oldHideThinking,
			AutoScroll:    config.NormalizeAutoScrollMode(string(oldAutoScroll)),
		}).SetStatus("Save failed: " + err.Error())
		return
	}

	if m.cfg == nil {
		m.cfg = cfg
	}
	// Runtime application is immediate: subsequent composed prompts read the
	// package-level active style without an application restart.
	activeStyle := mustStyle(cfg.Style)
	prompt.SetActiveStyle(activeStyle)
	m.hideThinkingBlocks = cfg.UI.HideThinking
	m.setAutoScrollMode(cfg.ActiveAutoScrollMode())
	m.settingsModel = m.settingsModel.
		SetValues(settings_widget.Values{
			ResponseStyle: activeStyle,
			HideThinking:  cfg.UI.HideThinking,
			AutoScroll:    cfg.ActiveAutoScrollMode(),
		}).
		SetStatus("Saved")
}

// applyLoadedConfig projects an externally observed global config change into
// all live settings authorities. The file watcher may observe edits made by a
// second process, so style hot-reload must not depend on the settings widget.
func (m *model) applyLoadedConfig(cfg *config.Config) {
	if m == nil || cfg == nil {
		return
	}
	m.cfg = cfg
	m.hideThinkingBlocks = cfg.UI.HideThinking
	m.setAutoScrollMode(cfg.ActiveAutoScrollMode())
	prompt.SetActiveStyle(cfg.ActiveStylePolicy())
	if m.showSettings {
		m.settingsModel = m.settingsModel.SetValues(settings_widget.Values{
			ResponseStyle: cfg.ActiveStylePolicy(),
			HideThinking:  cfg.UI.HideThinking,
			AutoScroll:    cfg.ActiveAutoScrollMode(),
		})
	}
}

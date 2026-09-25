package config

import (
	"fmt"
	"strings"
)

// AutoScrollMode is the user-selectable policy used by the workspace
// viewport manager while an LLM stream is producing output.
//
// The enum intentionally lives beside the persisted UI preferences, but the
// runtime authority for applying it remains the workspace viewport manager in
// internal/ui. Keeping the value in the global config makes a choice survive a
// restart without giving the TUI a second policy owner.
type AutoScrollMode string

const (
	// AutoScrollSmart follows the stream only while the user is already at
	// the tail. A manual scroll remains stable until the user returns to the
	// bottom.
	AutoScrollSmart AutoScrollMode = "smart"
	// AutoScrollAlways keeps the viewport pinned to the stream tail.
	AutoScrollAlways AutoScrollMode = "always"
	// AutoScrollOff leaves the current viewport position untouched.
	AutoScrollOff AutoScrollMode = "off"
)

// Compatibility aliases make the canonical names convenient for callers that
// prefer the longer form without introducing a second enum.
const (
	AutoScrollModeSmart  = AutoScrollSmart
	AutoScrollModeAlways = AutoScrollAlways
	AutoScrollModeOff    = AutoScrollOff
)

// String returns the canonical persisted spelling.
func (m AutoScrollMode) String() string { return string(m) }

// ValidAutoScrollModes lists the supported policies in display order.
var ValidAutoScrollModes = []AutoScrollMode{
	AutoScrollSmart,
	AutoScrollAlways,
	AutoScrollOff,
}

// ParseAutoScrollMode normalizes a persisted or user-supplied policy. An
// empty value is the backwards-compatible default (Smart).
func ParseAutoScrollMode(raw string) (AutoScrollMode, error) {
	switch AutoScrollMode(strings.ToLower(strings.TrimSpace(raw))) {
	case "":
		return AutoScrollSmart, nil
	case AutoScrollSmart:
		return AutoScrollSmart, nil
	case AutoScrollAlways:
		return AutoScrollAlways, nil
	case AutoScrollOff:
		return AutoScrollOff, nil
	default:
		return "", fmt.Errorf("unknown auto-scroll policy %q (valid: %s)", raw, strings.Join(autoScrollModeNames(), ", "))
	}
}

// NormalizeAutoScrollMode returns a supported policy, falling back to Smart
// for empty or invalid values. It is useful at runtime boundaries where a
// malformed hand-edited config must not break the TUI.
func NormalizeAutoScrollMode(raw string) AutoScrollMode {
	mode, err := ParseAutoScrollMode(string(raw))
	if err != nil {
		return AutoScrollSmart
	}
	return mode
}

// UIConfig contains presentation preferences that are safe to expose through
// the standalone Settings surface. It deliberately contains no provider,
// model, variant, role, credential, authorization, or execution settings.
type UIConfig struct {
	HideThinking bool           `yaml:"hide_thinking" json:"hide_thinking"`
	AutoScroll   AutoScrollMode `yaml:"auto_scroll" json:"auto_scroll"`
}

// ActiveAutoScrollMode returns the normalized viewport policy. Older config
// files predate the UI block and therefore resolve to Smart.
func (c *Config) ActiveAutoScrollMode() AutoScrollMode {
	if c == nil {
		return AutoScrollSmart
	}
	return NormalizeAutoScrollMode(string(c.UI.AutoScroll))
}

// HideThinkingBlocks reports the canonical persisted CoT visibility preference.
func (c *Config) HideThinkingBlocks() bool {
	return c != nil && c.UI.HideThinking
}

func autoScrollModeNames() []string {
	names := make([]string, 0, len(ValidAutoScrollModes))
	for _, mode := range ValidAutoScrollModes {
		names = append(names, string(mode))
	}
	return names
}

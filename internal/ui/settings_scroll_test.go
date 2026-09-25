package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/viewport"

	"github.com/PizenLabs/izen/internal/config"
)

func TestStandaloneViewportManagerControlsStreamTailPolicy(t *testing.T) {
	vp := viewport.New(80, 10)
	cases := []struct {
		name string
		mode config.AutoScrollMode
		lock bool
		want int
	}{
		{name: "smart follows unlocked tail", mode: config.AutoScrollSmart, want: 10},
		{name: "smart respects manual lock", mode: config.AutoScrollSmart, lock: true, want: 3},
		{name: "always overrides manual lock", mode: config.AutoScrollAlways, lock: true, want: 10},
		{name: "off preserves current position", mode: config.AutoScrollOff, want: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &model{
				Viewport:        vp,
				viewportManager: newViewportManager(tc.mode),
				docScrollOffset: 3,
			}
			m.setScrollLocked(tc.lock)
			if got := m.calculateEffectiveYOffset(20); got != tc.want {
				t.Fatalf("offset = %d, want %d", got, tc.want)
			}
		})
	}
}

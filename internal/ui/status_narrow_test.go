package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestStatusModalFitsNarrowWidths(t *testing.T) {
	for _, dims := range [][2]int{{40, 12}, {50, 16}, {60, 20}, {100, 40}} {
		m := newTestModel()
		m.Ready = false
		m.viewRegistry = nil
		m.width = dims[0]
		m.height = dims[1]
		m.showStatus = true
		rendered := m.renderStatusModal()
		lines := strings.Split(rendered, "\n")
		if len(lines) != dims[1] {
			t.Fatalf("%dx%d lines=%d", dims[0], dims[1], len(lines))
		}
		for i, line := range lines {
			if w := lipgloss.Width(ansi.Strip(line)); w > dims[0] {
				t.Fatalf("%dx%d line %d width %d", dims[0], dims[1], i, w)
			}
		}
	}
}

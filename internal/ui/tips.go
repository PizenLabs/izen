package ui

import (
	"math/rand"

	"github.com/charmbracelet/lipgloss"
)

var allTips = []string{
	"Use @file in your prompt to attach specific files for context.",
	"Use Ctrl+Up/Down to scroll through command history without re-typing.",
	"Use /plan before /build to decompose complex tasks into structured steps.",
	"Use /investigate to trace code paths and collect diagnostics before fixing bugs.",
	"Use /review to get a structured analysis of your code before committing.",
	"In Tmux, use Ctrl+b [ to enter scroll mode and browse output history.",
	"In Neovim, use K on a symbol to jump to its documentation.",
	"Use /mode ask to switch to Q&A mode without sandbox restrictions.",
	"The sandbox indicator shows RO (read-only) in ask/plan modes and RW (read-write) in build mode.",
	"Use /objective to define high-level goals that persist across sessions.",
	"Use /undo to revert the last build operation.",
	"Use /checkpoint to manually save the current workspace state.",
	"Use /arch to render a dependency graph of your codebase.",
	"The ctx optimized metric shows how much context was saved by AST pruning.",
	"Use /drop to remove attached files from the current session context.",
	"Run /help or /? at any time to see all available commands.",
	"Press [A] to accept individual build proposals or [L] to accept all at once.",
	"Use /commit to auto-generate a semantic commit message from staged changes.",
}

var taskSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸"}

func randomTip() string {
	return allTips[rand.Intn(len(allTips))]
}

func (m *model) renderTip(width int) string {
	if m.currentTip == "" {
		return ""
	}

	header := dimmedStyle.Render("▐ ") + mutedStyle.Render("💡 TIP: ")
	body := dimmedStyle.Render(m.currentTip)
	line := header + body

	avail := width - 2
	if lipgloss.Width(line) > avail {
		trunc := lipgloss.Width(header + body)
		diff := trunc - avail + 1
		bodyRunes := []rune(m.currentTip)
		if len(bodyRunes) > diff {
			body = dimmedStyle.Render(string(bodyRunes[:len(bodyRunes)-diff]) + "…")
		}
		line = header + body
	}

	return " " + line
}

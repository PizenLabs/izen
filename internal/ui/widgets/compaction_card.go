package widgets

import (
	"fmt"
	"strings"
)

// BudgetView is the presentation projection of a token budget. It mirrors the
// compactor.TokenBudget shape without importing the engine package so the
// widget layer stays decoupled from the compaction pipeline.
type BudgetView struct {
	MaxTokens          int
	CurrentTokens      int
	SystemPromptTokens int
	RecentTurnTokens   int
	SummaryTokens      int
	ToolOutputTokens   int
}

// ResultView is the presentation projection of a compaction run.
type ResultView struct {
	Strategy     string
	TokensBefore int
	TokensAfter  int
	FreedTokens  int
	Uncompacted  int
}

// RenderCompactionCard renders the /compact stats telemetry inside the UI
// viewport as a plaintext budget box.
func RenderCompactionCard(b BudgetView) string {
	pct := 0.0
	if b.MaxTokens > 0 {
		pct = float64(b.CurrentTokens) / float64(b.MaxTokens) * 100
	}
	bar := progressBar(b.CurrentTokens, b.MaxTokens, 34)
	var sb strings.Builder
	sb.WriteString("┌─ CONTEXT WINDOW BUDGET ──────────────────────────────────────────────┐\n")
	fmt.Fprintf(&sb, "│ Usage: [%s] %s / %s (%.0f%%)%s│\n",
		bar, commas(b.CurrentTokens), commas(b.MaxTokens), pct, padFor(58, bar, b.CurrentTokens, b.MaxTokens, pct))
	sb.WriteString("├──────────────────────────────────────────────────────────────────────┤\n")
	fmt.Fprintf(&sb, "│ • System Prompt & Tools : %s tokens (%s)%s│\n",
		padLeft(commas(b.SystemPromptTokens), 8), padLeft(share(b.SystemPromptTokens, b.CurrentTokens), 6), padRow(b.SystemPromptTokens, b.CurrentTokens))
	fmt.Fprintf(&sb, "│ • Pinned Recent Turns   : %s tokens (%s)%s│\n",
		padLeft(commas(b.RecentTurnTokens), 8), padLeft(share(b.RecentTurnTokens, b.CurrentTokens), 6), padRow(b.RecentTurnTokens, b.CurrentTokens))
	fmt.Fprintf(&sb, "│ • Compacted Summary     : %s tokens (%s)%s│\n",
		padLeft(commas(b.SummaryTokens), 8), padLeft(share(b.SummaryTokens, b.CurrentTokens), 6), padRow(b.SummaryTokens, b.CurrentTokens))
	prune := ""
	if b.ToolOutputTokens > 0 {
		prune = " [Prune Candidate]"
	}
	fmt.Fprintf(&sb, "│ • Live Tool Outputs     : %s tokens (%s)%s%s│\n",
		padLeft(commas(b.ToolOutputTokens), 8), padLeft(share(b.ToolOutputTokens, b.CurrentTokens), 6), prune, padRowTool(b.ToolOutputTokens, b.CurrentTokens, prune))
	sb.WriteString("└──────────────────────────────────────────────────────────────────────┘")
	return sb.String()
}

// RenderCompactionResult renders the terminal record of a /compact now run.
func RenderCompactionResult(r ResultView) string {
	return fmt.Sprintf("compaction %s: %s → %s tokens (freed %s, %d recent turn(s) retained)",
		r.Strategy, commas(r.TokensBefore), commas(r.TokensAfter), commas(r.FreedTokens), r.Uncompacted)
}

func progressBar(cur, max, width int) string {
	if width <= 0 {
		width = 20
	}
	if max <= 0 {
		return strings.Repeat("░", width)
	}
	filled := cur * width / max
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func commas(n int) string {
	if n < 0 {
		n = 0
	}
	s := fmt.Sprintf("%d", n)
	out := make([]byte, 0, len(s)+4)
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}

func share(part, total int) string {
	if total <= 0 {
		return " 0.0%"
	}
	return fmt.Sprintf("%4.1f%%", float64(part)/float64(total)*100)
}

func padLeft(s string, w int) string {
	for len(s) < w {
		s = " " + s
	}
	return s
}

// padFor keeps the usage line at a fixed visual width.
func padFor(_ int, bar string, cur, max int, pct float64) string {
	line := fmt.Sprintf("│ Usage: [%s] %s / %s (%.0f%%)", bar, commas(cur), commas(max), pct)
	// Target inner width 72 runes.
	w := 72 - len([]rune(line)) + len("│")
	if w < 0 {
		return ""
	}
	_ = w
	// Compute trailing spaces so the line closes with │.
	vis := len([]rune(fmt.Sprintf(" Usage: [%s] %s / %s (%.0f%%) ", bar, commas(cur), commas(max), pct)))
	sp := 70 - vis
	if sp < 0 {
		sp = 0
	}
	return strings.Repeat(" ", sp)
}

func padRow(_, _ int) string {
	return strings.Repeat(" ", 6)
}

func padRowTool(_, _ int, prune string) string {
	sp := 6 - len(prune)
	if sp < 0 {
		sp = 0
	}
	return strings.Repeat(" ", sp)
}

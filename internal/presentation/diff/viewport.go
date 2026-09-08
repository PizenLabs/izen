package diff

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// DefaultBudgetRatio is the default maximum vertical height ratio a rendered
// diff may occupy within the terminal viewport.
const DefaultBudgetRatio = 0.40

// DefaultTabWidth is the default number of spaces a tab character expands to.
const DefaultTabWidth = 4

// CalculateCellWidth returns the terminal cell display width of line after
// stripping ANSI escape sequences, expanding tab characters into tabWidth
// spaces, and summing the rune cell widths. East Asian wide runes count as two
// cells, so wrapping is never computed from byte or raw rune counts.
func CalculateCellWidth(line string, tabWidth int) int {
	if tabWidth <= 0 {
		tabWidth = DefaultTabWidth
	}
	plain := stripANSI(line)
	expanded := strings.ReplaceAll(plain, "\t", strings.Repeat(" ", tabWidth))
	width := 0
	for _, r := range expanded {
		width += runewidth.RuneWidth(r)
	}
	return width
}

// ComputeRenderPlan projects mutation evidence into the terminal viewport.
// Content width is the terminal width minus the gutter and prefix reservations,
// each line's visual rows are derived from its cell display width, and the
// allowed rows are floor(TermHeight * BudgetRatio). When the total visual rows
// fit, a RenderModeFullInline plan is returned; otherwise a symmetric
// head/tail truncated plan is produced.
func ComputeRenderPlan(evidence MutationEvidence, cfg ViewportConfig) RenderPlan {
	applyConfigDefaults(&cfg)

	contentWidth := cfg.TermWidth - (cfg.GutterWidth + cfg.PrefixWidth)
	if contentWidth < 1 {
		contentWidth = 1
	}

	allowed := int(float64(cfg.TermHeight) * cfg.BudgetRatio)
	if allowed < 0 {
		allowed = 0
	}

	n := len(evidence.Lines)
	rowCounts := make([]int, n)
	totalVisual := 0
	for i, line := range evidence.Lines {
		width := CalculateCellWidth(line.Content, cfg.TabWidth)
		rows := (width + contentWidth - 1) / contentWidth
		if rows < 1 {
			rows = 1
		}
		rowCounts[i] = rows
		totalVisual += rows
	}

	plan := RenderPlan{
		TotalVisual: totalVisual,
		AllowedRows: allowed,
	}

	if totalVisual <= allowed {
		plan.Mode = RenderModeFullInline
		plan.VisibleLines = evidence.Lines
		return plan
	}

	plan.Mode = RenderModeTruncatedHeadTail

	// Greedy symmetric head/tail selection: always grow the side that has
	// accumulated fewer visual rows so the visible slices stay balanced, while
	// never exceeding the allowed budget.
	head, tail := 0, n-1
	headRows, tailRows := 0, 0
	for head <= tail {
		if headRows+tailRows >= allowed {
			break
		}
		if head == tail {
			if headRows+tailRows+rowCounts[head] <= allowed {
				headRows += rowCounts[head]
				head++
			}
			break
		}
		if headRows <= tailRows {
			if headRows+tailRows+rowCounts[head] <= allowed {
				headRows += rowCounts[head]
				head++
				continue
			}
			if headRows+tailRows+rowCounts[tail] <= allowed {
				tailRows += rowCounts[tail]
				tail--
				continue
			}
			break
		}
		if headRows+tailRows+rowCounts[tail] <= allowed {
			tailRows += rowCounts[tail]
			tail--
			continue
		}
		if headRows+tailRows+rowCounts[head] <= allowed {
			headRows += rowCounts[head]
			head++
			continue
		}
		break
	}

	// Graceful degradation: never emit an empty render for non-empty evidence,
	// even when a single line alone exceeds the allowed budget.
	if headRows+tailRows == 0 && n > 0 {
		head = 1
	}

	visible := make([]PatchLine, 0, head+(n-(tail+1)))
	visible = append(visible, evidence.Lines[:head]...)
	visible = append(visible, evidence.Lines[tail+1:]...)
	plan.VisibleLines = visible

	if head <= tail {
		plan.TruncatedAt = tail - head + 1
	}
	return plan
}

// applyConfigDefaults clamps invalid geometry and applies the documented
// defaults for budget ratio and tab width.
func applyConfigDefaults(cfg *ViewportConfig) {
	if cfg.BudgetRatio <= 0 {
		cfg.BudgetRatio = DefaultBudgetRatio
	}
	if cfg.TabWidth <= 0 {
		cfg.TabWidth = DefaultTabWidth
	}
	if cfg.TermWidth < 1 {
		cfg.TermWidth = 1
	}
	if cfg.TermHeight < 0 {
		cfg.TermHeight = 0
	}
	if cfg.GutterWidth < 0 {
		cfg.GutterWidth = 0
	}
	if cfg.PrefixWidth < 0 {
		cfg.PrefixWidth = 0
	}
}

// stripANSI removes ANSI escape sequences (CSI, OSC, and two-byte escapes)
// from s so that styling never contributes to cell width calculations.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) {
			break
		}
		switch s[i+1] {
		case '[': // CSI: ESC [ parameter* intermediate* final-byte
			i += 2
			for i < len(s) {
				c := s[i]
				if c >= 0x40 && c <= 0x7E || c < 0x20 || c == 0x7F {
					break
				}
				i++
			}
			i++
		case ']': // OSC: ESC ] ... BEL | ESC \
			i += 2
			for i < len(s) {
				if s[i] == 0x07 {
					i++
					break
				}
				if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		default: // Two-byte escape (e.g. ESC 7, ESC M)
			i += 2
		}
	}
	return b.String()
}

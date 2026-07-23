package review

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

type ProvenanceRenderer struct {
	Ledger *ReviewLedger
	Width  int
}

func NewProvenanceRenderer(ledger *ReviewLedger, width int) *ProvenanceRenderer {
	if width < 40 {
		width = 40
	}
	if width > 120 {
		width = 120
	}
	return &ProvenanceRenderer{
		Ledger: ledger,
		Width:  width,
	}
}

func (pr *ProvenanceRenderer) Render() string {
	l := pr.Ledger
	l.mu.Lock()
	defer l.mu.Unlock()

	contentWidth := pr.Width - 4
	if contentWidth < 36 {
		contentWidth = 36
	}

	var b strings.Builder

	pr.writeTopBorder(&b, " Review Ledger ")

	if len(l.Changes) > 0 {
		for _, c := range l.Changes {
			line := fmt.Sprintf("%s  Change: %s", c.ID, c.File)
			snippet := c.Snippet
			if snippet != "" {
				if len(snippet) > int(float64(contentWidth)*0.5) {
					snippet = snippet[:int(float64(contentWidth)*0.5)-3] + "..."
				}
				line += fmt.Sprintf(" (%s)", snippet)
			}
			pr.writeRow(&b, line, contentWidth)
		}
	}

	if len(l.Risks) > 0 {
		for _, r := range l.Risks {
			line := fmt.Sprintf("%s  Risk [%s]:", r.ID, r.Category)
			loc := r.File
			if r.Line > 0 {
				loc = fmt.Sprintf("%s:%d", r.File, r.Line)
			}
			if len(r.Desc) > 0 {
				desc := r.Desc
				maxDesc := contentWidth - len(line) - len(loc) - 5
				if maxDesc < 20 {
					maxDesc = 20
				}
				if len(desc) > maxDesc {
					desc = desc[:maxDesc-3] + "..."
				}
				line = fmt.Sprintf("%s %s — %s", line, loc, desc)
			} else {
				line = fmt.Sprintf("%s %s", line, loc)
			}
			pr.writeRow(&b, line, contentWidth)
		}
	}

	if len(l.Hypotheses) > 0 {
		for _, h := range l.Hypotheses {
			line := fmt.Sprintf("%s  Hypothesis: %s", h.ID, h.Hypothesis)
			pr.writeRow(&b, line, contentWidth)
		}
	}

	if len(l.Verifications) > 0 {
		for _, v := range l.Verifications {
			line := fmt.Sprintf("%s  Plan: %s", v.ID, v.Plan)
			pr.writeRow(&b, line, contentWidth)
		}
	}

	if len(l.Evidences) > 0 {
		for _, e := range l.Evidences {
			line := fmt.Sprintf("%s  Evidence [%s]: %s (Confidence: %s)",
				e.ID, string(e.Type), string(e.Status), string(e.Confidence))
			pr.writeRow(&b, line, contentWidth)
		}
	}

	pr.writeSep(&b, contentWidth)

	statusLine := fmt.Sprintf("Review Status: %s", string(l.Status))
	unsupported := l.countUnresolved()
	if unsupported > 0 {
		if unsupported == 1 {
			statusLine += " (1 risk requires runtime manual check)"
		} else {
			statusLine += fmt.Sprintf(" (%d risks require runtime manual check)", unsupported)
		}
	}
	pr.writeRow(&b, statusLine, contentWidth)

	pr.writeBottomBorder(&b)

	return b.String()
}

func (pr *ProvenanceRenderer) RenderCompact() string {
	return pr.Ledger.FormatCompact()
}

func (pr *ProvenanceRenderer) writeTopBorder(b *strings.Builder, label string) {
	boxWidth := pr.Width
	prefix := "┌─" + label
	b.WriteString(prefix)
	fill := boxWidth - runewidth.StringWidth(prefix) - 1
	if fill > 0 {
		b.WriteString(strings.Repeat("─", fill))
	}
	b.WriteString("┐\n")
}

func (pr *ProvenanceRenderer) writeBottomBorder(b *strings.Builder) {
	b.WriteString("└" + strings.Repeat("─", pr.Width-2) + "┘")
}

func (pr *ProvenanceRenderer) writeSep(b *strings.Builder, contentWidth int) {
	b.WriteString("│ ")
	b.WriteString(strings.Repeat("─", contentWidth))
	b.WriteString(" │\n")
}

func (pr *ProvenanceRenderer) writeRow(b *strings.Builder, text string, contentWidth int) {
	textWidth := runewidth.StringWidth(text)
	if textWidth > contentWidth {
		if contentWidth > 3 {
			var trimmed strings.Builder
			w := 0
			for _, r := range text {
				rw := runewidth.RuneWidth(r)
				if w+rw > contentWidth-3 {
					break
				}
				trimmed.WriteRune(r)
				w += rw
			}
			trimmed.WriteString("...")
			text = trimmed.String()
		} else {
			var trimmed strings.Builder
			w := 0
			for _, r := range text {
				rw := runewidth.RuneWidth(r)
				if w+rw > contentWidth {
					break
				}
				trimmed.WriteRune(r)
				w += rw
			}
			text = trimmed.String()
		}
	} else {
		pad := contentWidth - textWidth
		if pad > 0 {
			text += strings.Repeat(" ", pad)
		}
	}
	b.WriteString("│ ")
	b.WriteString(text)
	b.WriteString(" │\n")
}

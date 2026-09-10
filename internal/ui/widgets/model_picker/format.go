package model_picker

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Strict single-line tabular layout helpers. Zero I/O.
//
// Pricing sanitizer prevents IEEE 754 float bloat ($0.099999999) by
// formatting to 2-4 significant decimals based on magnitude. Truncation
// helpers guarantee every model row occupies EXACTLY 1 physical line.

// formatPriceVal formats a single $/M cost value.
func formatPriceVal(v float64) string {
	if v == 0 {
		return "0"
	}
	if v < 0.01 {
		return fmt.Sprintf("%.3f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

// formatPricing renders "$prompt/$completion" or "free" for zero pricing.
func formatPricing(prompt, completion float64) string {
	if prompt == 0 && completion == 0 {
		return "free"
	}
	s := fmt.Sprintf("$%s/$%s", formatPriceVal(prompt), formatPriceVal(completion))
	return strings.ReplaceAll(s, "\n", " ")
}

// formatContextWindow renders a context window with dynamic units:
// 1000000+ → "1M"/"2M"/"1.5M", 1000+ → "128k"/"256k", else raw digits.
// Non-positive windows render "-" (unknown). Zero I/O.
func formatContextWindow(tokens int) string {
	var s string
	switch {
	case tokens <= 0:
		s = "-"
	case tokens >= 1_000_000:
		val := float64(tokens) / 1_000_000.0
		if val == float64(int(val)) {
			s = fmt.Sprintf("%dM", int(val))
		} else {
			s = fmt.Sprintf("%.1fM", val)
		}
	case tokens >= 1_000:
		s = fmt.Sprintf("%dk", tokens/1000)
	default:
		s = fmt.Sprintf("%d", tokens)
	}
	return strings.ReplaceAll(s, "\n", " ")
}

// padVisible pads a (potentially ANSI-styled) string with trailing spaces so
// its visible width equals exactly w cells. Uses lipgloss.Width for correct
// measurement of styled and wide-character content.
//
//nolint:unused // retained for spec compatibility and potential external use
func padVisible(s string, w int) string {
	vw := lipgloss.Width(s)
	if vw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-vw)
}

// truncateStyled cuts an ANSI-styled line to at most w visible cells while
// preserving escape sequences. Guarantees single-line width without wrapping.
func truncateStyled(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	var b strings.Builder
	vis := 0
	i := 0
	for i < len(s) {
		// Pass through ANSI escape sequences without counting width.
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++ // include 'm'
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		// Decode one rune.
		r, size := rune(s[i]), 1
		if s[i] >= 0x80 {
			decoded := []rune(s[i:])
			if len(decoded) > 0 {
				r = decoded[0]
				size = len(string(r))
			}
		}
		rw := lipgloss.Width(string(r))
		if vis+rw > w {
			break
		}
		b.WriteRune(r)
		vis += rw
		i += size
		// Newlines must never survive: single physical line only.
		if r == '\n' {
			break
		}
	}
	return b.String()
}

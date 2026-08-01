package ui

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
)

// catppuccinMochaStyle is the Catppuccin Mocha syntax style loaded from
// chroma's built-in registry. It provides truecolor ANSI escape sequences
// for every token type, giving high-contrast syntax highlighting that
// matches the Catppuccin Mocha dark palette.
var catppuccinMochaStyle = styles.Get("catppuccin-mocha")

// sgrForToken returns the ANSI SGR escape sequence for a chroma token type,
// using the Catppuccin Mocha palette. It embeds bold/italic modifiers and
// truecolor foreground (and background when set). Unknown token types fall
// back to the default foreground colour (#cdd6f4).
func sgrForToken(t chroma.TokenType) string {
	if catppuccinMochaStyle == nil {
		return "\x1b[38;2;205;214;244m"
	}
	e := catppuccinMochaStyle.Get(t)
	if e.IsZero() {
		return "\x1b[38;2;205;214;244m"
	}
	var codes []string
	if e.Bold == chroma.Yes {
		codes = append(codes, "1")
	}
	if e.Italic == chroma.Yes {
		codes = append(codes, "3")
	}
	if e.Colour.IsSet() {
		codes = append(codes, fmt.Sprintf("38;2;%d;%d;%d", e.Colour.Red(), e.Colour.Green(), e.Colour.Blue()))
	}
	if e.Background.IsSet() {
		codes = append(codes, fmt.Sprintf("48;2;%d;%d;%d", e.Background.Red(), e.Background.Green(), e.Background.Blue()))
	}
	if len(codes) == 0 {
		return "\x1b[38;2;205;214;244m"
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

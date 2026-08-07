package compiler

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Normalizer is a language-agnostic text cleaner. It does NOT translate,
// rewrite or match words in any language: it only enforces well-formed
// Unicode so downstream semantic extraction is reliable.
//
//   - Unicode NFC normalization (canonical composition) — "lam la\u0302i"
//     becomes "lam lâi" regardless of how the client composed the accents.
//   - Non-printable control characters are stripped (NUL, ESC, ...), while
//     whitespace control characters survive to be collapsed later.
//   - Redundant whitespace is collapsed to single spaces.
//
// Every valid UTF-8 sequence (Latin, CJK, Arabic, Emoji, ...) is preserved
// verbatim; the Normalizer never rewrites raw words.
type Normalizer struct{}

// NewNormalizer builds a Normalizer. It carries no configuration: the
// transformation is fully defined by the Unicode standard.
func NewNormalizer() *Normalizer {
	return &Normalizer{}
}

// Process normalises raw into a canonical, machine-friendly form: NFC
// normalized, control-character cleaned and whitespace-collapsed. Applying
// Process twice is idempotent, and empty input stays empty.
func (n *Normalizer) Process(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	nfc := norm.NFC.String(raw)
	var b strings.Builder
	for _, r := range nfc {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return collapseSpace(b.String())
}

// collapseSpace trims and collapses every run of whitespace into a single
// space.
func collapseSpace(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

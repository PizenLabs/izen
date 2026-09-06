package context

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// UntrustedContextWrapper is the sole sanctioned envelope for workspace-derived
// data injected into any model prompt.
type UntrustedContextWrapper struct {
	Path      string `json:"path"`
	Hash      string `json:"hash"`
	Scope     string `json:"scope"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

// WrapUntrusted is the ONLY constructor. It hashes content and freezes the scope.
func WrapUntrusted(path, scope, content string, truncated bool) UntrustedContextWrapper {
	h := sha256.Sum256([]byte(content))
	return UntrustedContextWrapper{
		Path:      path,
		Scope:     scope,
		Content:   content,
		Truncated: truncated,
		Hash:      hex.EncodeToString(h[:]),
	}
}

// WireFormat renders the wrapper as the canonical XML envelope for prompt injection.
func (w UntrustedContextWrapper) WireFormat() string {
	return fmt.Sprintf(
		`<untrusted_context path=%q hash=%q scope=%q>%s</untrusted_context>`,
		w.Path, w.Hash, w.Scope, xmlEscape(w.Content),
	)
}

func xmlEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ParseUntrusted extracts wrappers from a rendered prompt for audit/testing.
// It is NOT used at runtime prompt construction — only for verification.
func ParseUntrusted(prompt string) ([]UntrustedContextWrapper, error) {
	// Minimal stub for test verification — not normative runtime path.
	var out []UntrustedContextWrapper
	_ = prompt
	return out, nil
}

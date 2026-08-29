package execution

import (
	"path/filepath"
	"regexp"
	"strings"
)

// ── Markup Syntax Preflight Relaxation ──────────────────────────────────────
//
// The preflight AST gate (ValidateDocumentSyntax → EvaluateScope → ASTStatus)
// must distinguish a CATASTROPHIC syntax failure from PERMISSIVE markup. A
// standard web template routinely omits optional closing tags (<img>, <br>,
// <li>, <p>), and the lenient HTML5 parser silently repairs a lone "<" left in
// text (a "<" that never opens a real element). Neither is AST corruption — a
// document with loose HTML5 tags is still structurally sound and executable.
//
// The gate stays strict on the boundaries that CANNOT be silently repaired:
//   - truncated raw-text blocks (<script>, <style>, <textarea>, <title>,
//     <noscript>, <template> with no closing tag) — a truncated <script> or
//     <style> swallows the rest of the document;
//   - content the WHATWG parser itself cannot parse (binary / corrupt text);
//   - an unterminated quoted attribute value or a dangling "<" at EOF.
//
// Permissive markup failures are downgraded to a pass at the PREFLIGHT gate
// only. The V3 artifact pipeline (v3Artifact.ValidateContent) that validates
// model OUTPUT stays strict — generated artifacts must still be well-formed.

// markupExtensions maps file extensions to the canonical markup validator tag.
// Text templates that render to HTML are treated as markup too.
var markupExtensions = map[string]bool{
	".html":  true,
	".htm":   true,
	".xhtml": true,
	".shtml": true,
	".tpl":   true,
	".tmpl":  true,
}

// IsMarkupTarget reports whether target's format is markup (HTML or an HTML
// template). Markup targets get the permissive-tag preflight relaxation and
// the bounded-patch budget accounting for targeted modification prompts.
func IsMarkupTarget(target string) bool {
	if target == "" {
		return false
	}
	return markupExtensions[strings.ToLower(filepath.Ext(target))]
}

// SyntaxFailureKind classifies a document syntax failure for the preflight
// AST gate.
type SyntaxFailureKind int

const (
	// SyntaxFailureNone: the document parses cleanly (or has no validator).
	SyntaxFailureNone SyntaxFailureKind = iota
	// SyntaxFailureCatastrophic: severe unclosed structural boundaries or
	// unparseable content — the document MUST be treated as AST-corrupt.
	SyntaxFailureCatastrophic
	// SyntaxFailurePermissive: loose/optional markup a lenient HTML5 parser
	// silently repairs — NOT corruption for a standard web template.
	SyntaxFailurePermissive
)

// String returns the stable classification label.
func (k SyntaxFailureKind) String() string {
	switch k {
	case SyntaxFailureCatastrophic:
		return "catastrophic"
	case SyntaxFailurePermissive:
		return "permissive"
	default:
		return "none"
	}
}

// permissiveMarkupRe matches the deterministic diagnostics the lenient HTML5
// scan emits for markup that a browser silently tolerates: a "<" in text that
// never opens a real element and is left unrepaired at the end of the scan
// ("unterminated tag at line N"). Raw-text truncation, unterminated quotes and
// dangling openers are NOT in this set — they are severe structural failures.
var permissiveMarkupRe = regexp.MustCompile(`(?i)unterminated tag at line`)

// ClassifySyntaxFailure maps a document syntax error onto the preflight
// severity for target's format. It is the single authority on which failures
// are AST corruption and which are permissive markup:
//
//   - nil error → SyntaxFailureNone;
//   - a MARKUP target whose validator failure is a permissive loose-tag
//     diagnostic → SyntaxFailurePermissive (relaxed — NOT corrupt);
//   - every other failure (raw-text block truncation, unparseable content,
//     unterminated quotes, non-markup formats) → SyntaxFailureCatastrophic.
func ClassifySyntaxFailure(target string, err error) SyntaxFailureKind {
	if err == nil {
		return SyntaxFailureNone
	}
	if IsMarkupTarget(target) && permissiveMarkupRe.MatchString(err.Error()) {
		return SyntaxFailurePermissive
	}
	return SyntaxFailureCatastrophic
}

// IsCatastrophicSyntaxFailure reports whether a document syntax error is a
// severe structural failure for target's format (AST-corrupt), as opposed to
// permissive markup. It backs the preflight gate's ASTStatus decision.
func IsCatastrophicSyntaxFailure(target string, err error) bool {
	return ClassifySyntaxFailure(target, err) == SyntaxFailureCatastrophic
}

// EstimateBoundedPatchTokens computes the estimated generation cost of a
// TARGETED modification on target using the bounded patch multiplier
// (target_bytes/4 × BoundedPatchTokenMultiplier). It applies ONLY to markup
// targets — a non-markup target reports applies=false so callers keep the
// full-rewrite accounting.
func EstimateBoundedPatchTokens(targetBytes int, target string) (estimated int, applies bool) {
	if !IsMarkupTarget(target) || targetBytes <= 0 {
		return 0, false
	}
	return (targetBytes / 4) * BoundedPatchTokenMultiplier, true
}

// Package target provides canonical target extraction and resolution.
//
// The ExtractReferences function is the single canonical lexer for @-referenced
// file targets in prompts. Every admission-time and strategy-phase target
// resolution MUST delegate here to avoid tokenizer drift: quoted paths
// (@"src/my component.tsx") and standard @path references behave identically
// across admission and strategy phases.
package target

import (
	"regexp"
	"strings"
)

// extractRefRE matches @-referenced file targets in a prompt. It handles both:
//   - Standard references: @path/to/file.go, @index.html, @src/my-pkg
//   - Quoted references: @"src/my component.tsx", @"path with spaces.txt"
//
// The pattern captures the path inside quotes (for quoted refs) or the
// contiguous run of path-valid characters (for standard refs). Path-valid
// characters include letters, digits, underscores, dots, slashes, and hyphens.
var extractRefRE = regexp.MustCompile(`@(?:"([^"]+)"|([A-Za-z0-9_./\-]+))`)

// Reference is one extracted @-referenced target from a prompt.
type Reference struct {
	// Raw is the exact matched text (without the leading @, without quotes).
	Raw string
	// Quoted reports whether the reference used the @"..." syntax.
	Quoted bool
}

// ExtractReferences extracts @-referenced file targets from a prompt and
// returns them in first-appearance order, de-duplicated. It is the single
// canonical lexer for this task — both admission (handlers.resolveTargets)
// and strategy (strategy.collectTargets) phases delegate here so quoted and
// standard @path references behave identically everywhere.
//
// De-duplication is case-sensitive and preserves the first spelling seen, so
// @File.go and @file.go are distinct entries (case-insensitive resolution is
// owned by the Resolver, never the extractor).
func ExtractReferences(prompt string) []Reference {
	seen := make(map[string]bool)
	var out []Reference
	for _, m := range extractRefRE.FindAllStringSubmatch(prompt, -1) {
		// m[1] is the quoted path (without quotes), m[2] is the standard path.
		var raw string
		quoted := false
		if m[1] != "" {
			raw = strings.TrimSpace(m[1])
			quoted = true
		} else {
			raw = strings.TrimSpace(m[2])
		}
		if raw == "" || raw == "/" {
			continue
		}
		if seen[raw] {
			continue
		}
		seen[raw] = true
		out = append(out, Reference{Raw: raw, Quoted: quoted})
	}
	return out
}

// ExtractTargetPaths is a convenience that returns only the raw path strings
// from ExtractReferences. It is the drop-in replacement for the localized
// regex-based extraction that previously lived in handlers.resolveTargets and
// pkg/engine/strategy.builtin.extractTargets.
func ExtractTargetPaths(prompt string) []string {
	refs := ExtractReferencePaths(prompt)
	return refs
}

// ExtractReferencePaths returns the raw path strings of every @-reference in
// prompt, in first-appearance order, de-duplicated.
func ExtractReferencePaths(prompt string) []string {
	refs := ExtractReferences(prompt)
	if len(refs) == 0 {
		return nil
	}
	paths := make([]string, 0, len(refs))
	for _, r := range refs {
		paths = append(paths, r.Raw)
	}
	return paths
}

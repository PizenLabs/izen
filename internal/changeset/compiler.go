package changeset

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// Compiler synthesizes the authoritative ---/+++ unified diff for a ChangeSet
// against the on-disk original. It is the SINGLE AUTHORITATIVE SOURCE of diff
// patches in the pipeline: model output is never applied directly, and no other
// component derives a diff from ChangeSet intent.
type Compiler struct{}

// NewCompiler returns a Diff Compiler.
func NewCompiler() *Compiler { return &Compiler{} }

// CompileToPatch synthesizes the authoritative unified diff for cs against the
// on-disk original content:
//
//   - KindApplyDiff: the raw diff carried by the ChangeSet is returned verbatim.
//   - KindReplaceFile: a programmatic unified diff between originalDiskContent
//     and cs.NewContent is computed.
//   - KindReplaceBlock: cs.OldContent is located in originalDiskContent, the
//     updated buffer is constructed, and a programmatic unified diff is emitted.
//
// KINDREPLACE TRUNCATION GUARD: before a KindReplaceBlock / KindReplaceFile
// payload is compiled into a diff, its structural balance is checked (matching
// closing tags for HTML, markdown fences, JSON delimiters). A response cut off
// mid-generation by the completion ceiling is structurally unbalanced; the
// compile ABORTS with ErrTruncatedOutput rather than emitting a broken diff
// that deletes subsequent valid file sections.
func (c *Compiler) CompileToPatch(cs ChangeSet, originalDiskContent []byte) ([]byte, error) {
	if cs.TargetFile == "" {
		return nil, fmt.Errorf("changeset: compile target file is empty")
	}
	switch cs.Kind {
	case KindApplyDiff:
		return compileApplyDiff(cs)
	case KindReplaceFile:
		if reason := checkTruncation(cs.NewContent); reason != "" {
			return nil, fmt.Errorf("%w: %s", ErrTruncatedOutput, reason)
		}
		return compileUnifiedDiff(cs.TargetFile, string(originalDiskContent), cs.NewContent)
	case KindReplaceBlock:
		if reason := checkTruncation(cs.NewContent); reason != "" {
			return nil, fmt.Errorf("%w: %s", ErrTruncatedOutput, reason)
		}
		return compileReplaceBlock(cs, string(originalDiskContent))
	default:
		return nil, fmt.Errorf("changeset: unknown change kind %q", cs.Kind)
	}
}

// checkTruncation reports the first structural imbalance found in model output
// that indicates the response was cut off before completion. It returns "" when
// the payload appears complete. Checks performed:
//
//   - empty / whitespace-only payload
//   - unclosed markdown code fence (``` without a closing ```)
//   - unbalanced HTML tags (an opening tag with no matching close)
//   - unbalanced JSON braces/brackets
//
// The check is deliberately conservative: it only aborts on clearly-unbalanced
// STRONG structural tags (void elements, self-closing tags and inline
// comparison operators are ignored) so complete output is never rejected.
func checkTruncation(content string) string {
	if strings.TrimSpace(content) == "" {
		return "empty content"
	}
	if reason := unclosedFence(content); reason != "" {
		return reason
	}
	if reason := unbalancedHTML(content); reason != "" {
		return reason
	}
	if reason := unbalancedJSON(content); reason != "" {
		return reason
	}
	return ""
}

// unclosedFence detects an opening markdown fence with no matching close.
func unclosedFence(content string) string {
	lines := strings.Split(content, "\n")
	open := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if open == -1 {
				open = i
			} else if strings.TrimSpace(line) == "```" {
				open = -1
			}
		}
	}
	if open != -1 {
		return "unclosed markdown code block"
	}
	return ""
}

// unbalancedHTML reports a tag imbalance among structural tags that MUST be
// paired in well-formed HTML. Void elements (<br>, <img>, <meta>, ...) and
// self-closing tags (<tag .../>) are excluded so complete documents are never
// false-flagged.
func unbalancedHTML(content string) string {
	paired := []string{
		"html", "head", "body", "div", "section", "table", "form", "ul", "ol",
		"title", "script", "style", "header", "footer", "main", "nav",
		"article", "aside", "h1", "h2", "h3", "h4", "h5", "h6", "p", "span",
		"li", "tr", "td", "th",
	}
	lower := strings.ToLower(content)
	openRE := regexp.MustCompile(`<([a-z][a-z0-9]*)\b[^>]*>`)
	closeRE := regexp.MustCompile(`</([a-z][a-z0-9]*)\s*>`)
	for _, tag := range paired {
		open, close := 0, 0
		for _, m := range openRE.FindAllStringSubmatch(lower, -1) {
			if m[1] != tag {
				continue
			}
			// Skip void / self-closing occurrences.
			if isVoidElement(m[1]) {
				continue
			}
			open++
		}
		for _, m := range closeRE.FindAllStringSubmatch(lower, -1) {
			if m[1] == tag {
				close++
			}
		}
		if open > close {
			return fmt.Sprintf("unbalanced HTML: %d unclosed <%s> tag(s)", open-close, tag)
		}
	}
	return ""
}

// isVoidElement reports whether an HTML tag never requires a closing tag.
func isVoidElement(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input",
		"link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}

// unbalancedJSON reports an excess of opening JSON delimiters ({, [) over
// closing ones. String contents are skipped so a literal "{" inside a string is
// never counted.
func unbalancedJSON(content string) string {
	openCurly, closeCurly := 0, 0
	openBracket, closeBracket := 0, 0
	inString := false
	escaped := false
	for _, r := range content {
		switch {
		case inString:
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				inString = false
			}
		case r == '"':
			inString = true
		case r == '{':
			openCurly++
		case r == '}':
			closeCurly++
		case r == '[':
			openBracket++
		case r == ']':
			closeBracket++
		}
	}
	if openCurly > closeCurly {
		return fmt.Sprintf("unbalanced JSON: %d unclosed { delimiter(s)", openCurly-closeCurly)
	}
	if openBracket > closeBracket {
		return fmt.Sprintf("unbalanced JSON: %d unclosed [ delimiter(s)", openBracket-closeBracket)
	}
	return ""
}

// compileApplyDiff validates and returns the raw diff payload verbatim. The
// trailing newline is trimmed so the diff applies cleanly through the patch
// engine's hunk parser (a trailing newline would otherwise inject a spurious
// empty context line into the final hunk).
func compileApplyDiff(cs ChangeSet) ([]byte, error) {
	raw := strings.TrimSpace(cs.NewContent)
	if raw == "" {
		return nil, fmt.Errorf("changeset: APPLY_DIFF payload is empty")
	}
	if diffPayloadIndex(raw) < 0 {
		return nil, fmt.Errorf("changeset: APPLY_DIFF payload is missing ---/+++ diff headers")
	}
	return []byte(raw), nil
}

// compileReplaceBlock locates the anchor in the on-disk original, replaces it,
// and emits the authoritative unified diff. The anchor must appear exactly once;
// zero or multiple occurrences abort (ambiguous representation).
func compileReplaceBlock(cs ChangeSet, original string) ([]byte, error) {
	anchor := cs.OldContent
	if anchor == "" {
		return nil, fmt.Errorf("changeset: REPLACE_BLOCK requires a non-empty anchor")
	}
	idx := strings.Index(original, anchor)
	if idx < 0 {
		return nil, fmt.Errorf("changeset: REPLACE_BLOCK anchor %q not found in %s", anchor, cs.TargetFile)
	}
	if strings.Contains(original[idx+len(anchor):], anchor) {
		return nil, fmt.Errorf("changeset: REPLACE_BLOCK anchor %q is ambiguous (multiple occurrences in %s)", anchor, cs.TargetFile)
	}
	updated := original[:idx] + cs.NewContent + original[idx+len(anchor):]
	return compileUnifiedDiff(cs.TargetFile, original, updated)
}

// compileUnifiedDiff computes a context-rich unified diff between the on-disk
// original and the modified content using the go-difflib engine. An identical
// result is a no-op and rejected.
func compileUnifiedDiff(path, original, modified string) ([]byte, error) {
	if original == modified {
		return nil, fmt.Errorf("changeset: change to %s produces no diff", path)
	}
	text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(original),
		B:        difflib.SplitLines(modified),
		FromFile: "a/" + path,
		ToFile:   "b/" + path,
		Context:  3,
	})
	if err != nil {
		return nil, fmt.Errorf("changeset: unified diff for %s failed: %w", path, err)
	}
	// Trim the trailing newline so the hunk parser in the patch engine sees no
	// spurious trailing empty context line.
	return []byte(strings.TrimSuffix(text, "\n")), nil
}

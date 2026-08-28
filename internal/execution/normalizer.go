package execution

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NormalizingArtifactValidator decorates an underlying ArtifactValidator
// to compile unstructured / raw LLM outputs into strictly compliant
// BoundedPatch artifacts without weakening P0 execution safety.
//
// It attempts the inner validator first; on failure it runs the
// DetectAndNormalize pipeline (fenced-code extraction, unified-diff
// normalization, full-file to bounded-patch synthesis via Myers LCS)
// and re-validates the normalized bytes through the inner P0 contract.
type NormalizingArtifactValidator struct {
	inner      ArtifactValidator
	root       string
	loadTarget func(target string) (string, error)
}

// NewNormalizingArtifactValidator returns a decorator wrapping inner.
// The decorator reads target content from the filesystem relative to
// the current working directory by default. Use WithRoot / SetRoot to
// bind it to a workspace root (e.g. RuntimeExecutor's root).
func NewNormalizingArtifactValidator(inner ArtifactValidator) *NormalizingArtifactValidator {
	return &NormalizingArtifactValidator{inner: inner}
}

// NewNormalizingArtifactValidatorWithRoot returns a decorator bound to root.
func NewNormalizingArtifactValidatorWithRoot(root string, inner ArtifactValidator) *NormalizingArtifactValidator {
	return &NormalizingArtifactValidator{inner: inner, root: root}
}

// WithRoot binds the validator to root and returns itself for chaining.
func (n *NormalizingArtifactValidator) WithRoot(root string) *NormalizingArtifactValidator {
	n.root = root
	return n
}

// SetRoot binds the validator to root.
func (n *NormalizingArtifactValidator) SetRoot(root string) {
	n.root = root
}

// SetTargetLoader overrides the target-content loader (test seam).
func (n *NormalizingArtifactValidator) SetTargetLoader(fn func(target string) (string, error)) {
	n.loadTarget = fn
}

var _ ArtifactValidator = (*NormalizingArtifactValidator)(nil)

// validateSearchPatchFullFile checks if a SEARCH/REPLACE patch yields a
// syntactically valid full file when applied. This is the correct
// validation for Go patches where the REPLACE payload alone is not a
// valid standalone file (e.g. a single type declaration without package).
func (n *NormalizingArtifactValidator) validateSearchPatchFullFile(rawStr, target, targetContent string) (*BoundedPatch, bool) {
	if targetContent == "" || !strings.Contains(rawStr, "<<<<<<< SEARCH") {
		return nil, false
	}
	blocks := ParseSearchReplaceBlocks(rawStr)
	if len(blocks) == 0 {
		return nil, false
	}
	patched, ok := ApplySearchReplaceBlocks(targetContent, blocks)
	if !ok {
		return nil, false
	}
	tag := ValidatorTagForPath(target)
	if tag == "" {
		// Unregistered language: any applicable patch is valid.
		bp := &BoundedPatch{
			Target:  target,
			Search:  blocks[0].search,
			Replace: blocks[0].replace,
			Content: []byte(rawStr),
			Raw:     []byte(rawStr),
		}
		return bp, true
	}
	pipeline := NewV3ArtifactPipeline()
	gate := pipeline.ValidateContent(target, []byte(patched), 0)
	if gate.Passed {
		bp := &BoundedPatch{
			Target:  target,
			Search:  blocks[0].search,
			Replace: blocks[0].replace,
			Content: []byte(rawStr),
			Raw:     []byte(rawStr),
		}
		return bp, true
	}
	return nil, false
}

// ValidateArtifact implements ArtifactValidator.
//
//  1. Fail-closed truncation check.
//  2. Inner validator fast-path; on success still validates anchor
//     uniqueness and, for full-file rewrites, attempts minimal synthesis.
//  3. On inner failure (format), runs DetectAndNormalize and re-validates.
func (n *NormalizingArtifactValidator) ValidateArtifact(raw []byte, target string) (*BoundedPatch, error) {
	if n == nil || n.inner == nil {
		return nil, fmt.Errorf("%w: validator not configured", ErrFormatRejected)
	}
	rawStr := strings.TrimSpace(string(raw))
	if rawStr == "" {
		return nil, fmt.Errorf("%w: empty artifact", ErrFormatRejected)
	}
	// Fail-closed: truncation markers are never valid artifacts.
	if isTruncationMarker(rawStr) {
		return nil, fmt.Errorf("%w: truncation marker detected", ErrFormatRejected)
	}

	// Fast-path: try inner first.
	bp, err := n.inner.ValidateArtifact(raw, target)
	if err == nil {
		// If the raw is already a strictly compliant patch (SEARCH/REPLACE or diff),
		// validate anchor uniqueness and return.
		hasSearch := strings.Contains(rawStr, "<<<<<<< SEARCH")
		hasDiff := strings.Contains(rawStr, "@@")
		targetContent := n.loadTargetContent(target)
		if hasSearch || hasDiff {
			if targetContent != "" && hasSearch {
				blocks := ParseSearchReplaceBlocks(rawStr)
				for _, b := range blocks {
					sl := strings.Split(b.search, "\n")
					if _, _, aerr := ResolveAnchors(sl, targetContent); aerr != nil {
						return nil, aerr
					}
				}
			}
			return bp, nil
		}
		// Full-file valid case (e.g. HTML/JSON/Go) — attempt minimal synthesis
		// so the result is a bounded SEARCH/REPLACE patch, not a full rewrite.
		if targetContent != "" && targetContent != rawStr {
			// Only synthesize when raw looks like a full-file rewrite (no markers)
			// and target exists. Use fence-aware candidate if raw contained fences.
			candidate := rawStr
			if strings.Contains(rawStr, "```") {
				if extracted, ok := ExtractCodeBlockContent(rawStr); ok && strings.TrimSpace(extracted) != "" {
					candidate = strings.TrimSpace(extracted)
				} else {
					candidate = stripFences(rawStr)
				}
				if isTruncationMarker(candidate) {
					return nil, fmt.Errorf("%w: truncation marker in extracted code", ErrFormatRejected)
				}
			}
			// If candidate is already a patch/diff skip synthesis (handled above).
			if !strings.Contains(candidate, "<<<<<<< SEARCH") && !strings.Contains(candidate, "@@") {
				if norm, nerr := DetectAndNormalize(raw, targetContent); nerr == nil && len(norm) > 0 && strings.Contains(string(norm), "<<<<<<< SEARCH") {
					if nbp, nerr2 := n.inner.ValidateArtifact(norm, target); nerr2 == nil {
						// Validate anchors for synthesized patch.
						blocks := ParseSearchReplaceBlocks(string(norm))
						anchorOK := true
						for _, b := range blocks {
							sl := strings.Split(b.search, "\n")
							if _, _, aerr := ResolveAnchors(sl, targetContent); aerr != nil {
								anchorOK = false
								break
							}
						}
						if anchorOK {
							return nbp, nil
						}
					}
				}
			}
		}
		return bp, nil
	}

	// Scope violations are never normalized.
	if errors.Is(err, ErrScopeViolation) {
		return nil, err
	}

	targetContent := n.loadTargetContent(target)

	// For Go SEARCH patches where the REPLACE payload alone is not a
	// valid standalone file but the full patched file is valid, the
	// inner validator incorrectly rejects. Try full-file validation.
	if errors.Is(err, ErrFormatRejected) && targetContent != "" && strings.Contains(rawStr, "<<<<<<< SEARCH") {
		if bp2, ok := n.validateSearchPatchFullFile(rawStr, target, targetContent); ok {
			blocks := ParseSearchReplaceBlocks(rawStr)
			for _, b := range blocks {
				sl := strings.Split(b.search, "\n")
				if _, _, aerr := ResolveAnchors(sl, targetContent); aerr != nil {
					return nil, aerr
				}
			}
			return bp2, nil
		}
	}

	normalized, nerr := DetectAndNormalize(raw, targetContent)
	if nerr != nil {
		return nil, nerr
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("%w: normalization produced empty artifact", ErrFormatRejected)
	}
	// Fail-closed truncation check on normalized content as well.
	if isTruncationMarker(string(normalized)) {
		return nil, fmt.Errorf("%w: truncation marker in normalized artifact", ErrFormatRejected)
	}
	// Validate anchor uniqueness for any synthesized SEARCH blocks.
	if targetContent != "" {
		if blocks := ParseSearchReplaceBlocks(string(normalized)); len(blocks) > 0 {
			for _, b := range blocks {
				if strings.TrimSpace(b.search) == "" {
					return nil, fmt.Errorf("%w: empty SEARCH anchor", ErrAmbiguousAnchor)
				}
				sl := strings.Split(b.search, "\n")
				if _, _, aerr := ResolveAnchors(sl, targetContent); aerr != nil {
					return nil, aerr
				}
			}
		}
	}

	bp2, err2 := n.inner.ValidateArtifact(normalized, target)
	if err2 == nil {
		return bp2, nil
	}
	// If normalized is a SEARCH patch that fails Go validation of REPLACE
	// alone but the full patched file is valid, accept it.
	if errors.Is(err2, ErrFormatRejected) && targetContent != "" && strings.Contains(string(normalized), "<<<<<<< SEARCH") {
		if bp3, ok := n.validateSearchPatchFullFile(string(normalized), target, targetContent); ok {
			return bp3, nil
		}
	}
	return nil, err2
}

func (n *NormalizingArtifactValidator) loadTargetContent(target string) string {
	if n.loadTarget != nil {
		if c, err := n.loadTarget(target); err == nil {
			return c
		}
		return ""
	}
	// Try root-bound read, then fallback to direct.
	var data []byte
	var err error
	if n.root != "" {
		data, err = os.ReadFile(filepath.Join(n.root, filepath.FromSlash(target)))
		if err == nil {
			return string(data)
		}
	}
	data, err = os.ReadFile(filepath.FromSlash(target))
	if err == nil {
		return string(data)
	}
	// Try with "." prefix
	data, err = os.ReadFile(filepath.Join(".", filepath.FromSlash(target)))
	if err == nil {
		return string(data)
	}
	return ""
}

// DetectAndNormalize implements the three-mode normalization pipeline:
//
//	Mode 1: Raw Markdown / Fenced Code Extraction
//	Mode 2: Unified Diff Normalization
//	Mode 3: Full-File to Bounded Patch Synthesis (Myers LCS)
//
// It returns normalized bytes that should be re-validated through the
// inner P0 validator, or a typed error (ErrFormatRejected / ErrAmbiguousAnchor).
func DetectAndNormalize(raw []byte, targetContent string) ([]byte, error) {
	rawStr := strings.TrimSpace(string(raw))
	if rawStr == "" {
		return nil, fmt.Errorf("%w: empty artifact", ErrFormatRejected)
	}
	if isTruncationMarker(rawStr) {
		return nil, fmt.Errorf("%w: truncation marker detected", ErrFormatRejected)
	}

	candidate := rawStr

	// Mode 1: Fenced code extraction.
	if strings.Contains(rawStr, "```") {
		if extracted, ok := ExtractCodeBlockContent(rawStr); ok && strings.TrimSpace(extracted) != "" {
			candidate = strings.TrimSpace(extracted)
		} else if extracted2, ok2 := ExtractRawCodeBlock(rawStr); ok2 && strings.TrimSpace(extracted2) != "" {
			candidate = strings.TrimSpace(extracted2)
		} else {
			candidate = stripFences(rawStr)
		}
		if candidate == "" {
			return nil, fmt.Errorf("%w: fenced code extraction produced empty content", ErrFormatRejected)
		}
		if isTruncationMarker(candidate) {
			return nil, fmt.Errorf("%w: truncation marker in extracted code", ErrFormatRejected)
		}
	}

	// If candidate already contains explicit SEARCH/REPLACE, sanitize and return.
	if strings.Contains(candidate, "<<<<<<< SEARCH") {
		sanitized := SanitizeBoundedPatchResponse(candidate)
		if sanitized != "" {
			candidate = sanitized
		}
		if blocks := ParseSearchReplaceBlocks(candidate); len(blocks) > 0 {
			return []byte(candidate), nil
		}
		// Malformed SEARCH block.
		return nil, fmt.Errorf("%w: malformed SEARCH block", ErrFormatRejected)
	}

	// Mode 2: Unified diff normalization.
	if hasDiffHeaders(candidate) {
		normalized := normalizeUnifiedDiff(candidate)
		return []byte(normalized), nil
	}

	// Mode 3: Full-file to bounded patch synthesis.
	if targetContent != "" && candidate != "" && candidate != targetContent {
		if isTruncationMarker(candidate) {
			return nil, fmt.Errorf("%w: truncation marker in full-file content", ErrFormatRejected)
		}
		synthesized, err := synthesizePatch(targetContent, candidate)
		if err != nil {
			return nil, err
		}
		if len(synthesized) > 0 {
			return synthesized, nil
		}
	}

	// Fallback: if candidate is non-empty and targetContent is empty (new file),
	// return candidate as-is (the inner validator will handle creation).
	if candidate != "" {
		return []byte(candidate), nil
	}
	return nil, fmt.Errorf("%w: no valid normalization", ErrFormatRejected)
}

// ResolveAnchors scans targetContent for an exact contiguous block match
// of searchLines. If exact fails it attempts indentation-trimmed whitespace
// matching. Returns 1-indexed start/end lines on success, or a typed error.
//
//   - 0 matches -> ErrFormatRejected
//   - >1 matches -> ErrAmbiguousAnchor
func ResolveAnchors(searchLines []string, targetContent string) (int, int, error) {
	if len(searchLines) == 0 {
		return 0, 0, fmt.Errorf("%w: empty search", ErrAmbiguousAnchor)
	}
	// Filter out trailing empty line that Split produces for final newline.
	// Keep internal empties.
	// Count non-empty search? Empty search is already handled.
	searchBlock := strings.Join(searchLines, "\n")
	if strings.TrimSpace(searchBlock) == "" {
		return 0, 0, fmt.Errorf("%w: empty SEARCH anchor", ErrAmbiguousAnchor)
	}

	targetLines := strings.Split(targetContent, "\n")
	n := len(searchLines)
	var exact []int
	for i := 0; i+n <= len(targetLines); i++ {
		match := true
		for j := 0; j < n; j++ {
			if targetLines[i+j] != searchLines[j] {
				match = false
				break
			}
		}
		if match {
			exact = append(exact, i)
		}
	}
	if len(exact) == 1 {
		return exact[0] + 1, exact[0] + n, nil
	}
	if len(exact) > 1 {
		return 0, 0, fmt.Errorf("%w: anchor matches %d regions", ErrAmbiguousAnchor, len(exact))
	}
	// Exact failed -> try indentation-trimmed match.
	trimmedSearch := make([]string, n)
	for i, l := range searchLines {
		trimmedSearch[i] = strings.TrimSpace(l)
	}
	var trimmed []int
	for i := 0; i+n <= len(targetLines); i++ {
		match := true
		for j := 0; j < n; j++ {
			if strings.TrimSpace(targetLines[i+j]) != trimmedSearch[j] {
				match = false
				break
			}
		}
		if match {
			trimmed = append(trimmed, i)
		}
	}
	if len(trimmed) == 1 {
		return trimmed[0] + 1, trimmed[0] + n, nil
	}
	if len(trimmed) > 1 {
		return 0, 0, fmt.Errorf("%w: anchor matches %d regions (trimmed)", ErrAmbiguousAnchor, len(trimmed))
	}
	return 0, 0, fmt.Errorf("%w: anchor not found", ErrFormatRejected)
}

// ── helpers ────────────────────────────────────────────────────────────────

func isTruncationMarker(s string) bool {
	if strings.Contains(s, "// ...") || strings.Contains(s, "// …") ||
		strings.Contains(s, "/* ...") || strings.Contains(s, "# ...") {
		return true
	}
	// HTML comment truncation - with or without ellipsis
	if strings.Contains(s, "<!--") && strings.Contains(s, "...") {
		return true
	}
	if strings.Contains(strings.ToLower(s), "rest of content") {
		return true
	}
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "..." || t == "// ..." || t == "/* ... */" || t == "# ..." || t == "<!-- ... -->" {
			return true
		}
		if strings.Contains(t, "...") && (strings.Contains(strings.ToLower(t), "rest of") || strings.Contains(strings.ToLower(t), "remaining") || strings.Contains(strings.ToLower(t), "omitted") || strings.Contains(strings.ToLower(t), "content")) {
			return true
		}
		if strings.Contains(strings.ToLower(t), "rest of content") {
			return true
		}
	}
	return false
}

func stripFences(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func normalizeUnifiedDiff(content string) string {
	// If already has headers, return as-is.
	if strings.Contains(content, "--- a/") && strings.Contains(content, "+++ b/") {
		return content
	}
	// If contains @@ hunks but missing file headers, the inner validator
	// accepts it as diff without headers, so no need to synthesize headers.
	// Return as-is.
	return content
}

// splitLines splits s into lines without trailing empty artifact.
// It mirrors the behavior of most diff helpers that ignore the final
// empty entry produced by a trailing newline, preventing spurious
// empty-string hunks.
func splitLines(s string) []string {
	// Normalize CRLF to LF for consistent diff.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	// Remove trailing empty strings produced by a final newline.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// synthesizePatch computes a minimal diff between targetContent and newContent
// using Myers LCS (DP) and constructs explicit SEARCH/REPLACE blocks.
func synthesizePatch(targetContent, newContent string) ([]byte, error) {
	if targetContent == "" {
		return nil, fmt.Errorf("%w: empty target for synthesis", ErrFormatRejected)
	}
	if newContent == targetContent {
		return nil, fmt.Errorf("%w: no changes", ErrFormatRejected)
	}
	origLines := splitLines(targetContent)
	newLines := splitLines(newContent)

	dp := lcs(origLines, newLines)
	ops := backtrack(dp, origLines, newLines)

	type hunk struct {
		deletes   []string
		inserts   []string
		origStart int
	}
	var hunks []hunk
	var cur *hunk
	origPos := 0
	for i, op := range ops {
		switch op.typ {
		case "equal":
			if cur != nil {
				hunks = append(hunks, *cur)
				cur = nil
			}
			origPos++
		case "delete":
			if cur == nil {
				cur = &hunk{origStart: origPos}
			}
			cur.deletes = append(cur.deletes, op.line)
			origPos++
		case "insert":
			if cur == nil {
				cur = &hunk{origStart: origPos}
			}
			cur.inserts = append(cur.inserts, op.line)
		}
		if i == len(ops)-1 && cur != nil {
			hunks = append(hunks, *cur)
		}
	}

	if len(hunks) == 0 {
		return nil, fmt.Errorf("%w: no diff hunks synthesized", ErrFormatRejected)
	}

	var blocks []string
	for _, h := range hunks {
		var search, replace string
		switch {
		case len(h.deletes) > 0 && len(h.inserts) > 0:
			search = strings.Join(h.deletes, "\n")
			replace = strings.Join(h.inserts, "\n")
		case len(h.deletes) > 0 && len(h.inserts) == 0:
			search = strings.Join(h.deletes, "\n")
			replace = ""
		case len(h.deletes) == 0 && len(h.inserts) > 0:
			// Insertion-only: anchor on surrounding context.
			start := h.origStart - 2
			if start < 0 {
				start = 0
			}
			end := h.origStart
			if start == end {
				// At beginning: anchor on next lines.
				end = h.origStart + 2
				if end > len(origLines) {
					end = len(origLines)
				}
				if start < end {
					search = strings.Join(origLines[start:end], "\n")
					replace = strings.Join(h.inserts, "\n") + "\n" + search
				} else {
					return nil, fmt.Errorf("%w: insertion anchor empty", ErrAmbiguousAnchor)
				}
			} else {
				search = strings.Join(origLines[start:end], "\n")
				replace = search + "\n" + strings.Join(h.inserts, "\n")
			}
		default:
			continue
		}
		if strings.TrimSpace(search) == "" {
			return nil, fmt.Errorf("%w: empty SEARCH anchor", ErrAmbiguousAnchor)
		}
		// Fail-closed anchor check.
		sl := strings.Split(search, "\n")
		if _, _, err := ResolveAnchors(sl, targetContent); err != nil {
			return nil, err
		}
		block := fmt.Sprintf("<<<<<<< SEARCH\n%s\n=======\n%s\n>>>>>>>", search, replace)
		blocks = append(blocks, block)
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("%w: no blocks synthesized", ErrFormatRejected)
	}
	return []byte(strings.Join(blocks, "\n")), nil
}

type op struct {
	typ  string
	line string
}

func lcs(a, b []string) [][]int {
	m, n := len(a), len(b)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				if dp[i-1][j] >= dp[i][j-1] {
					dp[i][j] = dp[i-1][j]
				} else {
					dp[i][j] = dp[i][j-1]
				}
			}
		}
	}
	return dp
}

func backtrack(dp [][]int, a, b []string) []op {
	i, j := len(a), len(b)
	var rev []op
backtrackLoop:
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && a[i-1] == b[j-1]:
			rev = append(rev, op{typ: "equal", line: a[i-1]})
			i--
			j--
		case j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]):
			rev = append(rev, op{typ: "insert", line: b[j-1]})
			j--
		case i > 0 && (j == 0 || dp[i][j-1] < dp[i-1][j]):
			rev = append(rev, op{typ: "delete", line: a[i-1]})
			i--
		default:
			break backtrackLoop
		}
	}
	// reverse
	for l, r := 0, len(rev)-1; l < r; l, r = l+1, r-1 {
		rev[l], rev[r] = rev[r], rev[l]
	}
	return rev
}

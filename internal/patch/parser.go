package patch

import (
	"regexp"
	"strconv"
	"strings"
)

// SearchReplaceBlock is one parsed <<<<<<< SEARCH ... ======= ... >>>>>>> REPLACE
// unit.
type SearchReplaceBlock struct {
	Search  string
	Replace string
}

var (
	searchReplaceRe = regexp.MustCompile(`(?s)<<<<<<< SEARCH\n(.*?)=======\n(.*?)>>>>>>> REPLACE`)
	hunkHeaderRe    = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
)

// UnifiedHunk is one parsed diff hunk.
type UnifiedHunk struct {
	OldStart, OldCount int
	OldLines, NewLines []string
}

// Parser resolves raw patch output into a concrete Patch. Implementations are
// responsible for choosing the tier and producing final file content.
type Parser interface {
	Parse(original string, raw string) (Patch, error)
}

// ParserFunc adapts a function to the Parser interface.
type ParserFunc func(original string, raw string) (Patch, error)

func (f ParserFunc) Parse(original string, raw string) (Patch, error) { return f(original, raw) }

// TieredParser walks the parser tiers in order: unified diff, then
// SEARCH/REPLACE blocks, then whole-file rewrite. It returns ErrAmbiguousPatch
// when no tier yields a resolvable patch.
type TieredParser struct{}

func NewTieredParser() *TieredParser { return &TieredParser{} }

func (p *TieredParser) Parse(original string, raw string) (Patch, error) {
	if patch, ok, err := parseUnifiedDiff(original, raw); err != nil {
		return Patch{}, err
	} else if ok {
		return patch, nil
	}

	if patch, ok, err := parseSearchReplace(original, raw); err != nil {
		return Patch{}, err
	} else if ok {
		return patch, nil
	}

	if patch, ok := parseWholeFile(original, raw); ok {
		return patch, nil
	}

	return Patch{}, ErrAmbiguousPatch
}

// parseUnifiedDiff interprets `raw` as a unified diff and produces the final
// content by applying hunks to `original`. It returns (patch, true, nil) on a
// clean application; (Patch{}, false, nil) when the payload is not a unified
// diff; and an error when it is a diff but hunks do not match the original.
func parseUnifiedDiff(original string, raw string) (Patch, bool, error) {
	hunks := parseHunks(raw)
	if len(hunks) == 0 {
		return Patch{}, false, nil
	}

	origLines := strings.Split(original, "\n")
	// Apply bottom-up so earlier line indices stay valid.
	for i := len(hunks) - 1; i >= 0; i-- {
		h := hunks[i]
		start := h.OldStart - 1
		if start < 0 || start+len(h.OldLines) > len(origLines) {
			return Patch{}, false, errHunkMismatch
		}
		for j := range h.OldLines {
			if origLines[start+j] != h.OldLines[j] {
				return Patch{}, false, errHunkMismatch
			}
		}
		merged := make([]string, 0, len(origLines)-len(h.OldLines)+len(h.NewLines))
		merged = append(merged, origLines[:start]...)
		merged = append(merged, h.NewLines...)
		merged = append(merged, origLines[start+len(h.OldLines):]...)
		origLines = merged
	}

	return Patch{
		Original: original,
		Modified: strings.Join(origLines, "\n"),
		Tier:     Tier1StructuredDiff,
		Strategy: Tier1StructuredDiff.String(),
	}, true, nil
}

var errHunkMismatch = errHunkMismatchType{}

type errHunkMismatchType struct{}

func (errHunkMismatchType) Error() string { return "patch hunk does not match file content" }

// parseHunks extracts unified-diff hunks from `raw`. It returns nil when no
// hunk headers are present.
func parseHunks(raw string) []UnifiedHunk {
	lines := strings.Split(raw, "\n")
	var hunks []UnifiedHunk
	var cur *UnifiedHunk

	for _, line := range lines {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if m := hunkHeaderRe.FindStringSubmatch(line); m != nil {
			oldStart, _ := strconv.Atoi(m[1])
			oldCount := 1
			if m[2] != "" {
				oldCount, _ = strconv.Atoi(m[2])
			}
			if cur != nil {
				hunks = append(hunks, *cur)
			}
			cur = &UnifiedHunk{OldStart: oldStart, OldCount: oldCount}
			continue
		}
		if cur == nil {
			continue
		}
		if len(line) == 0 {
			cur.OldLines = append(cur.OldLines, "")
			cur.NewLines = append(cur.NewLines, "")
			continue
		}
		switch line[0] {
		case ' ':
			cur.OldLines = append(cur.OldLines, line[1:])
			cur.NewLines = append(cur.NewLines, line[1:])
		case '-':
			cur.OldLines = append(cur.OldLines, line[1:])
		case '+':
			cur.NewLines = append(cur.NewLines, line[1:])
		case '\\':
			// "\ No newline at end of file" marker — ignored.
			continue
		}
	}
	if cur != nil {
		hunks = append(hunks, *cur)
	}
	return hunks
}

// parseSearchReplace interprets `raw` as one or more SEARCH/REPLACE blocks and
// applies them to `original`. Returns (patch, true, nil) when at least one
// block matched; (Patch{}, false, nil) when no SEARCH/REPLACE markers exist;
// and an error when markers exist but no block matched the original.
func parseSearchReplace(original string, raw string) (Patch, bool, error) {
	blocks := parseSearchReplaceBlocks(raw)
	if len(blocks) == 0 {
		return Patch{}, false, nil
	}

	result := original
	matched := 0
	for _, b := range blocks {
		idx := strings.Index(result, b.Search)
		if idx < 0 {
			continue
		}
		result = result[:idx] + b.Replace + result[idx+len(b.Search):]
		matched++
	}
	if matched == 0 {
		return Patch{}, false, errHunkMismatch
	}

	return Patch{
		Original: original,
		Modified: result,
		Tier:     Tier2SearchReplace,
		Strategy: Tier2SearchReplace.String(),
	}, true, nil
}

// parseSearchReplaceBlocks extracts SEARCH/REPLACE blocks from `raw`.
func parseSearchReplaceBlocks(raw string) []SearchReplaceBlock {
	ms := searchReplaceRe.FindAllStringSubmatch(raw, -1)
	blocks := make([]SearchReplaceBlock, 0, len(ms))
	for _, m := range ms {
		blocks = append(blocks, SearchReplaceBlock{
			Search:  strings.TrimSuffix(m[1], "\n"),
			Replace: strings.TrimSuffix(m[2], "\n"),
		})
	}
	return blocks
}

// parseWholeFile treats `raw` as a full replacement of the file. It strips
// code fences when present and refuses payloads that are clearly much smaller
// than an existing non-empty original (a raw snippet, not a rewrite).
func parseWholeFile(original string, raw string) (Patch, bool) {
	clean := stripFences(raw)
	if strings.TrimSpace(clean) == "" {
		return Patch{}, false
	}
	if original != "" && len(clean) < len(original)/2 {
		// Plausible only when the file is being intentionally shrunk; the
		// safety evaluator decides that. Ambiguity guard lives in the engine.
		return Patch{}, false
	}
	return Patch{
		Original: original,
		Modified: clean,
		Tier:     Tier3WholeFile,
		Strategy: Tier3WholeFile.String(),
	}, true
}

// stripFences removes a surrounding ``` fence (with optional language tag)
// from a whole-file payload. It preserves all other bytes exactly.
func stripFences(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	rest := strings.TrimPrefix(s, "```")
	if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
		rest = rest[idx+1:]
	}
	if idx := strings.LastIndex(rest, "```"); idx >= 0 {
		rest = rest[:idx]
	}
	return rest
}

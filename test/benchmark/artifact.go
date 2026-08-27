package benchmark

import (
	"fmt"
	"strings"
)

// ── SEARCH/REPLACE artifact contract ────────────────────────────────────────
//
// The benchmark accepts the same anchored-block protocol the runtime's
// Boundary-4 gate enforces: one or more
//
//	<<<<<<< SEARCH
//	exact lines copied from the current content
//	=======
//	replacement
//	>>>>>>>
//
// blocks whose SEARCH text occurs EXACTLY ONCE in the current document. A
// generation violating the contract is a retry, never an apply.

// patchBlock is one parsed SEARCH/REPLACE unit.
type patchBlock struct {
	search  string
	replace string
}

const (
	markerSearch  = "<<<<<<< SEARCH"
	markerDivide  = "======="
	markerReplace = ">>>>>>>"
)

// parseSearchReplaceBlocks extracts every well-formed block from one model
// completion. Code-fence decorations around the block are tolerated.
func parseSearchReplaceBlocks(content string) ([]patchBlock, error) {
	var blocks []patchBlock
	lines := strings.Split(content, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], markerSearch) {
			continue
		}
		searchLines := make([]string, 0, 8)
		i++
		for ; i < len(lines) && !strings.HasPrefix(lines[i], markerDivide); i++ {
			searchLines = append(searchLines, lines[i])
		}
		if i >= len(lines) {
			return nil, fmt.Errorf("artifact: SEARCH block without ======= divider")
		}
		replaceLines := make([]string, 0, 8)
		i++
		for ; i < len(lines) && !strings.HasPrefix(lines[i], markerReplace); i++ {
			replaceLines = append(replaceLines, lines[i])
		}
		if i >= len(lines) {
			return nil, fmt.Errorf("artifact: block without >>>>>>> terminator")
		}
		if strings.TrimSpace(strings.Join(searchLines, "\n")) == "" {
			return nil, fmt.Errorf("artifact: empty SEARCH anchor")
		}
		blocks = append(blocks, patchBlock{
			search:  strings.Join(searchLines, "\n"),
			replace: strings.Join(replaceLines, "\n"),
		})
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("artifact: no SEARCH/REPLACE blocks in generation")
	}
	return blocks, nil
}

// applyBlocks applies every block to src; each SEARCH text must occur exactly
// once (exact-once anchor proof). Returns the mutated source.
func applyBlocks(src string, blocks []patchBlock) (string, error) {
	out := src
	for _, b := range blocks {
		if n := strings.Count(out, b.search); n != 1 {
			return "", fmt.Errorf("anchor not exact-once (%d occurrences): %q", n, firstLine(b.search))
		}
		out = strings.Replace(out, b.search, b.replace, 1)
	}
	if out == src {
		return "", fmt.Errorf("block applied no change")
	}
	return out, nil
}

// firstLine returns the first line of s for bounded evidence.
func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

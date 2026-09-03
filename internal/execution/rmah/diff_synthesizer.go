package rmah

import (
	"fmt"
	"strings"
)

// ErrTier3DestructiveTruncation identifies a raw response which would remove
// most of an otherwise valid baseline.
var ErrTier3DestructiveTruncation = fmt.Errorf("RMAH Tier 3: synthesized patch rejected due to destructive truncation (<60%% retention)")

// ErrAmbiguousAnchors is the sentinel for Tier 3 ambiguous anchor failures
// that could not be resolved even after dynamic context expansion. It is
// classified as NonRetryableArtifactError unless line-offset context is injected.
// Returned ONLY when strings.Count(baseline, searchBlock) > 1 after maxRadius.
var ErrAmbiguousAnchors = fmt.Errorf("RMAH Tier 3: ambiguous anchors")

// ErrZeroMatchAnchor is the sentinel for Tier 3 hallucinated anchor failures
// where the synthesized SEARCH matches zero regions in the baseline. It is
// classified as HallucinatedAnchorError and offers distinct DecisionSurface
// options (full-file fallback + full-text re-prompt).
// Returned when strings.Count(baseline, searchBlock) == 0 after maxRadius.
var ErrZeroMatchAnchor = fmt.Errorf("RMAH Tier 3: hallucinated anchor — zero match")

// minRadius and maxRadius bound the dynamic context expansion for anchor
// resolution. The synthesizer expands SEARCH context iteratively until
// strings.Count(baseline, searchBlock) == 1 or maxRadius is exhausted.
const (
	minRadius = 2
	maxRadius = 15
)

type diffOperation struct {
	kind byte
	line string
}

// synthesizeDiffPatch computes a line-based Myers edit script and renders it
// as anchored SEARCH/REPLACE blocks. The returned patch is intentionally
// self-contained: every SEARCH side contains two unchanged lines where they
// exist, which prevents repeated snippets from becoming fuzzy anchors.
//
// Dynamic context expansion (RMAH Tier 3): when a static 2-line context is
// ambiguous (strings.Count(baseline, searchBlock) > 1) the synthesizer
// iteratively expands the context radius from minRadius to maxRadius until
// the SEARCH block is unique. Expansion is symmetric first (top+bottom);
// when BOF/EOF is hit the remaining expansion is applied unilaterally.
func synthesizeDiffPatch(baseline, candidate string) (string, error) {
	oldLines := diffLines(baseline)
	newLines := diffLines(candidate)
	if len(oldLines) == 0 || len(newLines) == 0 || baseline == candidate {
		return "", fmt.Errorf("rmah tier 3: no synthesizable change")
	}
	ops := myersDiff(oldLines, newLines)
	starts, ends := editRunsWithIndices(ops)
	if len(starts) == 0 {
		return "", fmt.Errorf("rmah tier 3: no diff hunks synthesized")
	}

	baselineStr := strings.Join(oldLines, "\n")
	blocks := make([]string, 0, len(starts))
	for idx := range starts {
		start, end := starts[idx], ends[idx]
		var resolvedSearch, resolvedReplace string
		found := false
		lastCount := -1
		// Iterative expansion loop: radius := minRadius … maxRadius
		for radius := minRadius; radius <= maxRadius; radius++ {
			run := paddedRunWithRadius(ops, start, end, radius)
			oldText, newText := renderRun(run)
			if strings.TrimSpace(oldText) == "" {
				continue
			}
			searchBlock := oldText
			matchCount := strings.Count(baselineStr, searchBlock)
			lastCount = matchCount
			// Fall back to line-exact count when substring count diverges
			// due to overlapping matches or delimiter differences.
			if matchCount != 1 {
				if countExactLines(oldLines, strings.Split(searchBlock, "\n")) != 1 {
					continue
				}
				// Line-exact uniqueness is sufficient when substring count
				// is polluted by partial overlaps.
				resolvedSearch, resolvedReplace = oldText, newText
				found = true
				break
			}
			if countExactLines(oldLines, strings.Split(searchBlock, "\n")) != 1 {
				lastCount = countExactLines(oldLines, strings.Split(searchBlock, "\n"))
				continue
			}
			resolvedSearch, resolvedReplace = oldText, newText
			found = true
			break
		}
		if !found {
			// Explicit classification: N=0 vs N>1 after maxRadius expansion.
			if lastCount == 0 {
				return "", ErrZeroMatchAnchor
			}
			return "", ErrAmbiguousAnchors
		}
		blocks = append(blocks, fmt.Sprintf("<<<<<<< SEARCH\n%s\n=======\n%s\n>>>>>>> REPLACE", resolvedSearch, resolvedReplace))
	}
	return strings.Join(blocks, "\n"), nil
}

// buildSearchBlock is the exported helper used by the spec's pseudocode and
// tests: it builds a SEARCH block from baseline lines for the given edit hunk
// at the requested radius. It is a thin wrapper over paddedRunWithRadius +
// renderRun for external callers.
//
//nolint:unused
func buildSearchBlock(baselineLines []string, ops []diffOperation, start, end, radius int) string {
	run := paddedRunWithRadius(ops, start, end, radius)
	oldText, _ := renderRun(run)
	_ = baselineLines
	return oldText
}

func diffLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// myersDiff returns the shortest edit script. Ties prefer deletions, making
// the output deterministic when repeated lines exist.
func myersDiff(a, b []string) []diffOperation {
	max := len(a) + len(b)
	v := map[int]int{1: 0}
	trace := make([]map[int]int, 0, max+1)
	for d := 0; d <= max; d++ {
		for k := -d; k <= d; k += 2 {
			x := 0
			if k == -d || (k != d && v[k-1] < v[k+1]) {
				x = v[k+1]
			} else {
				x = v[k-1] + 1
			}
			y := x - k
			for x < len(a) && y < len(b) && a[x] == b[y] {
				x++
				y++
			}
			v[k] = x
			if x >= len(a) && y >= len(b) {
				trace = append(trace, cloneInts(v))
				return backtrackMyers(trace, a, b, d)
			}
		}
		trace = append(trace, cloneInts(v))
	}
	return nil
}

func backtrackMyers(trace []map[int]int, a, b []string, d int) []diffOperation {
	x, y := len(a), len(b)
	reversed := make([]diffOperation, 0, x+y)
	for depth := d; depth > 0; depth-- {
		v := trace[depth]
		k := x - y
		prevK := k - 1
		if k == -depth || (k != depth && v[k-1] < v[k+1]) {
			prevK = k + 1
		}
		prevX := trace[depth-1][prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			reversed = append(reversed, diffOperation{'=', a[x]})
		}
		if x == prevX {
			y--
			reversed = append(reversed, diffOperation{'+', b[y]})
		} else {
			x--
			reversed = append(reversed, diffOperation{'-', a[x]})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		reversed = append(reversed, diffOperation{'=', a[x]})
	}
	for x > 0 {
		x--
		reversed = append(reversed, diffOperation{'-', a[x]})
	}
	for y > 0 {
		y--
		reversed = append(reversed, diffOperation{'+', b[y]})
	}
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return reversed
}

func cloneInts(src map[int]int) map[int]int {
	dst := make(map[int]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// editRuns groups edits whose two-line context windows would overlap.
// Legacy wrapper retained for external callers; synthesizeDiffPatch now uses
// editRunsWithIndices + paddedRunWithRadius for dynamic expansion.
//
//nolint:unused
func editRuns(ops []diffOperation) [][]diffOperation {
	var runs [][]diffOperation
	start := -1
	lastEdit := -1
	equalAfter := 0
	for i, op := range ops {
		if op.kind != '=' {
			if start < 0 {
				start = i
			} else if equalAfter > 4 {
				runs = append(runs, paddedRun(ops, start, lastEdit))
				start = i
			}
			lastEdit = i
			equalAfter = 0
		} else if start >= 0 {
			equalAfter++
		}
	}
	if start >= 0 {
		runs = append(runs, paddedRun(ops, start, lastEdit))
	}
	return runs
}

//nolint:unused
func paddedRun(ops []diffOperation, start, end int) []diffOperation {
	return paddedRunWithRadius(ops, start, end, minRadius)
}

// editRunsWithIndices returns the start/end indices of each edit run before
// padding, so the caller can iteratively expand context radius.
func editRunsWithIndices(ops []diffOperation) (starts, ends []int) {
	start := -1
	lastEdit := -1
	equalAfter := 0
	for i, op := range ops {
		if op.kind != '=' {
			if start < 0 {
				start = i
			} else if equalAfter > 4 {
				starts = append(starts, start)
				ends = append(ends, lastEdit)
				start = i
			}
			lastEdit = i
			equalAfter = 0
		} else if start >= 0 {
			equalAfter++
		}
	}
	if start >= 0 {
		starts = append(starts, start)
		ends = append(ends, lastEdit)
	}
	return starts, ends
}

// paddedRunWithRadius expands the [start,end] edit window by radius lines of
// context in each direction. It expands symmetrically first (top + bottom);
// if one side hits BOF/EOF the remaining budget is applied unilaterally to
// the other side.
func paddedRunWithRadius(ops []diffOperation, start, end, radius int) []diffOperation {
	if radius < 0 {
		radius = minRadius
	}
	if radius > maxRadius {
		radius = maxRadius
	}
	topAvail := 0
	for i := start - 1; i >= 0 && ops[i].kind == '='; i-- {
		topAvail++
	}
	bottomAvail := 0
	for i := end + 1; i < len(ops) && ops[i].kind == '='; i++ {
		bottomAvail++
	}
	// Symmetric baseline.
	topWant := radius
	if topWant > topAvail {
		topWant = topAvail
	}
	bottomWant := radius
	if bottomWant > bottomAvail {
		bottomWant = bottomAvail
	}
	// Unilateral compensation: if one side hit its boundary, give its
	// shortfall to the other side.
	if topAvail < radius && bottomAvail > radius {
		shortfall := radius - topAvail
		extra := bottomAvail - radius
		if extra > shortfall {
			extra = shortfall
		}
		bottomWant += extra
	}
	if bottomAvail < radius && topAvail > radius {
		shortfall := radius - bottomAvail
		extra := topAvail - radius
		if extra > shortfall {
			extra = shortfall
		}
		topWant += extra
	}
	newStart := start - topWant
	if newStart < 0 {
		newStart = 0
	}
	newEnd := end + bottomWant
	if newEnd >= len(ops) {
		newEnd = len(ops) - 1
	}
	return append([]diffOperation(nil), ops[newStart:newEnd+1]...)
}

func renderRun(run []diffOperation) (string, string) {
	oldLines, newLines := make([]string, 0, len(run)), make([]string, 0, len(run))
	for _, op := range run {
		if op.kind != '+' {
			oldLines = append(oldLines, op.line)
		}
		if op.kind != '-' {
			newLines = append(newLines, op.line)
		}
	}
	return strings.Join(oldLines, "\n"), strings.Join(newLines, "\n")
}

func countExactLines(lines, needle []string) int {
	count := 0
	for i := 0; i+len(needle) <= len(lines); i++ {
		if strings.Join(lines[i:i+len(needle)], "\n") == strings.Join(needle, "\n") {
			count++
		}
	}
	return count
}

func applySynthesizedPatch(baseline, patch string) (string, bool) {
	result := baseline
	for _, block := range strings.Split(patch, "<<<<<<< SEARCH\n")[1:] {
		parts := strings.SplitN(block, "\n=======\n", 2)
		if len(parts) != 2 {
			return "", false
		}
		replaceParts := strings.SplitN(parts[1], "\n>>>>>>> REPLACE", 2)
		if len(replaceParts) != 2 {
			return "", false
		}
		search, replace := parts[0], replaceParts[0]
		if strings.Count(result, search) != 1 {
			return "", false
		}
		result = strings.Replace(result, search, replace, 1)
	}
	return result, true
}

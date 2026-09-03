package rmah

import (
	"fmt"
	"strings"
)

// ErrTier3DestructiveTruncation identifies a raw response which would remove
// most of an otherwise valid baseline.
var ErrTier3DestructiveTruncation = fmt.Errorf("RMAH Tier 3: synthesized patch rejected due to destructive truncation (<60%% retention)")

type diffOperation struct {
	kind byte
	line string
}

// synthesizeDiffPatch computes a line-based Myers edit script and renders it
// as anchored SEARCH/REPLACE blocks. The returned patch is intentionally
// self-contained: every SEARCH side contains two unchanged lines where they
// exist, which prevents repeated snippets from becoming fuzzy anchors.
func synthesizeDiffPatch(baseline, candidate string) (string, error) {
	oldLines := diffLines(baseline)
	newLines := diffLines(candidate)
	if len(oldLines) == 0 || len(newLines) == 0 || baseline == candidate {
		return "", fmt.Errorf("rmah tier 3: no synthesizable change")
	}
	ops := myersDiff(oldLines, newLines)
	runs := editRuns(ops)
	if len(runs) == 0 {
		return "", fmt.Errorf("rmah tier 3: no diff hunks synthesized")
	}

	blocks := make([]string, 0, len(runs))
	for _, run := range runs {
		oldText, newText := renderRun(run)
		if strings.TrimSpace(oldText) == "" {
			return "", fmt.Errorf("rmah tier 3: ambiguous empty anchor")
		}
		if countExactLines(oldLines, strings.Split(oldText, "\n")) != 1 {
			return "", fmt.Errorf("rmah tier 3: ambiguous anchor")
		}
		blocks = append(blocks, fmt.Sprintf("<<<<<<< SEARCH\n%s\n=======\n%s\n>>>>>>> REPLACE", oldText, newText))
	}
	return strings.Join(blocks, "\n"), nil
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

func paddedRun(ops []diffOperation, start, end int) []diffOperation {
	for n := 0; n < 2 && start > 0 && ops[start-1].kind == '='; n++ {
		start--
	}
	for n := 0; n < 2 && end+1 < len(ops) && ops[end+1].kind == '='; n++ {
		end++
	}
	return append([]diffOperation(nil), ops[start:end+1]...)
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

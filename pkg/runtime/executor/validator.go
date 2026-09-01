package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/pkg/projection/diff"
)

// hunkHeaderRe matches a unified-diff hunk header and captures the old-file
// start line and count and the new-file start line and count.
var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ProposalValidator performs the deterministic validation pass on an untrusted
// ProposedMutation. It is side-effect-free: it never writes to the filesystem,
// so a validation failure never requires a rollback.
type ProposalValidator struct{}

// NewValidator returns a ready-to-use ProposalValidator.
func NewValidator() *ProposalValidator {
	return &ProposalValidator{}
}

// Validate runs the deterministic validation pipeline: schema and target
// integrity, target safety (path traversal), patch structure, and evidence
// generation. It returns a ValidationResult with Valid set to false and a
// descriptive ErrorReason for every failed deterministic check; the Go error
// return is reserved for internal misuse (nil receiver, unexpected read
// failure).
func (v *ProposalValidator) Validate(proposal ProposedMutation) (*ValidationResult, error) {
	if v == nil {
		return nil, errors.New("executor: nil ProposalValidator")
	}

	// Schema & target integrity check.
	if proposal.TargetRef == nil {
		return invalidResult("executor: proposal carries no target reference"), nil
	}
	if proposal.TargetRef.Canonical == "" {
		return invalidResult("executor: proposal target has an empty canonical path"), nil
	}
	if isPathTraversal(proposal.TargetRef.Canonical) {
		return invalidResult("executor: proposal target escapes the working directory"), nil
	}

	// Patch payload presence.
	if proposal.RawPatch == "" && len(proposal.PatchLines) == 0 {
		return invalidResult("executor: proposal carries no mutation payload"), nil
	}

	// Patch structural validity.
	if err := validatePatchStructure(proposal); err != nil {
		return invalidResult(err.Error()), nil
	}

	// Target safety: the raw patch must not address a path outside the
	// canonical target (path traversal smuggled through diff headers).
	if proposal.RawPatch != "" && patchTargetEscapes(proposal.RawPatch) {
		return invalidResult("executor: patch addresses a path outside the canonical target"), nil
	}

	// Evidence generation: a deterministic diff of the current target content
	// against the materialized post-mutation content.
	base, err := currentContent(proposal.TargetRef.Canonical)
	if err != nil {
		return invalidResult(fmt.Sprintf("executor: cannot read target: %s", err)), nil
	}
	final, err := materializeContent(proposal, base)
	if err != nil {
		return invalidResult(err.Error()), nil
	}

	return &ValidationResult{
		Valid:            true,
		Evidence:         evidenceFor(base, final, proposal.TargetRef.Canonical),
		RequiresRollback: false,
	}, nil
}

// invalidResult builds a failed ValidationResult with a descriptive reason.
func invalidResult(reason string) *ValidationResult {
	return &ValidationResult{Valid: false, ErrorReason: reason}
}

// validatePatchStructure checks that the proposal payload is structurally
// sound: provided PatchLines must carry valid mutation types, and a RawPatch
// payload must be non-empty and, when it looks like a unified diff, must
// contain at least one parseable hunk.
func validatePatchStructure(proposal ProposedMutation) error {
	if len(proposal.PatchLines) > 0 {
		for _, ln := range proposal.PatchLines {
			if ln.Type < diff.MutationAdd || ln.Type > diff.MutationDelete {
				return errors.New("executor: unknown mutation type in patch lines")
			}
		}
		return nil
	}
	if strings.TrimSpace(proposal.RawPatch) == "" {
		return errors.New("executor: proposal carries an empty raw patch")
	}
	if looksLikeUnifiedDiff(proposal.RawPatch) && len(parseDiffHunks(proposal.RawPatch)) == 0 {
		return errors.New("executor: raw patch has no parseable hunks")
	}
	return nil
}

// currentContent reads the target file content, treating a missing file as
// empty content so new-file mutations validate cleanly.
func currentContent(canonical string) (string, error) {
	data, err := os.ReadFile(canonical)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// materializeContent derives the post-mutation target content. Explicit
// PatchLines take precedence (Add/Modify lines form the content, Delete lines
// are dropped); otherwise a unified-diff RawPatch is applied against base and
// any other RawPatch is treated as full post-mutation content.
func materializeContent(proposal ProposedMutation, base string) (string, error) {
	if len(proposal.PatchLines) > 0 {
		return materializeFromLines(proposal.PatchLines), nil
	}
	if proposal.RawPatch == "" {
		return "", errors.New("executor: proposal carries no mutation payload")
	}
	if looksLikeUnifiedDiff(proposal.RawPatch) {
		return applyUnifiedDiff(base, proposal.RawPatch)
	}
	return proposal.RawPatch, nil
}

// materializeFromLines assembles post-mutation content from typed patch
// lines: MutationAdd and MutationModify lines are kept, MutationDelete lines
// are dropped.
func materializeFromLines(lines []diff.PatchLine) string {
	var b strings.Builder
	for _, ln := range lines {
		if ln.Type == diff.MutationDelete {
			continue
		}
		b.WriteString(ln.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// evidenceFor computes the diff evidence between the base and post-mutation
// content using a line-level LCS diff, mapping changed lines to
// MutationAdd / MutationDelete patch lines and counting them.
func evidenceFor(base, final, canonical string) diff.MutationEvidence {
	lines := lineDiff(splitLines(base), splitLines(final))
	ev := diff.MutationEvidence{TargetFile: canonical, Lines: lines}
	for _, ln := range lines {
		switch ln.Type {
		case diff.MutationAdd:
			ev.Added++
		case diff.MutationDelete:
			ev.Deleted++
		}
	}
	return ev
}

// splitLines splits content into lines, dropping the trailing empty element
// introduced by a trailing newline. Empty content yields no lines.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lineDiff returns the line-level diff between oldLines and newLines as a
// sequence of MutationDelete / MutationAdd patch lines. For inputs whose LCS
// table would exceed maxDiffCells, it falls back to a delete-all/add-all
// projection to bound memory usage.
func lineDiff(oldLines, newLines []string) []diff.PatchLine {
	const maxDiffCells = 1_000_000

	out := make([]diff.PatchLine, 0, len(oldLines)+len(newLines))
	n, m := len(oldLines), len(newLines)
	if n*m > maxDiffCells {
		for _, ln := range oldLines {
			out = append(out, diff.PatchLine{Type: diff.MutationDelete, Content: ln})
		}
		for _, ln := range newLines {
			out = append(out, diff.PatchLine{Type: diff.MutationAdd, Content: ln})
		}
		return out
	}

	// LCS lengths table (suffix-based).
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		row := dp[i]
		next := dp[i+1]
		for j := m - 1; j >= 0; j-- {
			switch {
			case oldLines[i] == newLines[j]:
				row[j] = next[j+1] + 1
			case next[j] >= row[j+1]:
				row[j] = next[j]
			default:
				row[j] = row[j+1]
			}
		}
	}

	// Walk the table to emit delete/add lines for non-LCS positions.
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, diff.PatchLine{Type: diff.MutationDelete, Content: oldLines[i]})
			i++
		default:
			out = append(out, diff.PatchLine{Type: diff.MutationAdd, Content: newLines[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diff.PatchLine{Type: diff.MutationDelete, Content: oldLines[i]})
	}
	for ; j < m; j++ {
		out = append(out, diff.PatchLine{Type: diff.MutationAdd, Content: newLines[j]})
	}
	return out
}

// isPathTraversal reports whether path escapes the working directory via ".."
// components or is empty.
func isPathTraversal(path string) bool {
	if path == "" {
		return true
	}
	clean := filepath.Clean(path)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// patchTargetEscapes reports whether any unified-diff file header in raw
// addresses a path outside the working directory, either through ".."
// traversal or an absolute path.
func patchTargetEscapes(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		var p string
		switch {
		case strings.HasPrefix(line, "--- "):
			p = strings.TrimPrefix(line, "--- ")
		case strings.HasPrefix(line, "+++ "):
			p = strings.TrimPrefix(line, "+++ ")
		default:
			continue
		}
		p = strings.TrimSpace(p)
		if p == "" || p == "/dev/null" {
			continue
		}
		p = strings.TrimPrefix(p, "a/")
		p = strings.TrimPrefix(p, "b/")
		if filepath.IsAbs(p) || isPathTraversal(p) {
			return true
		}
	}
	return false
}

// looksLikeUnifiedDiff reports whether raw carries unified-diff structure
// (old/new file headers and at least one hunk header).
func looksLikeUnifiedDiff(raw string) bool {
	hasOld, hasNew, hasHunk := false, false, false
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			hasOld = true
		case strings.HasPrefix(line, "+++ "):
			hasNew = true
		case strings.HasPrefix(line, "@@ "):
			hasHunk = true
		}
	}
	return hasOld && hasNew && hasHunk
}

// diffHunk is one parsed unified-diff hunk.
type diffHunk struct {
	oldStart int
	oldLines []string
	newLines []string
}

// applyUnifiedDiff applies hunks in raw to current bottom-up so earlier line
// indices remain valid. It errors when any hunk does not match the current
// content.
func applyUnifiedDiff(current, raw string) (string, error) {
	hunks := parseDiffHunks(raw)
	if len(hunks) == 0 {
		return "", errors.New("executor: raw patch has no parseable hunks")
	}
	lines := strings.Split(current, "\n")
	for i := len(hunks) - 1; i >= 0; i-- {
		h := hunks[i]
		if h.oldStart == 0 {
			if len(h.oldLines) != 0 || strings.TrimSpace(current) != "" {
				return "", fmt.Errorf("executor: new-file hunk requires empty base content")
			}
		}
		start := h.oldStart - 1
		if start < 0 {
			start = 0
		}
		if h.oldStart > 0 && start+len(h.oldLines) > len(lines) {
			return "", fmt.Errorf("executor: hunk range out of bounds")
		}
		if h.oldStart > 0 {
			for j, want := range h.oldLines {
				if lines[start+j] != want {
					return "", fmt.Errorf("executor: hunk does not match file content at line %d", start+j+1)
				}
			}
		}
		merged := make([]string, 0, len(lines)-len(h.oldLines)+len(h.newLines))
		merged = append(merged, lines[:start]...)
		merged = append(merged, h.newLines...)
		merged = append(merged, lines[start+len(h.oldLines):]...)
		lines = merged
	}
	return strings.Join(lines, "\n"), nil
}

// parseDiffHunks extracts hunks from a unified-diff payload.
func parseDiffHunks(raw string) []diffHunk {
	lines := strings.Split(raw, "\n")
	hunks := make([]diffHunk, 0, 2)
	var cur *diffHunk
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "@@ "):
			if hunkHeaderRe.MatchString(line) {
				if cur != nil {
					hunks = append(hunks, *cur)
				}
				cur = &diffHunk{oldStart: hunkOldStart(line)}
			}
			continue
		case cur == nil:
			continue
		case len(line) == 0:
			// A bare empty line is the trailing-newline split artifact of the
			// payload; well-formed unified diffs prefix empty context lines
			// with a single space, so a bare "" never represents content.
			continue
		default:
			switch line[0] {
			case ' ':
				cur.oldLines = append(cur.oldLines, line[1:])
				cur.newLines = append(cur.newLines, line[1:])
			case '-':
				cur.oldLines = append(cur.oldLines, line[1:])
			case '+':
				cur.newLines = append(cur.newLines, line[1:])
			case '\\':
				continue
			}
		}
	}
	if cur != nil {
		hunks = append(hunks, *cur)
	}
	return hunks
}

// hunkOldStart extracts the old-file start line from a hunk header.
func hunkOldStart(header string) int {
	m := hunkHeaderRe.FindStringSubmatch(header)
	if m == nil {
		return 0
	}
	start, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return start
}

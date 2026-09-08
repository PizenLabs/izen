package diff

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

// ParseUnifiedDiff parses a standard unified diff (e.g. `git diff` output)
// into per-file structures with precise OldNo/NewNo line numbers.
//
// It is intentionally resilient: unknown metadata lines are skipped, the
// "\ No newline at end of file" marker is ignored, binary diffs yield a
// FileDiff with zero hunks, and malformed hunk headers produce an error
// naming the offending line.
func ParseUnifiedDiff(rawDiff string) ([]FileDiff, error) {
	rawDiff = strings.ReplaceAll(rawDiff, "\r\n", "\n")
	if strings.TrimSpace(rawDiff) == "" {
		return nil, nil
	}
	lines := strings.Split(rawDiff, "\n")

	var files []FileDiff
	var cur *FileDiff
	var hunk *DiffHunk
	var oldNo, newNo int
	// Pending git header paths, resolved once ---/+++ arrive.
	var gitOld, gitNew string

	flushHunk := func() { hunk = nil }
	flushFile := func() {
		flushHunk()
		cur = nil
		gitOld, gitNew = "", ""
	}

	for i, raw := range lines {
		// A trailing newline yields a final empty split element that is
		// not diff content (it would otherwise parse as a bogus empty
		// unchanged line and skew line counts).
		if i == len(lines)-1 && raw == "" {
			continue
		}
		line := raw
		// Handle the marker without consuming it as content. It may arrive
		// as a standalone line or glued to the previous content line when
		// the input lacks a trailing newline before it.
		if idx := strings.Index(line, `\ No newline at end of file`); idx >= 0 {
			if strings.TrimSpace(line[:idx]) == "" {
				continue
			}
			line = line[:idx]
		}

		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushFile()
			gitOld, gitNew = parseGitHeader(line)
			files = append(files, FileDiff{OldPath: gitOld, NewPath: gitNew})
			cur = &files[len(files)-1]
		case strings.HasPrefix(line, "--- "):
			path := cleanDiffPath(strings.TrimSpace(strings.TrimPrefix(line, "--- ")))
			if cur == nil {
				files = append(files, FileDiff{})
				cur = &files[len(files)-1]
			}
			switch {
			case cur.OldPath == "":
				cur.OldPath = path
			case gitOld != "" && cur.OldPath == gitOld:
				// keep git-derived path; --- confirms it
			case cur.OldPath != path && path != "/dev/null":
				cur.OldPath = path
			}
			if path == "/dev/null" {
				cur.IsNew = true
			}
			if gitOld == "/dev/null" || gitOld == "" {
				if path == "/dev/null" {
					cur.IsNew = true
				}
			}
			flushHunk()
		case strings.HasPrefix(line, "+++ "):
			path := cleanDiffPath(strings.TrimSpace(strings.TrimPrefix(line, "+++ ")))
			if cur == nil {
				files = append(files, FileDiff{})
				cur = &files[len(files)-1]
			}
			if cur.NewPath == "" {
				cur.NewPath = path
			} else if path != "/dev/null" {
				cur.NewPath = path
			}
			if path == "/dev/null" {
				cur.IsDel = true
			}
			// Fill gaps when only +++ exists (or git header missing).
			if cur.OldPath == "" && gitOld != "" {
				cur.OldPath = gitOld
			}
			if cur.NewPath == "" && gitNew != "" {
				cur.NewPath = gitNew
			}
			flushHunk()
		case strings.HasPrefix(line, "@@ "):
			m := hunkHeaderRe.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("diff: malformed hunk header at line %d: %q", i+1, raw)
			}
			if cur == nil {
				files = append(files, FileDiff{})
				cur = &files[len(files)-1]
			}
			oldStart := atoiDefault(m[1], 1)
			oldLen := atoiDefault(m[2], 1)
			newStart := atoiDefault(m[3], 1)
			newLen := atoiDefault(m[4], 1)
			cur.Hunks = append(cur.Hunks, DiffHunk{
				OldStart:  oldStart,
				OldLength: oldLen,
				NewStart:  newStart,
				NewLength: newLen,
				Header:    line,
			})
			hunk = &cur.Hunks[len(cur.Hunks)-1]
			oldNo, newNo = oldStart, newStart
		case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
			// Binary marker: ensure a file entry exists, no hunks.
			if cur == nil {
				files = append(files, FileDiff{OldPath: gitOld, NewPath: gitNew})
				cur = &files[len(files)-1]
			}
			flushHunk()
		case strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "new file mode") ||
			strings.HasPrefix(line, "deleted file mode") || strings.HasPrefix(line, "old mode") ||
			strings.HasPrefix(line, "new mode") || strings.HasPrefix(line, "similarity index") ||
			strings.HasPrefix(line, "rename from ") || strings.HasPrefix(line, "rename to "):
			continue
		default:
			if cur == nil || hunk == nil {
				// Content outside a hunk (or stray header text) is skipped
				// for resilience against malformed diffs.
				if strings.HasPrefix(line, "@@") {
					return nil, fmt.Errorf("diff: malformed hunk header at line %d: %q", i+1, raw)
				}
				continue
			}
			if line == "" {
				// An empty split line represents an empty unchanged line.
				hunk.Lines = append(hunk.Lines, DiffLine{Type: LineUnchanged, OldNo: oldNo, NewNo: newNo})
				oldNo++
				newNo++
				continue
			}
			switch line[0] {
			case ' ':
				hunk.Lines = append(hunk.Lines, DiffLine{Type: LineUnchanged, OldNo: oldNo, NewNo: newNo, Text: line[1:]})
				oldNo++
				newNo++
			case '+':
				hunk.Lines = append(hunk.Lines, DiffLine{Type: LineAdded, NewNo: newNo, Text: line[1:]})
				newNo++
			case '-':
				hunk.Lines = append(hunk.Lines, DiffLine{Type: LineDeleted, OldNo: oldNo, Text: line[1:]})
				oldNo++
			case '\\':
				// "\ No newline..." already handled above; anything else
				// starting with backslash is ignored.
				continue
			default:
				// Malformed content line inside a hunk: treat as context to
				// avoid dropping line-number alignment entirely.
				hunk.Lines = append(hunk.Lines, DiffLine{Type: LineUnchanged, OldNo: oldNo, NewNo: newNo, Text: line})
				oldNo++
				newNo++
			}
		}
	}

	// Backfill paths from git headers and drop fully-empty entries that
	// carry no file identity and no hunks (pure noise).
	out := files[:0]
	for _, f := range files {
		if f.OldPath == "" && f.NewPath == "" && len(f.Hunks) == 0 {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

func parseGitHeader(line string) (oldPath, newPath string) {
	// Format: diff --git a/<old> b/<new> (paths may be quoted).
	rest := strings.TrimSpace(strings.TrimPrefix(line, "diff --git "))
	parts := splitGitPaths(rest)
	if len(parts) >= 2 {
		return cleanDiffPath(parts[0]), cleanDiffPath(parts[1])
	}
	return "", ""
}

func splitGitPaths(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

func cleanDiffPath(p string) string {
	p = strings.TrimSpace(p)
	// Strip trailing timestamps ("file\t2024-...") and quotes.
	if idx := strings.Index(p, "\t"); idx >= 0 {
		p = p[:idx]
	}
	p = strings.Trim(p, "\"'")
	if p == "/dev/null" || p == "dev/null" {
		return "/dev/null"
	}
	for _, prefix := range []string{"a/", "b/", "i/", "w/", "c/"} {
		if strings.HasPrefix(p, prefix) && len(p) > len(prefix) {
			return p[len(prefix):]
		}
	}
	return p
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

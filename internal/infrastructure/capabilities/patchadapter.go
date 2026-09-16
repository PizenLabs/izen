package capabilities

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/domain/ports"
	"github.com/PizenLabs/izen/internal/runtime/scope"
)

// ErrWorkspaceEscape is returned when a patch target resolves outside
// the workspace root FD handle at execution time. It aliases
// scope.ErrWorkspaceEscape; match with errors.Is.
var ErrWorkspaceEscape = scope.ErrWorkspaceEscape

// Patch strategy discriminators carried by PatchPayload.Modified. They are
// chosen by Parse and honored by Validate and Apply.
const (
	strategyDiff          = "DIFF_PATCH"
	strategySearchReplace = "SEARCH_REPLACE"
	strategyWholeFile     = "WHOLE_FILE"
)

var (
	hunkHeaderRe    = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
	searchReplaceRe = regexp.MustCompile(`(?s)<<<<<<< SEARCH\n(.*?)=======\n(.*?)>>>>>>> REPLACE`)
	fileHeaderRe    = regexp.MustCompile(`(?m)^\+\+\+ (?:a/|b/)?(.+)$`)
)

// PatchAdapter implements ports.PatchPort over the workspace filesystem with a
// fallback patch strategy: unified diff, then SEARCH/REPLACE blocks, then a
// whole-file rewrite. It never consults the domain safety policy; it is purely
// a mechanical resolver and writer.
//
// Execution-time confinement: every Apply verifies its target at USE time
// against the workspace-root FD handle (held open for the adapter
// lifetime when bound, short-lived otherwise). A symlink swapped in
// between resolution and application that points outside the root fails
// closed with ErrWorkspaceEscape before any byte is written. Internal
// symlinks resolving within the root remain permitted.
type PatchAdapter struct {
	root string
	// scopeRoot is the FD-anchored workspace handle. It is opened lazily
	// from root on first use when not bound explicitly, so every Apply is
	// verified against an open descriptor rather than a string path.
	scopeRoot *scope.Root
}

// NewPatchAdapter returns a PatchPort adapter that writes under root.
func NewPatchAdapter(root string) *PatchAdapter {
	return &PatchAdapter{root: root}
}

// NewPatchAdapterWithRoot returns a PatchPort adapter anchored at an
// already-open workspace-root FD handle. The caller retains ownership of
// root (Close); the adapter never closes a handle it did not open.
func NewPatchAdapterWithRoot(root *scope.Root) *PatchAdapter {
	p := &PatchAdapter{}
	if root != nil {
		p.scopeRoot = root
		p.root = root.RootPath()
	}
	return p
}

// Parse classifies a raw payload into a normalized PatchPayload. Unified diffs
// and SEARCH/REPLACE payloads are preserved verbatim in Modified; whole-file
// rewrites are fenced-stripped and flagged with IsFullRewrite. The target file
// is extracted from a diff header when present.
func (p *PatchAdapter) Parse(ctx context.Context, payload string) (ports.PatchPayload, error) {
	if err := ctx.Err(); err != nil {
		return ports.PatchPayload{}, err
	}
	if strings.TrimSpace(payload) == "" {
		return ports.PatchPayload{}, fmt.Errorf("patch: empty payload")
	}

	out := ports.PatchPayload{Modified: payload}
	switch detectStrategy(payload) {
	case strategyDiff:
		out.Modified = payload
		if m := fileHeaderRe.FindStringSubmatch(payload); len(m) > 1 {
			out.File = strings.TrimPrefix(m[1], "a/")
		}
	case strategySearchReplace:
		out.Modified = payload
	case strategyWholeFile:
		out.Modified = stripFences(payload)
		out.IsFullRewrite = true
	}
	return out, nil
}

// Validate checks that the patch resolves cleanly against current content. It
// is side-effect free.
func (p *PatchAdapter) Validate(ctx context.Context, patch ports.PatchPayload, current string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := resolvePatch(patch, current)
	return err
}

// Apply resolves the patch against the current on-disk content and writes the
// result, returning line-change metrics.
//
// TOCTOU contract: the target is verified at USE time against the
// workspace-root FD handle immediately before the read AND immediately
// before the write. A relative or absolute symlink swapped in after
// resolution that points outside the root forces ErrWorkspaceEscape with
// zero bytes written outside the boundary.
func (p *PatchAdapter) Apply(ctx context.Context, patch ports.PatchPayload) (ports.PatchResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.PatchResult{}, err
	}
	if patch.File == "" {
		return ports.PatchResult{}, fmt.Errorf("patch: missing target file")
	}
	// Lexical containment first (fast fail on absolute/traversal spellings).
	full := filepath.Join(p.root, filepath.Clean(patch.File))
	if rel, err := filepath.Rel(p.root, full); err != nil || strings.HasPrefix(rel, "..") {
		return ports.PatchResult{}, fmt.Errorf("%w: patch target %q escapes workspace", scope.ErrWorkspaceEscape, patch.File)
	}

	// FD-anchored use-time verification (resolve-then-swap closes here).
	scopeRoot, closeRoot, err := p.anchored()
	if err != nil {
		return ports.PatchResult{}, err
	}
	if closeRoot != nil {
		defer closeRoot()
	}
	rel := filepath.Clean(patch.File)
	if verr := scopeRoot.Verify(rel); verr != nil {
		return ports.PatchResult{}, verr
	}

	current, err := readFileIfExists(full)
	if err != nil {
		return ports.PatchResult{}, fmt.Errorf("patch: read %s: %w", patch.File, err)
	}

	resolved, err := resolvePatch(patch, current)
	if err != nil {
		return ports.PatchResult{}, err
	}

	// Re-verify immediately before the write: the execution-time gate.
	// Any symlink planted while the patch was being resolved is caught
	// here, before a single byte is written.
	if verr := scopeRoot.Verify(rel); verr != nil {
		return ports.PatchResult{}, verr
	}
	if err := scopeRoot.AtomicWrite(rel, []byte(resolved), 0o644); err != nil {
		return ports.PatchResult{}, fmt.Errorf("patch: write %s: %w", patch.File, err)
	}

	added, removed := lineDelta(current, resolved)
	return ports.PatchResult{
		File:         patch.File,
		Applied:      true,
		LinesAdded:   added,
		LinesRemoved: removed,
	}, nil
}

// anchored returns the FD-anchored workspace handle for this Apply and,
// when the handle is opened short-lived, a closer the caller must defer.
// A bound handle is returned directly (never closed by the caller).
func (p *PatchAdapter) anchored() (*scope.Root, func(), error) {
	if p.scopeRoot != nil {
		return p.scopeRoot, nil, nil
	}
	if strings.TrimSpace(p.root) == "" {
		return nil, nil, fmt.Errorf("patch: missing workspace root")
	}
	r, err := scope.Open(p.root)
	if err != nil {
		return nil, nil, err
	}
	return r, func() { _ = r.Close() }, nil
}

// detectStrategy returns the fallback strategy that interprets payload.
func detectStrategy(payload string) string {
	if looksLikeDiff(payload) {
		return strategyDiff
	}
	if strings.Contains(payload, "<<<<<<< SEARCH") && strings.Contains(payload, ">>>>>>> REPLACE") {
		return strategySearchReplace
	}
	return strategyWholeFile
}

// looksLikeDiff reports whether payload carries unified-diff structure (file
// headers and at least one hunk header).
func looksLikeDiff(s string) bool {
	hasOld, hasNew, hasHunk := false, false, false
	for _, line := range strings.Split(s, "\n") {
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

// resolvePatch produces the final content for patch against current using the
// strategy carried by the payload.
func resolvePatch(patch ports.PatchPayload, current string) (string, error) {
	switch {
	case patch.IsFullRewrite:
		return patch.Modified, nil
	case looksLikeDiff(patch.Modified):
		return applyUnifiedDiff(current, patch.Modified)
	case strings.Contains(patch.Modified, "<<<<<<< SEARCH") &&
		strings.Contains(patch.Modified, ">>>>>>> REPLACE"):
		return applySearchReplace(current, patch.Modified)
	default:
		return "", fmt.Errorf("patch: unrecognized payload format")
	}
}

// diffHunk is one parsed unified-diff hunk.
type diffHunk struct {
	oldStart int
	oldLines []string
	newLines []string
}

// applyUnifiedDiff applies hunks in raw to current bottom-up so earlier line
// indices remain valid. It errors when any hunk does not match current.
func applyUnifiedDiff(current, raw string) (string, error) {
	hunks := parseDiffHunks(raw)
	if len(hunks) == 0 {
		return "", fmt.Errorf("patch: no unified diff hunks")
	}
	lines := strings.Split(current, "\n")
	for i := len(hunks) - 1; i >= 0; i-- {
		h := hunks[i]
		start := h.oldStart - 1
		if start < 0 || start+len(h.oldLines) > len(lines) {
			return "", fmt.Errorf("patch: hunk range out of bounds")
		}
		for j, want := range h.oldLines {
			if lines[start+j] != want {
				return "", fmt.Errorf("patch: hunk does not match file content at line %d", start+j+1)
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
			if cur != nil {
				hunks = append(hunks, *cur)
			}
			cur = &diffHunk{oldStart: hunkOldStart(line)}
		case cur == nil:
			continue
		case len(line) == 0:
			cur.oldLines = append(cur.oldLines, "")
			cur.newLines = append(cur.newLines, "")
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
	var start int
	for _, r := range m[1] {
		if r < '0' || r > '9' {
			break
		}
		start = start*10 + int(r-'0')
	}
	return start
}

// applySearchReplace applies every SEARCH/REPLACE block that matches current.
// It errors when markers exist but no block matches.
func applySearchReplace(current, raw string) (string, error) {
	blocks := searchReplaceRe.FindAllStringSubmatch(raw, -1)
	if len(blocks) == 0 {
		return "", fmt.Errorf("patch: no SEARCH/REPLACE blocks")
	}
	result := current
	matched := 0
	for _, m := range blocks {
		search := strings.TrimSuffix(m[1], "\n")
		replace := strings.TrimSuffix(m[2], "\n")
		idx := strings.Index(result, search)
		if idx < 0 {
			continue
		}
		result = result[:idx] + replace + result[idx+len(search):]
		matched++
	}
	if matched == 0 {
		return "", fmt.Errorf("patch: SEARCH block does not match file content")
	}
	return result, nil
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

// readFileIfExists returns the file content, or "" when it does not exist.
func readFileIfExists(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// lineDelta computes the net added/removed line counts between two contents.
func lineDelta(before, after string) (int, int) {
	oldN, newN := lineCount(before), lineCount(after)
	switch {
	case newN >= oldN:
		return newN - oldN, 0
	default:
		return 0, oldN - newN
	}
}

// lineCount counts newline-terminated lines, treating empty content as zero.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

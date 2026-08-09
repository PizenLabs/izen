package changeset

import (
	"fmt"
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
func (c *Compiler) CompileToPatch(cs ChangeSet, originalDiskContent []byte) ([]byte, error) {
	if cs.TargetFile == "" {
		return nil, fmt.Errorf("changeset: compile target file is empty")
	}
	switch cs.Kind {
	case KindApplyDiff:
		return compileApplyDiff(cs)
	case KindReplaceFile:
		return compileUnifiedDiff(cs.TargetFile, string(originalDiskContent), cs.NewContent)
	case KindReplaceBlock:
		return compileReplaceBlock(cs, string(originalDiskContent))
	default:
		return nil, fmt.Errorf("changeset: unknown change kind %q", cs.Kind)
	}
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

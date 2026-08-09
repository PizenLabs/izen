package changeset

import (
	"path/filepath"
	"strings"
)

// Format classifies normalized model output into one of the three extraction
// paths. The normalizer strips preambles; the extractor consumes the payload.
type Format int

const (
	// FormatUnknown marks empty or unclassifiable output.
	FormatUnknown Format = iota
	// FormatDiff: the output carries standard --- a/ / +++ b/ diff headers.
	FormatDiff
	// FormatCodeBlock: the output carries markdown fenced code blocks.
	FormatCodeBlock
	// FormatText: plain text with neither diff headers nor code fences.
	FormatText
)

func (f Format) String() string {
	switch f {
	case FormatDiff:
		return "DIFF"
	case FormatCodeBlock:
		return "BLOCK"
	case FormatText:
		return "TEXT"
	default:
		return "UNKNOWN"
	}
}

// CodeBlock is one fenced code block extracted from model output. Path is empty
// when the fence header carried no explicit target (bare ```lang).
type CodeBlock struct {
	Lang    string
	Path    string
	Content string
}

// NormalizedOutput is the classified, preamble-stripped model output.
type NormalizedOutput struct {
	Format  Format
	Payload string
	Blocks  []CodeBlock
}

// Normalize strips preambles and classifies the model output format. It never
// rejects: classification is a structural decision, not a validation gate.
func Normalize(output string) NormalizedOutput {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return NormalizedOutput{Format: FormatUnknown}
	}
	if idx := diffPayloadIndex(trimmed); idx >= 0 {
		return NormalizedOutput{Format: FormatDiff, Payload: trimmed[idx:]}
	}
	blocks := parseCodeBlocks(trimmed)
	if len(blocks) > 0 {
		return NormalizedOutput{Format: FormatCodeBlock, Payload: trimmed, Blocks: blocks}
	}
	return NormalizedOutput{Format: FormatText, Payload: trimmed}
}

// diffPayloadIndex returns the byte offset of the first standard --- a/ diff
// header line, or -1 when the output carries no unified diff. A line is a diff
// header only when it opens with --- a/ AND a later line opens with +++ b/.
func diffPayloadIndex(output string) int {
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "--- a/") {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "+++ b/") {
				return strings.Index(output, line)
			}
		}
	}
	return -1
}

// diffSection is one per-file section of a unified diff payload.
type diffSection struct {
	OldPath string
	NewPath string
	Body    string
}

// parseDiffSections splits a unified-diff payload into per-file sections,
// delimited by --- a/<path> headers. A line is treated as a section header only
// when it appears outside a hunk body (a hunk-removed line whose content begins
// with "-- a/" is structurally impossible to distinguish, but hunks carry a
// leading '-' so only a removed line beginning with "-- a/" could collide — the
// inHunk guard keeps that edge case out of the section splitter).
func parseDiffSections(payload string) []diffSection {
	var sections []diffSection
	var cur *diffSection
	inHunk := false

	for _, line := range strings.Split(payload, "\n") {
		if strings.HasPrefix(line, "@@") {
			inHunk = true
		}
		if !inHunk && strings.HasPrefix(line, "--- a/") {
			if cur != nil {
				sections = append(sections, *cur)
			}
			cur = &diffSection{OldPath: strings.TrimPrefix(line, "--- a/"), Body: line}
			continue
		}
		if cur == nil {
			continue // preamble before the first section header
		}
		if strings.HasPrefix(line, "+++ b/") {
			cur.NewPath = strings.TrimPrefix(line, "+++ b/")
		}
		cur.Body += "\n" + line
	}
	if cur != nil {
		sections = append(sections, *cur)
	}
	return sections
}

// parseCodeBlocks extracts fenced code blocks (```lang ... ```) from model
// output. Fence headers may carry an inline target path (```html:index.html,
// ```js script.js, ```file=index.html). Bare ```lang blocks are retained with
// an empty Path so the extractor can resolve the target from the pipeline hint.
func parseCodeBlocks(output string) []CodeBlock {
	var blocks []CodeBlock
	var cur *CodeBlock
	inFence := false

	flush := func() {
		if cur != nil {
			blocks = append(blocks, *cur)
		}
		cur = nil
		inFence = false
	}

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case inFence:
			if strings.HasPrefix(trimmed, "```") {
				flush()
				continue
			}
			cur.Content += line + "\n"
		default:
			if !strings.HasPrefix(trimmed, "```") {
				continue
			}
			header := strings.TrimPrefix(trimmed, "```")
			lang, path, _ := parseFenceHeader(header)
			cur = &CodeBlock{Lang: lang, Path: path}
			inFence = true
		}
	}
	flush()
	return blocks
}

// parseFenceHeader splits a code-fence opener into a language tag and an
// optional inline target path. ok is false when the header is empty.
func parseFenceHeader(header string) (lang, path string, ok bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", "", false
	}
	if lower := strings.ToLower(header); strings.HasPrefix(lower, "file=") {
		return "", strings.TrimSpace(header[len("file="):]), true
	}
	if idx := strings.IndexByte(header, ':'); idx >= 0 {
		lang = strings.TrimSpace(header[:idx])
		path = strings.TrimSpace(header[idx+1:])
		return lang, path, true
	}
	if fields := strings.Fields(header); len(fields) >= 2 {
		return fields[0], fields[1], true
	}
	return strings.TrimSpace(header), "", true
}

// Extract converts raw model output into an ordered ChangeSet IR. It is the
// strict Output Normalizer + Change Extractor stage: no file is ever touched.
//
// targetFile is the workspace-relative file being edited; originalDiskContent is
// its authoritative on-disk snapshot (used for anchor extraction and full-file
// classification). A change that cannot be mapped safely returns
// ErrAmbiguousChange and the pipeline must PAUSE.
func Extract(output string, targetFile string, originalDiskContent []byte) ([]ChangeSet, error) {
	return ExtractNormalized(Normalize(output), targetFile, originalDiskContent)
}

// ExtractNormalized converts an already-normalized payload into ChangeSet IR.
func ExtractNormalized(no NormalizedOutput, targetFile string, originalDiskContent []byte) ([]ChangeSet, error) {
	switch no.Format {
	case FormatDiff:
		return extractDiff(no, targetFile)
	case FormatCodeBlock:
		return extractBlocks(no, targetFile, originalDiskContent)
	case FormatText:
		return extractText(no, targetFile, originalDiskContent)
	default:
		return nil, ErrAmbiguousChange
	}
}

// extractDiff maps per-file unified diff sections onto ApplyDiff ChangeSets.
// Sections targeting a different file than targetFile are skipped.
func extractDiff(no NormalizedOutput, targetFile string) ([]ChangeSet, error) {
	sections := parseDiffSections(no.Payload)
	if len(sections) == 0 {
		return nil, ErrAmbiguousChange
	}
	var out []ChangeSet
	for _, s := range sections {
		path := s.NewPath
		if path == "" {
			path = s.OldPath
		}
		if path == "" {
			continue
		}
		if targetFile != "" && cleanRel(path) != cleanRel(targetFile) {
			continue // section belongs to a different file
		}
		out = append(out, ChangeSet{
			TargetFile: firstNonEmpty(path, targetFile),
			Kind:       KindApplyDiff,
			NewContent: s.Body,
			Confidence: 1.0,
		})
	}
	if len(out) == 0 {
		return nil, ErrAmbiguousChange
	}
	return out, nil
}

// extractBlocks maps fenced code blocks onto ReplaceFile or ReplaceBlock
// ChangeSets. Any block that cannot be mapped safely aborts the whole
// extraction (strict ambiguity guard — no destructive fallbacks).
func extractBlocks(no NormalizedOutput, targetFile string, original []byte) ([]ChangeSet, error) {
	if len(no.Blocks) == 0 {
		return nil, ErrAmbiguousChange
	}
	originalStr := string(original)
	var out []ChangeSet
	for _, b := range no.Blocks {
		if b.Path != "" && targetFile != "" && cleanRel(b.Path) != cleanRel(targetFile) {
			continue // block belongs to a different file
		}
		cs, err := classifyBlock(b, targetFile, originalStr)
		if err != nil {
			return nil, err
		}
		out = append(out, cs)
	}
	if len(out) == 0 {
		return nil, ErrAmbiguousChange
	}
	return out, nil
}

// extractText maps preamble-stripped plain text onto a single ChangeSet via the
// same block classification path (no fence header to carry a target).
func extractText(no NormalizedOutput, targetFile string, original []byte) ([]ChangeSet, error) {
	if no.Payload == "" {
		return nil, ErrAmbiguousChange
	}
	cs, err := classifyBlock(CodeBlock{Lang: "", Path: "", Content: no.Payload}, targetFile, string(original))
	if err != nil {
		return nil, err
	}
	return []ChangeSet{cs}, nil
}

// classifyBlock decides whether a code block is a full-file replacement or an
// anchored partial replacement. A block is a full-file replacement when it
// carries an explicit full-file indicator (a fence-header target path) or when
// it structurally covers >= fullFileCoverageThreshold of the on-disk original.
// Otherwise it is treated as a partial snippet and anchored to the closest
// original line; an unmatchable snippet aborts with ErrAmbiguousChange.
func classifyBlock(b CodeBlock, targetFile string, original string) (ChangeSet, error) {
	path := b.Path
	if path == "" {
		path = targetFile
	}
	if path == "" {
		return ChangeSet{}, ErrAmbiguousChange
	}

	content := strings.TrimRight(b.Content, "\n")
	if b.Path != "" || coverageRatio(original, content) >= fullFileCoverageThreshold {
		return ChangeSet{
			TargetFile: path,
			Kind:       KindReplaceFile,
			NewContent: content,
			Confidence: 1.0,
		}, nil
	}

	anchor, sim, ok := matchAnchor(original, content)
	if !ok {
		return ChangeSet{}, ErrAmbiguousChange
	}
	return ChangeSet{
		TargetFile: path,
		Kind:       KindReplaceBlock,
		OldContent: anchor,
		NewContent: content,
		Confidence: sim,
	}, nil
}

// fullFileCoverageThreshold is the fraction of the on-disk original's non-empty
// lines that a block must structurally cover to be classified as a whole-file
// replacement (>95% per the architectural spec).
const fullFileCoverageThreshold = 0.95

// coverageRatio reports the fraction of the original's non-empty lines that
// appear (by trimmed equality) inside the block.
func coverageRatio(original, block string) float64 {
	origLines := nonEmptyLines(original)
	if len(origLines) == 0 {
		return 0
	}
	blockSet := make(map[string]struct{}, 16)
	for _, line := range nonEmptyLines(block) {
		blockSet[line] = struct{}{}
	}
	matched := 0
	for _, line := range origLines {
		if _, ok := blockSet[line]; ok {
			matched++
		}
	}
	return float64(matched) / float64(len(origLines))
}

// minAnchorSimilarity is the lowest line similarity that still yields a safe
// ReplaceBlock anchor. Below it the snippet cannot be mapped confidently and the
// pipeline pauses.
const minAnchorSimilarity = 0.6

// matchAnchor resolves the exact on-disk text (OldContent) that the snippet
// should replace. It prefers an exact line match, then a fuzzy best-line match.
// ok=false when no line is a confident anchor.
func matchAnchor(original, snippet string) (anchor string, sim float64, ok bool) {
	s := strings.TrimSpace(snippet)
	if s == "" {
		return "", 0, false
	}
	for _, line := range splitLines(original) {
		if strings.TrimSpace(line) == s {
			return line, 1.0, true
		}
	}
	bestLine, bestSim := "", 0.0
	for _, line := range splitLines(original) {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if d := runeSimilarity(s, t); d > bestSim {
			bestSim, bestLine = d, t
		}
	}
	if bestLine == "" || bestSim < minAnchorSimilarity {
		return "", 0, false
	}
	return bestLine, bestSim, true
}

// runeSimilarity is the normalized longest-common-subsequence similarity over
// runes: 2*|LCS| / (|a| + |b|). It is bounded to [0,1] and reaches 1.0 for
// identical strings.
func runeSimilarity(a, b string) float64 {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 || len(br) == 0 {
		return 0
	}
	lcs := lcsLen(ar, br)
	return 2 * float64(lcs) / float64(len(ar)+len(br))
}

// lcsLen computes the length of the longest common subsequence of two rune
// slices using the classic O(n*m) dynamic program. Inputs are short (single
// lines / snippets), so the quadratic bound is acceptable.
func lcsLen(a, b []rune) int {
	n, m := len(a), len(b)
	prev := make([]int, m+1)
	cur := make([]int, m+1)
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			switch {
			case a[i-1] == b[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
		for j := range cur {
			cur[j] = 0
		}
	}
	return prev[m]
}

// splitLines splits s into lines (without trailing empty element for a final
// newline).
func splitLines(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

// nonEmptyLines returns the trimmed non-empty lines of s.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range splitLines(s) {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// cleanRel normalizes a workspace-relative path for comparison.
func cleanRel(p string) string {
	return filepath.ToSlash(filepath.Clean(p))
}

// firstNonEmpty returns a, falling back to b when a is empty.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

package harness

import (
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// Sentinel errors from the extraction pipeline. Parsing failure is distinct
// from execution failure: these signal a model-output translation problem, not
// a filesystem/execution problem, and therefore MUST NOT trigger model retries
// on their own. The pipeline falls through to the next tier instead.
var (
	// ErrNoTier1Match means the raw output could not be parsed as a strict
	// structured artifact (unified diff or XML).
	ErrNoTier1Match = errors.New("harness: no strict structured artifact found")
	// ErrAmbiguousMatch means Tier 2 located multiple plausible matches of
	// comparable score. Per the architectural invariant, ambiguity is a
	// first-class failure and MUST fail closed (never guess).
	ErrAmbiguousMatch = errors.New("harness: ambiguous match, refusing to guess")
	// ErrNoMatch means no tier could recover a candidate.
	ErrNoMatch = errors.New("harness: no candidate recoverable from output")
)

// uniDiffHeadRe matches a unified diff file header: "--- a/path".
var uniDiffHeadRe = regexp.MustCompile(`(?m)^---\s+[ab]/(\S+)\s*$`)

// artifactFileRe matches the structured XML-like artifact block:
// "<artifact file="...">...</artifact>" or "<file path="...">...</file>".
var artifactFileRe = regexp.MustCompile(`(?s)<(?:artifact|file)\s+(?:file|path)="([^"]+)"\s*>([\s\S]*?)</(?:artifact|file)>`)

// StrictExtractor implements Tier 1: it parses the raw output as either a
// unified diff or a structured XML artifact block. On success it returns
// Tier 1 evidence (ExactParse = true, Confidence = 1.0).
type StrictExtractor struct{}

// NewStrictExtractor returns a configured Tier 1 extractor.
func NewStrictExtractor() *StrictExtractor { return &StrictExtractor{} }

// Extract attempts a strict parse of raw. If no strict artifact is present it
// returns ErrNoTier1Match so the pipeline may descend to Tier 2.
func (s *StrictExtractor) Extract(raw []byte) (CandidateArtifact, error) {
	var cand CandidateArtifact

	if m := artifactFileRe.FindSubmatch(raw); m != nil {
		path := string(m[1])
		body := string(m[2])
		body = strings.TrimSpace(body)
		cand = CandidateArtifact{
			TargetFile: path,
			RawPatch:   []byte(body),
			Diff:       formatSimpleDiff(path, body),
			Evidence: ArtifactEvidence{
				Tier:       Tier1Strict,
				Confidence: 1.0,
				ExactParse: true,
			},
		}
		return cand, nil
	}

	if m := uniDiffHeadRe.FindSubmatch(raw); m != nil {
		path := string(m[1])
		diffStr := strings.TrimSpace(string(raw))
		cand = CandidateArtifact{
			TargetFile: path,
			RawPatch:   raw,
			Diff:       diffStr,
			Evidence: ArtifactEvidence{
				Tier:       Tier1Strict,
				Confidence: 1.0,
				ExactParse: true,
			},
		}
		return cand, nil
	}

	return cand, ErrNoTier1Match
}

// EvidenceMatcher implements Tier 2: it locates a single unambiguous
// occurrence of the candidate content within the original file using
// multi-factor anchoring. Ambiguity fails closed via ErrAmbiguousMatch.
type EvidenceMatcher struct {
	// minConfidence is the threshold below which a located match is rejected
	// as too weak.
	minConfidence float64
	// ambiguityGap is the score gap below which two top candidates are treated
	// as ambiguous (the smaller the gap, the more ambiguous).
	ambiguityGap float64
}

// NewEvidenceMatcher returns a Tier 2 matcher with sensible defaults.
func NewEvidenceMatcher() *EvidenceMatcher {
	return &EvidenceMatcher{
		minConfidence: 0.6,
		ambiguityGap:  0.1,
	}
}

// candidate represents a single anchored match of the snippet within original.
type candidate struct {
	start     int
	end       int
	score     float64
	anchors   int
	startLine int
	endLine   int
}

// Extract anchors the snippet bytes against the original file. It returns a
// candidate carrying Tier 2 fuzzy-match evidence. If several candidates are
// within ambiguityGap of the best score, it returns ErrAmbiguousMatch.
func (m *EvidenceMatcher) Extract(snippet []byte, original []byte, targetFile string) (CandidateArtifact, error) {
	s := string(snippet)
	o := string(original)

	if s == "" {
		return CandidateArtifact{}, ErrNoMatch
	}

	// Exact substring presence is the strongest single anchor.
	exactIdx := indexAll(o, s)

	cands := make([]candidate, 0, 2)

	// Candidate 1: exact substring occurrence (if any).
	if len(exactIdx) > 0 {
		for _, idx := range exactIdx {
			cands = append(cands, m.scoreExact(o, s, idx))
		}
	}

	// Candidate 2: line-anchored fuzzy match via difflib (handles whitespace /
	// indentation drift).
	if fz := m.fuzzyLineMatch(o, s); fz.start >= 0 {
		cands = append(cands, fz)
	}

	if len(cands) == 0 {
		return CandidateArtifact{}, ErrNoMatch
	}

	sort.Slice(cands, func(i, j int) bool {
		return cands[i].score > cands[j].score
	})

	best := cands[0]

	// Fail-closed ambiguity check. Ambiguity is defined as multiple DISTINCT
	// source locations scoring within ambiguityGap of the best candidate. Exact
	// and fuzzy candidates that resolve to the same location reinforce each
	// other rather than conflict, so they are merged before the check.
	distinct := distinctByLocation(cands)
	if len(distinct) > 1 {
		second := distinct[1]
		if best.score-second.score < m.ambiguityGap {
			return CandidateArtifact{}, ErrAmbiguousMatch
		}
	}
	if len(exactIdx) > 1 {
		// Two distinct exact occurrences means the snippet is not unique.
		return CandidateArtifact{}, ErrAmbiguousMatch
	}

	ev := ArtifactEvidence{
		Tier:        Tier2Evidence,
		SourceRange: SourceRange{StartLine: best.startLine, EndLine: best.endLine, StartCol: 1, EndCol: 1},
		MatchScore:  best.score,
		Confidence:  best.score,
		AnchorCount: best.anchors,
		FuzzyMatch:  true,
	}
	if ev.Confidence < m.minConfidence {
		return CandidateArtifact{}, ErrNoMatch
	}

	return CandidateArtifact{
		TargetFile: targetFile,
		RawPatch:   snippet,
		Diff:       formatSimpleDiff(targetFile, s),
		Evidence:   ev,
	}, nil
}

// scoreExact scores an exact substring occurrence by its frequency and the
// quality of its surrounding anchors.
func (m *EvidenceMatcher) scoreExact(o, s string, idx int) candidate {
	startLine := 1 + strings.Count(o[:idx], "\n")
	endLine := startLine + strings.Count(s, "\n")
	occ := strings.Count(o, s)
	// A unique occurrence is a strong match; repeated occurrences are weaker
	// and treated as ambiguous by the caller.
	freq := 1.0
	if occ > 1 {
		freq = 1.0 / float64(occ)
	}
	score := 0.9 * freq
	return candidate{
		start:     idx,
		end:       idx + len(s),
		score:     score,
		anchors:   1,
		startLine: startLine,
		endLine:   endLine,
	}
}

// fuzzyLineMatch uses the Myers diff to find the best matching contiguous run
// of lines in o for the lines of s, tolerating indentation/whitespace drift.
func (m *EvidenceMatcher) fuzzyLineMatch(o, s string) candidate {
	oLines := strings.Split(o, "\n")
	sLines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(sLines) == 0 {
		return candidate{start: -1}
	}

	// Build the target as a single joined line for diff scoring; instead we
	// reuse difflib to find matching blocks.
	sm := difflib.NewMatcher(oLines, sLines)
	blocks := sm.GetMatchingBlocks()
	if len(blocks) == 0 {
		return candidate{start: -1}
	}
	best := blocks[0]
	for _, b := range blocks[1:] {
		if b.Size > best.Size {
			best = b
		}
	}
	if best.Size == 0 {
		return candidate{start: -1}
	}

	coverage := float64(best.Size) / float64(len(sLines))
	if coverage < 0.5 {
		return candidate{start: -1}
	}

	return candidate{
		start:     best.A,
		end:       best.A + best.Size,
		score:     coverage,
		anchors:   best.Size,
		startLine: best.A + 1,
		endLine:   best.A + best.Size,
	}
}

// PartialOutputReconstructor implements Tier 3: it reconstructs a candidate
// from truncated / full-text prose output using the Myers diff algorithm.
// Tier 3 sets Inferred = true and Confidence <= 0.6; it may PROPOSE but never
// authorizes execution.
type PartialOutputReconstructor struct{}

// NewPartialOutputReconstructor returns a configured Tier 3 reconstructor.
func NewPartialOutputReconstructor() *PartialOutputReconstructor {
	return &PartialOutputReconstructor{}
}

// Extract reconstructs a candidate diff for targetFile against original. It
// detects truncation and always flags the output as Inferred with a confidence
// capped at 0.6.
func (r *PartialOutputReconstructor) Extract(raw []byte, original []byte, targetFile string) CandidateArtifact {
	s := string(raw)
	truncated := looksTruncated(s)

	oLines := strings.Split(strings.TrimRight(string(original), "\n"), "\n")
	nLines := strings.Split(strings.TrimRight(s, "\n"), "\n")

	// Detect EOF-style truncation: new content ends abruptly without a newline
	// and shares a leading prefix with the original.
	truncated = truncated || r.detectEOFCut(s, oLines)

	udiff := unifiedDiff(oLines, nLines, targetFile)

	return CandidateArtifact{
		TargetFile: targetFile,
		RawPatch:   []byte(s),
		Diff:       udiff,
		Evidence: ArtifactEvidence{
			Tier:       Tier3Inference,
			Confidence: 0.6,
			MatchScore: 0.5,
			Inferred:   true,
			Truncated:  truncated,
		},
	}
}

// detectEOFCut heuristically detects a truncated full-text response: the new
// content's final line does not end in a newline and is not a terminator.
func (r *PartialOutputReconstructor) detectEOFCut(s string, oLines []string) bool {
	if strings.HasSuffix(s, "\n") {
		return false
	}
	if s == "" {
		return false
	}
	last := s[strings.LastIndex(s, "\n")+1:]
	if last == "" {
		return false
	}
	// A hanging partial final line that begins like an existing source line is a
	// strong truncation signal.
	for _, ol := range oLines {
		trim := strings.TrimSpace(ol)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(last), trim[:min(len(trim), 4)]) && len(last) < len(trim) {
			return true
		}
	}
	// A hanging line with unclosed delimiters (open string/grouping/paren) is
	// also a strong truncation signal: real output would terminate these.
	if hasUnclosedDelims(last) {
		return true
	}
	return false
}

// hasUnclosedDelims reports whether a code fragment leaves a delimiter open.
func hasUnclosedDelims(fragment string) bool {
	opens := 0
	inString := false
	var quote byte
	for i := 0; i < len(fragment); i++ {
		c := fragment[i]
		switch {
		case inString:
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				inString = false
			}
		case c == '"' || c == '\'' || c == '`':
			inString = true
			quote = c
		case c == '(' || c == '[' || c == '{':
			opens++
		case c == ')' || c == ']' || c == '}':
			opens--
		}
	}
	return inString || opens > 0
}

// ExtractorPipeline orchestrates the three sequential tiers. A Tier 1 strict
// parse short-circuits; otherwise it descends to Tier 2 (anchor matching) and
// finally Tier 3 (reconstruction). It is safe for concurrent use as it holds
// no mutable state beyond the tier components.
type ExtractorPipeline struct {
	tier1 *StrictExtractor
	tier2 *EvidenceMatcher
	tier3 *PartialOutputReconstructor
}

// NewExtractorPipeline returns a pipeline with the default tier components.
func NewExtractorPipeline() *ExtractorPipeline {
	return &ExtractorPipeline{
		tier1: NewStrictExtractor(),
		tier2: NewEvidenceMatcher(),
		tier3: NewPartialOutputReconstructor(),
	}
}

// Extract runs the pipeline over raw model output. It requires the original
// file content for Tier 2/3 anchoring; pass nil if the file does not exist.
//
// The returned error is always a translation-layer failure (one of the
// harness sentinel errors) and never indicates execution failure.
func (p *ExtractorPipeline) Extract(raw []byte, original []byte, targetFile string) (CandidateArtifact, error) {
	// Tier 1: strict structured parse.
	cand, err := p.tier1.Extract(raw)
	if err == nil {
		return cand, nil
	}
	if !errors.Is(err, ErrNoTier1Match) {
		return CandidateArtifact{}, err
	}

	// Tier 2: anchor matching against original content. Requires original.
	if len(original) > 0 {
		cand, err = p.tier2.Extract(raw, original, targetFile)
		if err == nil {
			return cand, nil
		}
		if errors.Is(err, ErrAmbiguousMatch) {
			// Ambiguity is a first-class failure; never guess.
			return CandidateArtifact{}, ErrAmbiguousMatch
		}
	}

	// Tier 3: reconstruction from truncated / prose output. May PROPOSE only.
	return p.tier3.Extract(raw, original, targetFile), nil
}

// ---- helpers --------------------------------------------------------------

// distinctByLocation collapses candidates that resolve to the same source
// span, keeping the highest-scoring representative of each distinct location.
func distinctByLocation(cands []candidate) []candidate {
	seen := map[[2]int]bool{}
	var out []candidate
	for _, c := range cands {
		key := [2]int{c.startLine, c.endLine}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// indexAll returns all byte start indices of sub within s.
func indexAll(s, sub string) []int {
	var out []int
	if sub == "" {
		return out
	}
	from := 0
	for {
		i := strings.Index(s[from:], sub)
		if i < 0 {
			break
		}
		pos := from + i
		out = append(out, pos)
		from = pos + len(sub)
	}
	return out
}

// looksTruncated reports obvious truncation markers in raw output.
func looksTruncated(s string) bool {
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	return strings.Contains(lower, "...truncated") ||
		strings.Contains(lower, "[truncated]") ||
		strings.Contains(s, "\x00")
}

// unifiedDiff computes a unified diff between base and target lines.
func unifiedDiff(base, target []string, name string) string {
	d := difflib.UnifiedDiff{
		A:        base,
		B:        target,
		FromFile: "a/" + name,
		ToFile:   "b/" + name,
		Context:  3,
	}
	text, err := difflib.GetUnifiedDiffString(d)
	if err != nil {
		return ""
	}
	return strings.TrimRight(text, "\n")
}

// formatSimpleDiff renders a synthetic diff for a full-content candidate.
func formatSimpleDiff(name, content string) string {
	lines := strings.Split(content, "\n")
	base := []string{""}
	target := make([]string, 0, len(lines))
	for _, l := range lines {
		if l == "" {
			target = append(target, " ")
		} else {
			target = append(target, l)
		}
	}
	return unifiedDiff(base, target, name)
}

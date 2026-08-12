package extractor

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/PizenLabs/izen/pkg/ir"
)

// Extractor is the contract of the evidence-based extraction pipeline. Given
// raw LLM output it returns an ExtractionResult whose decision is a pure
// function of the observed EvidenceFlags — never of a confidence number.
type Extractor interface {
	Extract(ctx context.Context, raw string) ExtractionResult
}

// Header patterns accepted by MarkdownFenceExtractor. A fence header always
// names an artifact path; headers without a path are treated as prose and
// ignored.
var (
	// ```lang:path
	fenceColonRe = regexp.MustCompile(`^` + "```" + `\s*([\w.+/-]+)\s*:\s*(\S+)\s*$`)
	// ```lang path
	fenceSpaceRe = regexp.MustCompile(`^` + "```" + `\s*([\w.+/-]+)\s+(\S+)\s*$`)
	// === FILE: path (optional trailing ===)
	fileMarkerRe = regexp.MustCompile(`^=== FILE:\s*(\S+)\s*(?:===)?\s*$`)
)

// isHeaderLine reports whether line opens a supported header construct.
func isHeaderLine(line string) bool {
	return fenceColonRe.MatchString(line) || fenceSpaceRe.MatchString(line) || fileMarkerRe.MatchString(line)
}

// MarkdownFenceExtractor parses code blocks with headers out of raw LLM
// output. It recognises ```lang:path, ```lang path and === FILE: path
// headers. Text outside recognised blocks is ignored, so narrative prose
// around the blocks does not poison the extraction; an unclosed fence,
// however, invalidates the whole result.
type MarkdownFenceExtractor struct{}

// NewMarkdownFenceExtractor returns a MarkdownFenceExtractor.
func NewMarkdownFenceExtractor() *MarkdownFenceExtractor {
	return &MarkdownFenceExtractor{}
}

// Extract parses raw and returns the ExtractionResult with a deterministic
// evidence set. A successful run sets EvValidUTF8, EvValidFenceHeader,
// EvPathInHeader and EvFenceClosed and yields one ir.Artifact per complete
// block.
func (m *MarkdownFenceExtractor) Extract(ctx context.Context, raw string) ExtractionResult {
	if !utf8.ValidString(raw) {
		return ExtractionResult{Raw: raw}
	}

	evidences := []EvidenceFlag{EvValidUTF8}
	lines := strings.Split(raw, "\n")
	var artifacts []ir.Artifact
	foundHeader := false
	unclosedFence := false

	for i := 0; i < len(lines); {
		line := lines[i]
		switch {
		case fenceColonRe.MatchString(line):
			m := fenceColonRe.FindStringSubmatch(line)
			content, consumed, closed := collectFence(lines, i+1)
			if closed {
				artifacts = append(artifacts, newFileArtifact(m[2], m[1], content))
			} else {
				unclosedFence = true
			}
			foundHeader = true
			i += 1 + consumed
		case fenceSpaceRe.MatchString(line):
			m := fenceSpaceRe.FindStringSubmatch(line)
			content, consumed, closed := collectFence(lines, i+1)
			if closed {
				artifacts = append(artifacts, newFileArtifact(m[2], m[1], content))
			} else {
				unclosedFence = true
			}
			foundHeader = true
			i += 1 + consumed
		case fileMarkerRe.MatchString(line):
			m := fileMarkerRe.FindStringSubmatch(line)
			content, consumed := collectFileBlock(lines, i+1)
			artifacts = append(artifacts, ir.NewFile(m[1], joinContent(content)))
			foundHeader = true
			i += 1 + consumed
		default:
			i++
		}
	}

	if foundHeader {
		evidences = append(evidences, EvValidFenceHeader, EvPathInHeader)
	}
	if !unclosedFence {
		evidences = append(evidences, EvFenceClosed)
	}
	return ExtractionResult{Artifacts: artifacts, Evidences: evidences, Raw: raw}
}

// newFileArtifact builds a file artifact carrying its language as metadata.
func newFileArtifact(path, lang string, lines []string) ir.Artifact {
	a := ir.NewFile(path, joinContent(lines))
	a.Metadata.Language = lang
	return a
}

// collectFence accumulates the content of a ```lang[:path] block starting at
// start. It returns the collected lines, how many lines were consumed
// (including the closing fence when present), and whether the fence was
// closed. Only a line that trims to exactly ``` closes the block.
func collectFence(lines []string, start int) (content []string, consumed int, closed bool) {
	var out []string
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```" {
			return out, i - start + 1, true
		}
		out = append(out, lines[i])
	}
	return out, len(lines) - start, false
}

// collectFileBlock accumulates the body of a === FILE: path block starting at
// start. The body runs until the next recognised header line or end of input;
// trailing whitespace-only separator lines are trimmed so block boundaries do
// not leak into file content.
func collectFileBlock(lines []string, start int) (content []string, consumed int) {
	var out []string
	for i := start; i < len(lines); i++ {
		if isHeaderLine(lines[i]) {
			return trimTrailingBlank(out), i - start
		}
		out = append(out, lines[i])
	}
	return trimTrailingBlank(out), len(lines) - start
}

// trimTrailingBlank drops trailing whitespace-only lines from a block body.
func trimTrailingBlank(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[:end]
}

// joinContent joins collected lines exactly, without a trailing newline.
func joinContent(lines []string) []byte {
	return []byte(strings.Join(lines, "\n"))
}

// jsonArtifact is one entry of a JSON extraction envelope.
type jsonArtifact struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Kind    string `json:"kind"`
}

// jsonEnvelope is the canonical structured output shape accepted by
// JSONExtractor. A top-level array of jsonArtifact entries is also accepted.
type jsonEnvelope struct {
	Artifacts []jsonArtifact `json:"artifacts"`
}

// JSONExtractor parses structured JSON output and validates it against the
// envelope schema before emitting artifacts. On any schema violation it emits
// no artifacts and no EvValidJSONSchema, so the resulting decision is
// DecisionRejectAndRetry.
//
// It accepts a bare {"artifacts": [...]} object, a top-level [...] array, or
// either form wrapped in a ```json fence.
type JSONExtractor struct{}

// NewJSONExtractor returns a JSONExtractor.
func NewJSONExtractor() *JSONExtractor {
	return &JSONExtractor{}
}

// Extract parses raw structured output and returns an ExtractionResult with
// the deterministic evidence set. A schema-valid run sets all five evidence
// flags including EvValidJSONSchema.
func (j *JSONExtractor) Extract(ctx context.Context, raw string) ExtractionResult {
	if !utf8.ValidString(raw) {
		return ExtractionResult{Raw: raw}
	}

	evidences := []EvidenceFlag{EvValidUTF8}
	body := strings.TrimSpace(raw)
	fenced := false

	if strings.HasPrefix(body, "```") {
		fenced = true
		inner, closed := unwrapFence(body)
		if !closed {
			return ExtractionResult{Evidences: evidences, Raw: raw}
		}
		evidences = append(evidences, EvFenceClosed)
		body = strings.TrimSpace(inner)
	}

	var entries []jsonArtifact
	switch {
	case strings.HasPrefix(body, "["):
		var arr []jsonArtifact
		if err := json.Unmarshal([]byte(body), &arr); err != nil {
			return ExtractionResult{Evidences: evidences, Raw: raw}
		}
		entries = arr
	default:
		var env jsonEnvelope
		if err := json.Unmarshal([]byte(body), &env); err != nil {
			return ExtractionResult{Evidences: evidences, Raw: raw}
		}
		entries = env.Artifacts
	}
	if !fenced {
		evidences = append(evidences, EvFenceClosed)
	}

	if len(entries) == 0 {
		return ExtractionResult{Evidences: evidences, Raw: raw}
	}
	evidences = append(evidences, EvValidFenceHeader)

	artifacts := make([]ir.Artifact, 0, len(entries))
	for _, e := range entries {
		if e.Path == "" {
			return ExtractionResult{Evidences: evidences, Raw: raw}
		}
		kind := ir.ArtifactFile
		if e.Kind != "" {
			kind = ir.ArtifactKind(e.Kind)
		}
		if !kind.Valid() {
			return ExtractionResult{Evidences: evidences, Raw: raw}
		}
		artifacts = append(artifacts, ir.NewArtifact("", e.Path, kind, []byte(e.Content)))
	}
	evidences = append(evidences, EvPathInHeader, EvValidJSONSchema)

	return ExtractionResult{Artifacts: artifacts, Evidences: evidences, Raw: raw}
}

// unwrapFence strips a leading ```fence line and its matching closing ```,
// returning the inner text and whether the fence was closed. The closer is
// the last line that trims to exactly ```.
func unwrapFence(s string) (inner string, closed bool) {
	newline := strings.IndexByte(s, '\n')
	if newline < 0 {
		return "", false
	}
	rest := s[newline+1:]
	lines := strings.Split(rest, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			return strings.Join(lines[:i], "\n"), true
		}
	}
	return "", false
}

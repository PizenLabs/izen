package extractor

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/PizenLabs/izen/pkg/ir"
)

// ErrContractViolation is returned by ArtifactContractParser.Parse when the
// raw LLM output contains no valid artifact contract at all. It is a sentinel
// so the failure policy layer can classify the rejection deterministically:
// a contract violation is always a repromptable retry, never a hard abort.
var ErrContractViolation = errors.New("extractor: no valid artifact contract found")

// artifactOpenRe matches the formal V3 artifact fence opener
// ":::artifact <path>". The path is a single non-whitespace token and may
// optionally carry a language annotation via ":::artifact lang:path".
var artifactOpenRe = regexp.MustCompile(`^:::\s*artifact\s+(\S+)\s*$`)

// artifactCloseRe matches the formal artifact fence closer ":::". A closing
// fence is any line whose trimmed form is exactly ":::".
var artifactCloseRe = regexp.MustCompile(`^:::\s*$`)

// ContractBlock is one fully-formed artifact contract extracted from raw LLM
// output.
type ContractBlock struct {
	// Path is the workspace-relative target path declared by the contract.
	Path string
	// Language is the optional language tag declared by the contract
	// (e.g. "go" from "```go:main.go" or ":::artifact go:main.go").
	Language string
	// Lines holds the raw content lines between the fence openers/closers,
	// unmodified. Normalization happens later, in NormalizeArtifact.
	Lines []string
}

// ArtifactContractParser is the strict contract parser of the V3 Artifact
// Protocol. Unlike the lenient MarkdownFenceExtractor, it ONLY recognises
// formal artifact fences — ":::artifact <path>" ... ":::" and
// "```lang:path" ... "```". Every line outside a recognised fence is ignored
// naturally by the scanner: no regex or heuristic strippers ever run over the
// free text, so narrative prose can never poison an extraction.
//
// If raw output contains no valid artifact contract, Parse returns
// ErrContractViolation. The zero value is ready to use.
type ArtifactContractParser struct{}

// NewArtifactContractParser returns a ready-to-use ArtifactContractParser.
func NewArtifactContractParser() *ArtifactContractParser {
	return &ArtifactContractParser{}
}

// Parse scans raw and returns every artifact contract found, in order. Text
// outside formal fences is dropped without being examined. It returns
// ErrContractViolation when no complete contract exists in raw.
func (p *ArtifactContractParser) Parse(ctx context.Context, raw string) ([]ContractBlock, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if !utf8.ValidString(raw) {
		return nil, ErrContractViolation
	}

	lines := strings.Split(raw, "\n")
	var blocks []ContractBlock
	var pending *ContractBlock

	appendPending := func() {
		if pending != nil {
			blocks = append(blocks, *pending)
			pending = nil
		}
	}

	for _, line := range lines {
		switch {
		case pending == nil && artifactOpenRe.MatchString(line):
			path, lang := splitPathAndLang(artifactOpenRe.FindStringSubmatch(line)[1])
			pending = &ContractBlock{Path: path, Language: lang}
		case pending == nil && fenceColonRe.MatchString(line):
			m := fenceColonRe.FindStringSubmatch(line)
			pending = &ContractBlock{Path: m[2], Language: m[1]}
		case pending != nil && artifactCloseRe.MatchString(line):
			appendPending()
		case pending != nil && strings.TrimSpace(line) == "```":
			appendPending()
		case pending != nil:
			pending.Lines = append(pending.Lines, line)
		default:
			// Un-contracted text: dropped without inspection.
		}
	}
	// An unclosed pending fence is an incomplete contract: discard it so a
	// dangling opener can never yield a partial artifact.
	if pending != nil {
		pending = nil
	}

	if len(blocks) == 0 {
		return nil, ErrContractViolation
	}
	return blocks, nil
}

// splitPathAndLang splits a declared contract token "lang:path" into its
// language and path halves. Tokens without a colon are a bare path with no
// language tag. Windows-style drive letters ("C:\...") are preserved as a
// bare path because a single leading letter plus colon is not a language tag.
func splitPathAndLang(token string) (path, lang string) {
	idx := strings.Index(token, ":")
	if idx <= 0 || idx == len(token)-1 {
		return token, ""
	}
	if idx == 1 && len(token) > 2 && token[2] == '\\' {
		return token, ""
	}
	// ```lang:path form already split by fenceColonRe; only used for
	// :::artifact lang:path.
	return token[idx+1:], token[:idx]
}

// Extract implements the evidence-based Extractor contract. A successful
// parse yields one ir.Artifact per contract with the standard structural
// evidence set; a contract violation yields a rejected result with no
// artifacts so callers can drive a reprompt from the evidence set.
func (p *ArtifactContractParser) Extract(ctx context.Context, raw string) ExtractionResult {
	if !utf8.ValidString(raw) {
		return ExtractionResult{Raw: raw}
	}
	evidences := make([]EvidenceFlag, 0, 4)
	evidences = append(evidences, EvValidUTF8)
	blocks, err := p.Parse(ctx, raw)
	if err != nil {
		return ExtractionResult{Evidences: evidences, Raw: raw}
	}
	evidences = append(evidences, EvValidFenceHeader, EvPathInHeader, EvFenceClosed)
	artifacts := make([]ir.Artifact, 0, len(blocks))
	for _, b := range blocks {
		a := newFileArtifact(b.Path, b.Language, b.Lines)
		a.Metadata.Source = "contract-parser"
		artifacts = append(artifacts, a)
	}
	return ExtractionResult{Artifacts: artifacts, Evidences: evidences, Raw: raw}
}

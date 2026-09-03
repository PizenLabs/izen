// Package rmah implements the Robust Mutation Artifact Handler (RMAH) pipeline.
//
// RMAH handles unstructured LLM outputs — particularly from lightweight or
// free-tier models (e.g., dots-studio/dots-3-note-preview:free) that return
// raw code fences instead of strict SEARCH/REPLACE blocks. Without RMAH, such
// outputs cause immediate execution rejection and early hard-block.
//
// The pipeline has three tiers:
//
//	Tier 1 (Strict Schema Parser): validates input against strict SEARCH/REPLACE
//	blocks or unified diff contracts. If valid, emits a mutation candidate.
//
//	Tier 2 (Conservative Code Extractor): triggered ONLY when Tier 1 fails.
//	Extracts raw code content from fenced blocks, then performs AST baseline
//	verification. Fail-closed: a candidate that degrades a clean baseline to
//	corrupt is REJECTED immediately.
//
//	Tier 3 (Myers Diff Synthesizer): compares unstructured raw output with the
//	baseline and emits context-padded SEARCH/REPLACE blocks.
//
// Both tiers feed into the pre-existing safety barriers (artifact boundary,
// verifier). RMAH never bypasses those barriers — it only expands the set of
// outputs that can reach them.
package rmah

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Sentinel errors returned by the RMAH pipeline.
var (
	// ErrSchemaViolation is returned by Tier 1 when the raw output does not
	// match any strict schema (SEARCH/REPLACE blocks or unified diff).
	ErrSchemaViolation = errors.New("rmah: tier 1 schema violation")

	// ErrASTDegradation is returned by Tier 2 when the extracted candidate
	// degrades a clean baseline file into a corrupt AST structure. The
	// candidate is REJECTED — never passed to safety barriers.
	ErrASTDegradation = errors.New("rmah: candidate degrades baseline AST")

	// ErrDestructiveTruncation is returned by Tier 2 when a whole-file
	// extraction candidate silently truncates a structurally sound baseline
	// file (retains less than the retention floor of the baseline byte size).
	// The candidate is REJECTED — it would destroy the majority of the target
	// content even though it passes AST syntax validation.
	ErrDestructiveTruncation = errors.New("RMAH Tier 2: candidate rejected due to potential destructive truncation (retains < 60% of target content)")

	// ErrNoExtractableContent is returned by Tier 2 when no raw code content
	// could be extracted from fenced blocks (e.g., prose-only output).
	ErrNoExtractableContent = errors.New("rmah: no extractable code content")
)

// TierResult is the outcome of one RMAH tier.
type TierResult struct {
	// Candidate is the extracted mutation content (may be empty if rejected).
	Candidate string
	// Passed reports whether the tier produced a valid candidate.
	Passed bool
	// Rejected reports whether the tier explicitly rejected the candidate
	// (AST degradation, no extractable content). A rejected candidate must
	// never proceed to safety barriers.
	Rejected bool
	// RejectReason explains why the candidate was rejected (empty when Passed).
	RejectReason string
}

// StrictExtractor is the Tier 1 function signature: it validates rawLLMOutput
// against strict SEARCH/REPLACE or unified diff contracts and returns a
// candidate when the schema matches.
type StrictExtractor func(rawLLMOutput, original string) (candidate string, ok bool)

// CodeExtractor is the Tier 2 function signature: it extracts raw code
// content from fenced blocks in the LLM output.
type CodeExtractor func(rawLLMOutput string) (content string, ok bool)

// BaselineVerifier reports whether content for the given target file has a
// valid, clean parseable structure. It is the AST baseline check used by
// Tier 2 to detect degradation.
type BaselineVerifier func(content, target string) bool

// Pipeline is the RMAH Tier 1 / Tier 2 pipeline. It is stateless and safe for
// concurrent use. The extraction and verification functions are injected so
// the pipeline has no dependency on the execution package (avoiding import
// cycles) while reusing the same bounded-patch extraction, code-block
// extraction, and V3 validation logic.
type Pipeline struct {
	// maxCandidateBytes bounds the maximum candidate size. Candidates exceeding
	// this limit are rejected (fail-closed).
	maxCandidateBytes int
	// extractStrict is the Tier 1 strict schema extractor.
	extractStrict StrictExtractor
	// extractCode is the Tier 2 code-block extractor.
	extractCode CodeExtractor
	// verifyBaseline is the AST baseline verifier.
	verifyBaseline BaselineVerifier
}

// NewPipeline returns a Pipeline with the default bounds and nil function
// pointers. Callers MUST set the extraction and verification functions via
// the With* methods before calling Process, or use NewConfiguredPipeline.
func NewPipeline() *Pipeline {
	return &Pipeline{
		maxCandidateBytes: defaultMaxCandidateBytes,
	}
}

// NewConfiguredPipeline returns a Pipeline with the given bounds and all
// required function dependencies wired. It is the constructor used by the
// execution package to create a fully functional pipeline.
func NewConfiguredPipeline(maxBytes int, strict StrictExtractor, code CodeExtractor, verify BaselineVerifier) *Pipeline {
	return &Pipeline{
		maxCandidateBytes: maxBytes,
		extractStrict:     strict,
		extractCode:       code,
		verifyBaseline:    verify,
	}
}

// WithStrictExtractor sets the Tier 1 strict schema extractor.
func (p *Pipeline) WithStrictExtractor(fn StrictExtractor) *Pipeline {
	p.extractStrict = fn
	return p
}

// WithCodeExtractor sets the Tier 2 code-block extractor.
func (p *Pipeline) WithCodeExtractor(fn CodeExtractor) *Pipeline {
	p.extractCode = fn
	return p
}

// WithBaselineVerifier sets the AST baseline verifier.
func (p *Pipeline) WithBaselineVerifier(fn BaselineVerifier) *Pipeline {
	p.verifyBaseline = fn
	return p
}

// NewPipelineWithLimit returns a Pipeline with a custom byte limit.
func NewPipelineWithLimit(maxBytes int) *Pipeline {
	return &Pipeline{
		maxCandidateBytes: maxBytes,
	}
}

// defaultMaxCandidateBytes bounds the largest candidate the Tier 2 extractor
// will consider. It mirrors MaxFullContentRewriteBytes so a free-tier model
// cannot flood the safety barriers with a multi-megabyte payload.
const defaultMaxCandidateBytes = 50 * 1024

// retentionFloorRatio is the minimum fraction of the baseline byte size a
// whole-file Tier 2 candidate must retain. A candidate below this floor
// silently truncates a structurally sound baseline (an LLM emitting a partial
// snippet instead of a full document rewrite) and is rejected even though it
// passes AST syntax validation.
const retentionFloorRatio = 0.60

// Process runs the full RMAH pipeline over rawLLMOutput for the given target
// file. original is the current content of the target file (the baseline).
//
// Pipeline flow — fail-closed at every stage:
//
//  1. Tier 1 (Strict Schema Parser): attempt SEARCH/REPLACE or unified diff
//     extraction. If successful, return the candidate immediately.
//
//  2. Tier 2 (Conservative Code Extractor): triggered ONLY when Tier 1 fails.
//     a. Extract raw code content from fenced blocks.
//     b. If baseline was clean and candidate degrades to corrupt: REJECT.
//     c. If candidate produces a valid AST or resolves corruption: pass to
//     pre-existing safety barriers via the returned TierResult.
//
//  3. If both tiers fail, return a rejected TierResult with the reason.
func (p *Pipeline) Process(rawLLMOutput, target, original string) TierResult {
	// ── Tier 1: Strict Schema Parser ─────────────────────────────────────
	if p.extractStrict != nil {
		if candidate, ok := p.extractStrict(rawLLMOutput, original); ok {
			return TierResult{Candidate: candidate, Passed: true}
		}
	}

	// ── Tier 2: Conservative Code Extractor (fallback) ──────────────────
	tier2 := p.tier2ConservativeExtract(rawLLMOutput, target, original)
	if tier2.Passed || !tier2.Rejected || tier2.RejectReason != ErrNoExtractableContent.Error() {
		return tier2
	}

	// ── Tier 3: in-memory Myers synthesis ───────────────────────────────
	return p.tier3Synthesize(rawLLMOutput, target, original)
}

// ProcessArtifact is the explicit Tier 1 -> Tier 2 -> Tier 3 entry point.
func (p *Pipeline) ProcessArtifact(rawLLMOutput, target, baseline string) TierResult {
	return p.Process(rawLLMOutput, target, baseline)
}

func (p *Pipeline) tier3Synthesize(raw, target, baseline string) TierResult {
	// Without the structural gate there is no safe way to distinguish raw code
	// from prose. Tier 3 is therefore fail-closed when no verifier is wired.
	if p.verifyBaseline == nil || !tier3ASTTarget(target) || strings.TrimSpace(raw) == "" || baseline == "" {
		return TierResult{Rejected: true, RejectReason: ErrNoExtractableContent.Error()}
	}
	if p.verifyBaseline(baseline, target) {
		ratio := float64(len(strings.TrimSpace(raw))) / float64(len(baseline))
		if ratio < retentionFloorRatio {
			return TierResult{Rejected: true, RejectReason: ErrTier3DestructiveTruncation.Error()}
		}
	}
	patch, err := synthesizeDiffPatch(baseline, strings.TrimSpace(raw))
	if err != nil {
		return TierResult{Rejected: true, RejectReason: err.Error()}
	}
	// Apply the generated blocks in-memory and verify the actual resulting file,
	// rather than validating an isolated REPLACE payload.
	result, ok := applySynthesizedPatch(baseline, patch)
	if !ok {
		return TierResult{Rejected: true, RejectReason: "rmah tier 3: synthesized patch rejected due to ambiguous anchors"}
	}
	if p.verifyBaseline(baseline, target) && !p.verifyBaseline(result, target) {
		return TierResult{Rejected: true, RejectReason: ErrASTDegradation.Error()}
	}
	return TierResult{Candidate: patch, Passed: true}
}

func tier3ASTTarget(target string) bool {
	switch strings.ToLower(filepath.Ext(target)) {
	case ".go", ".html", ".htm", ".xhtml", ".json":
		return true
	default:
		return false
	}
}

// tier2ConservativeExtract is the Tier 2 fallback. It extracts raw code
// content from fenced blocks and performs AST baseline verification.
//
// Fail-closed rules:
//   - No extractable content → reject with ErrNoExtractableContent.
//   - Candidate exceeds maxCandidateBytes → reject (token bounds).
//   - Baseline was clean and candidate degrades to corrupt → reject with
//     ErrASTDegradation.
//   - Baseline was clean and candidate retains < 60% of the baseline byte
//     size → reject with ErrDestructiveTruncation (Content Retention Floor).
//   - Candidate produces a valid AST or resolves corruption → pass to safety
//     barriers.
func (p *Pipeline) tier2ConservativeExtract(rawLLMOutput, target, original string) TierResult {
	if p.extractCode == nil {
		return TierResult{
			Rejected:     true,
			RejectReason: ErrNoExtractableContent.Error(),
		}
	}
	// Extract raw code content from fenced blocks.
	candidate, ok := p.extractCode(rawLLMOutput)
	if !ok || strings.TrimSpace(candidate) == "" {
		return TierResult{
			Rejected:     true,
			RejectReason: ErrNoExtractableContent.Error(),
		}
	}

	// Token bounds check: fail-closed on oversized candidates.
	if len(candidate) > p.maxCandidateBytes {
		return TierResult{
			Rejected:     true,
			RejectReason: fmt.Sprintf("candidate exceeds max size (%d > %d bytes)", len(candidate), p.maxCandidateBytes),
		}
	}

	// AST Baseline Verification: if the baseline target was clean (parseable)
	// and the candidate degrades it to corrupt, REJECT immediately.
	if p.verifyBaseline != nil && original != "" && p.verifyBaseline(original, target) {
		if !p.verifyBaseline(candidate, target) {
			return TierResult{
				Rejected:     true,
				RejectReason: fmt.Sprintf("%v: baseline %q is parseable but candidate is not", ErrASTDegradation, target),
			}
		}

		// CONTENT RETENTION FLOOR RATIO: a whole-file extraction candidate for
		// a structurally sound baseline must retain at least 60% of the
		// baseline byte size. An LLM that emits a partial snippet (e.g., 20
		// lines) instead of a full document rewrite passes AST syntax
		// validation while silently truncating the majority of the target.
		// Fail-closed: the candidate is rejected.
		baselineBytes := len(original)
		if baselineBytes > 0 {
			ratio := float64(len(candidate)) / float64(baselineBytes)
			if ratio < retentionFloorRatio {
				return TierResult{
					Rejected:     true,
					RejectReason: fmt.Sprintf("%v: target %q", ErrDestructiveTruncation, target),
				}
			}
		}
	}

	// Candidate passed AST verification — pass to pre-existing safety
	// barriers. The caller is responsible for routing the candidate through
	// the artifact boundary and verifier.
	return TierResult{Candidate: candidate, Passed: true}
}

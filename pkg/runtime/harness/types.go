// Package harness implements the Resilient Model Adaptation Harness (RMAH).
//
// RMAH is strictly a Model-Output Translation Layer with ZERO execution
// authority. It translates raw LLM output into validated CandidateArtifacts
// and structured ArtifactEvidence. It never mutates the filesystem and never
// decides whether a mutation may run; that decision belongs exclusively to
// the RuntimeExecutor gated by the structural / scope / authorization
// pipeline in package gate.
package harness

// ExtractionTier classifies how a candidate was recovered from raw model
// output. Higher tiers imply more inference and therefore less authority.
type ExtractionTier int

const (
	// Tier1Strict means the output parsed cleanly into a structured diff or
	// artifact contract with exact match (ExactParse = true, Confidence = 1.0).
	Tier1Strict ExtractionTier = iota + 1
	// Tier2Evidence means the output required multi-factor anchor matching
	// against the original file content. Ambiguity is a first-class rejection.
	Tier2Evidence
	// Tier3Inference means the output was truncated or unstructured and was
	// reconstructed heuristically. Tier 3 may PROPOSE but never AUTHORIZE.
	Tier3Inference
)

// SourceRange is a half-open-ish, inclusive coordinate span [Start, End]
// within a source file. Line and column are 1-based.
type SourceRange struct {
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
	StartCol  int `json:"start_col"`
	EndCol    int `json:"end_col"`
}

// ArtifactEvidence captures how a CandidateArtifact was derived and how much
// trust may be placed in it. This is the single input to the gate pipeline's
// AuthorizationBoundary decision function.
type ArtifactEvidence struct {
	// Tier is the extraction tier (1, 2, or 3).
	Tier ExtractionTier `json:"tier"`
	// SourceRange is the span within the original file that the candidate maps
	// to, when applicable (Tier 2/3).
	SourceRange SourceRange `json:"source_range"`
	// MatchScore is the raw match quality in [0, 1] (Tier 2 anchor matching).
	MatchScore float64 `json:"match_score"`
	// Confidence is the overall confidence in [0, 1].
	Confidence float64 `json:"confidence"`
	// AnchorCount is the number of independent anchors used to locate the match.
	AnchorCount int `json:"anchor_count"`
	// ExactParse reports a clean structured parse (Tier 1).
	ExactParse bool `json:"exact_parse"`
	// FuzzyMatch reports anchor-based fuzzy recovery (Tier 2).
	FuzzyMatch bool `json:"fuzzy_match"`
	// Inferred reports heuristic reconstruction (Tier 3). Inferred candidates
	// may PROPOSE but never AUTOMATICALLY EXECUTE.
	Inferred bool `json:"inferred"`
	// Ambiguous reports that Tier 2 found multiple plausible matches of
	// comparable score. Ambiguous evidence MUST be rejected immediately.
	Ambiguous bool `json:"ambiguous"`
	// Truncated reports that the raw output appeared truncated.
	Truncated bool `json:"truncated"`
}

// CandidateArtifact is the translation-layer output: a proposed mutation
// together with the evidence that justifies it. It carries no authority.
type CandidateArtifact struct {
	// TargetFile is the resolved canonical path of the file to mutate.
	TargetFile string `json:"target_file"`
	// RawPatch is the raw byte payload parsed from model output.
	RawPatch []byte `json:"raw_patch"`
	// Diff is the normalized unified diff string, when derivable.
	Diff string `json:"diff"`
	// Evidence describes how this candidate was recovered.
	Evidence ArtifactEvidence `json:"evidence"`
}

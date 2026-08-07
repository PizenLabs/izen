// Package extractor implements the evidence-based extraction pipeline of the
// Izen Agent Runtime V3. Extractors turn raw LLM output into canonical
// ir.Artifact values, and every acceptance decision is made deterministically
// from a set of EvidenceFlags — never from a floating confidence number.
package extractor

import (
	"github.com/PizenLabs/izen/pkg/ir"
)

// EvidenceFlag is a deterministic, machine-readable fact about raw LLM output
// observed during extraction. A flag is either observed or it is not; flags
// carry no weights or probabilities.
type EvidenceFlag string

const (
	// EvValidFenceHeader is set when the raw output opened a well-formed
	// fence/header construct (e.g. a ```lang:path header or a === FILE: path
	// marker) that names an artifact.
	EvValidFenceHeader EvidenceFlag = "valid_fence_header"
	// EvPathInHeader is set when every produced artifact carries a non-empty
	// path declared in its header.
	EvPathInHeader EvidenceFlag = "path_in_header"
	// EvFenceClosed is set when every opened fence/block was closed in the
	// raw output (no dangling ``` opener).
	EvFenceClosed EvidenceFlag = "fence_closed"
	// EvValidUTF8 is set when the raw input decoded as valid UTF-8.
	EvValidUTF8 EvidenceFlag = "valid_utf8"
	// EvValidJSONSchema is set when a structured JSON extraction validated
	// its envelope against the expected schema. Only the JSONExtractor emits
	// this flag, and only on a successful validation.
	EvValidJSONSchema EvidenceFlag = "valid_json_schema"
)

// String returns the machine-readable flag label.
func (e EvidenceFlag) String() string { return string(e) }

// ExtractionDecision is the deterministic verdict produced by evaluating an
// ExtractionResult's evidence set.
type ExtractionDecision int

const (
	// DecisionRejectAndRetry is returned when the evidence does not prove a
	// complete, well-formed extraction. The caller should request a retry
	// from the model.
	DecisionRejectAndRetry ExtractionDecision = iota
	// DecisionAccept is returned when the evidence proves a complete,
	// well-formed extraction yielding at least one artifact.
	DecisionAccept
)

// String returns a human-readable decision label.
func (d ExtractionDecision) String() string {
	switch d {
	case DecisionAccept:
		return "accept"
	default:
		return "reject_and_retry"
	}
}

// ExtractionResult is the immutable output of one extraction attempt. It
// carries the produced artifacts, the set of EvidenceFlags observed, and the
// raw input — enough to audit or replay the decision.
type ExtractionResult struct {
	// Artifacts are the canonical ir.Artifact values produced, in order.
	Artifacts []ir.Artifact
	// Evidences is the set of EvidenceFlags observed during extraction.
	Evidences []EvidenceFlag
	// Raw is the original input passed to the extractor.
	Raw string
}

// HasEvidence reports whether flag is present in the evidence set.
func (r ExtractionResult) HasEvidence(flag EvidenceFlag) bool {
	for _, e := range r.Evidences {
		if e == flag {
			return true
		}
	}
	return false
}

// EvidenceSet returns the observed evidence flags deduplicated.
func (r ExtractionResult) EvidenceSet() []EvidenceFlag {
	seen := make(map[EvidenceFlag]struct{}, len(r.Evidences))
	out := make([]EvidenceFlag, 0, len(r.Evidences))
	for _, e := range r.Evidences {
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	return out
}

// Evaluate applies the deterministic acceptance contract and returns the
// extraction decision. A result is accepted if and only if:
//
//  1. every structural evidence flag is present — EvValidFenceHeader,
//     EvPathInHeader, EvFenceClosed and EvValidUTF8; and
//  2. at least one artifact was produced.
//
// Schema-validated extractions are accepted through the same contract: the
// JSON extractor emits EvValidJSONSchema and emits artifacts only when its
// schema validation passed, so a schema-invalid extraction surfaces as either
// a missing flag or an empty artifact set. The decision is a pure function of
// the evidence set — it never consults a confidence number.
func (r ExtractionResult) Evaluate() ExtractionDecision {
	for _, f := range []EvidenceFlag{EvValidFenceHeader, EvPathInHeader, EvFenceClosed, EvValidUTF8} {
		if !r.HasEvidence(f) {
			return DecisionRejectAndRetry
		}
	}
	if len(r.Artifacts) == 0 {
		return DecisionRejectAndRetry
	}
	return DecisionAccept
}

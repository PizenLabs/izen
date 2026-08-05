// Package inference implements the multi-hypothesis inference stage of the
// IR-driven intent compiler. The InferenceEngine aggregates signals from
// WorkspaceFacts and PromptSlots into an InferenceSet of ranked, weighted
// hypotheses for four decision dimensions — Framework, Language, Styling and
// Router. Every hypothesis carries traceable EvidenceTrace records, so the
// /explain-decision inspector can show exactly why a stack was chosen.
//
// Inference is a pure read of its inputs: the same facts and prompt always
// yield the same InferenceSet. Policy evaluation (Proceed / Fallback /
// EscalateToHuman) is separated into the PolicyEngine so the decision and
// the evidence stay auditable and independent.
package inference

import (
	"sort"
)

// InferenceType names one decision dimension of an InferenceSet.
type InferenceType string

// Decision dimensions the engine reasons over.
const (
	// TypeFramework is the web framework hypothesis (Next.js, Astro, ...).
	TypeFramework InferenceType = "framework"
	// TypeLanguage is the implementation language hypothesis.
	TypeLanguage InferenceType = "language"
	// TypeStyling is the styling approach hypothesis.
	TypeStyling InferenceType = "styling"
	// TypeRouter is the routing approach hypothesis.
	TypeRouter InferenceType = "router"
)

// String returns the machine-readable dimension label.
func (t InferenceType) String() string { return string(t) }

// allTypes lists the decision dimensions in canonical display order.
var allTypes = []InferenceType{TypeFramework, TypeLanguage, TypeStyling, TypeRouter}

// Inference is the resolved decision for one dimension: the winning label,
// its confidence, the traceable evidence that produced it, and the ranked
// alternative hypotheses.
type Inference struct {
	// Type is the decision dimension.
	Type InferenceType `json:"type"`
	// Label is the winning hypothesis label.
	Label string `json:"label"`
	// Confidence is the winning hypothesis confidence on [0,1].
	Confidence float64 `json:"confidence"`
	// Evidence traces the winning hypothesis back to its signals.
	Evidence []EvidenceTrace `json:"evidence"`
	// Alternatives lists every ranked hypothesis for this dimension.
	Alternatives []Hypothesis `json:"alternatives"`
}

// Hypothesis is the exported, ranked view of one candidate. It is the same
// data the inspector renders and the PolicyEngine evaluates.
type Hypothesis struct {
	// Label is the candidate name.
	Label string `json:"label"`
	// Evidence lists the supporting, traceable signals.
	Evidence []EvidenceTrace `json:"evidence"`
}

// Score returns the raw sum of the supporting evidence weights.
func (h Hypothesis) Score() float64 {
	var s float64
	for _, t := range h.Evidence {
		s += t.Weight
	}
	return s
}

// Confidence returns the score clamped to [0,1].
func (h Hypothesis) Confidence() float64 {
	s := h.Score()
	if s > 1 {
		return 1
	}
	if s < 0 {
		return 0
	}
	return s
}

// InferenceSet is the immutable result of one inference pass. It holds the
// ranked hypotheses for every decision dimension. Accessors return defensive
// copies; the engine returns a fresh set per Infer call.
type InferenceSet struct {
	dimensions map[InferenceType][]hypothesis
}

// NewInferenceSet returns an empty inference set.
func NewInferenceSet() *InferenceSet {
	return &InferenceSet{dimensions: map[InferenceType][]hypothesis{}}
}

// set stores the ranked hypotheses for one dimension.
func (s *InferenceSet) set(t InferenceType, hyps []hypothesis) {
	s.dimensions[t] = hyps
}

// Hypotheses returns the ranked hypotheses for one dimension, best first.
func (s *InferenceSet) Hypotheses(t InferenceType) []Hypothesis {
	hyps := s.dimensions[t]
	out := make([]Hypothesis, 0, len(hyps))
	for _, h := range hyps {
		out = append(out, Hypothesis{Label: h.label, Evidence: h.Evidence()})
	}
	return out
}

// Top returns the highest-ranked hypothesis for one dimension, if any.
func (s *InferenceSet) Top(t InferenceType) (Hypothesis, bool) {
	hyps := s.Hypotheses(t)
	if len(hyps) == 0 {
		return Hypothesis{}, false
	}
	return hyps[0], true
}

// Inference resolves the decision for one dimension. When no hypothesis was
// emitted, Label is empty and Confidence is 0.
func (s *InferenceSet) Inference(t InferenceType) Inference {
	hyps := s.Hypotheses(t)
	if len(hyps) == 0 {
		return Inference{Type: t}
	}
	top := hyps[0]
	return Inference{
		Type:         t,
		Label:        top.Label,
		Confidence:   top.Confidence(),
		Evidence:     top.Evidence,
		Alternatives: hyps,
	}
}

// ResolvedLabel returns the winning label for a dimension, or "" when the
// dimension is unresolved.
func (s *InferenceSet) ResolvedLabel(t InferenceType) string {
	return s.Inference(t).Label
}

// Framework returns the resolved framework inference.
func (s *InferenceSet) Framework() Inference { return s.Inference(TypeFramework) }

// Language returns the resolved language inference.
func (s *InferenceSet) Language() Inference { return s.Inference(TypeLanguage) }

// Styling returns the resolved styling inference.
func (s *InferenceSet) Styling() Inference { return s.Inference(TypeStyling) }

// Router returns the resolved router inference.
func (s *InferenceSet) Router() Inference { return s.Inference(TypeRouter) }

// ResolvedFramework returns the winning framework label, or "".
func (s *InferenceSet) ResolvedFramework() string { return s.ResolvedLabel(TypeFramework) }

// ResolvedLanguage returns the winning language label, or "".
func (s *InferenceSet) ResolvedLanguage() string { return s.ResolvedLabel(TypeLanguage) }

// ResolvedStyling returns the winning styling label, or "".
func (s *InferenceSet) ResolvedStyling() string { return s.ResolvedLabel(TypeStyling) }

// ResolvedRouter returns the winning router label, or "".
func (s *InferenceSet) ResolvedRouter() string { return s.ResolvedLabel(TypeRouter) }

// Types returns the decision dimensions present in the set in canonical
// display order.
func (s *InferenceSet) Types() []InferenceType {
	return append([]InferenceType(nil), allTypes...)
}

// AllEvidence returns every evidence trace across all dimensions, sorted by
// signal key. It is the flat view the inspector renders.
func (s *InferenceSet) AllEvidence() []EvidenceTrace {
	total := 0
	for _, t := range allTypes {
		for _, h := range s.dimensions[t] {
			total += len(h.evidence)
		}
	}
	out := make([]EvidenceTrace, 0, total)
	for _, t := range allTypes {
		out = append(out, s.dimensionEvidence(t)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Key() < out[j].Key()
	})
	return out
}

// dimensionEvidence flattens one dimension's winning + alternative traces.
func (s *InferenceSet) dimensionEvidence(t InferenceType) []EvidenceTrace {
	hyps := s.dimensions[t]
	total := 0
	for _, h := range hyps {
		total += len(h.evidence)
	}
	out := make([]EvidenceTrace, 0, total)
	for _, h := range hyps {
		out = append(out, h.Evidence()...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Key() < out[j].Key()
	})
	return out
}

// ─── InferenceEngine ────────────────────────────────────────────────────────

// InferenceEngine aggregates signals from WorkspaceFacts and PromptSlots into
// a ranked InferenceSet. It is a pure, deterministic stage: no model calls,
// no random choices, no state.
type InferenceEngine struct{}

// NewInferenceEngine returns a fresh inference engine.
func NewInferenceEngine() *InferenceEngine { return &InferenceEngine{} }

// Infer runs all detectors over the facts and prompt slots and assembles the
// ranked InferenceSet. The facts and slots are never mutated.
func (e *InferenceEngine) Infer(facts WorkspaceFacts, slots PromptSlots) *InferenceSet {
	set := NewInferenceSet()
	set.set(TypeFramework, detectFramework(facts, slots))
	set.set(TypeLanguage, detectLanguage(facts, slots))
	set.set(TypeStyling, detectStyling(facts, slots))
	set.set(TypeRouter, detectRouter(facts, slots))
	return set
}

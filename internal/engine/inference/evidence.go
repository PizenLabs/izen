package inference

// EvidenceSource classifies where an evidence trace originated. Every trace
// carries its source so the evidence inspector can show the user WHERE a
// signal came from (config file, dependency manifest, prompt, workspace).
type EvidenceSource string

// Evidence sources recognised by the inference engine.
const (
	// SourceConfig is a recognized config file present in the workspace
	// (e.g. next.config.ts).
	SourceConfig EvidenceSource = "config"
	// SourceDependency is a dependency declared in a manifest (package.json,
	// go.mod).
	SourceDependency EvidenceSource = "dependency"
	// SourcePrompt is a technology keyword mentioned in the user prompt.
	SourcePrompt EvidenceSource = "prompt"
	// SourceWorkspace is a structural workspace signal (a directory, a
	// recognized source file).
	SourceWorkspace EvidenceSource = "workspace"
)

// String returns the machine-readable source label.
func (s EvidenceSource) String() string { return string(s) }

// EvidenceTrace is one auditable fact supporting a hypothesis. It records
// the source, the exact signal id (e.g. "config:next.config.ts"), the
// weight it contributes to the hypothesis score, and a human-readable
// justification. Evidence is immutable after construction.
type EvidenceTrace struct {
	// Source is where the signal came from.
	Source EvidenceSource `json:"source"`
	// ID is the exact signal identifier, e.g. "config:next.config.ts" or
	// "dependency:next".
	ID string `json:"id"`
	// Weight is the contribution this trace adds to the hypothesis score.
	Weight float64 `json:"weight"`
	// Reason is the human-readable justification for the trace.
	Reason string `json:"reason"`
}

// Key returns the canonical signal key ("<source>:<id>"), the same string
// shown by the evidence inspector.
func (t EvidenceTrace) Key() string {
	return string(t.Source) + ":" + t.ID
}

// hypothesis is a ranked candidate for one decision dimension. Score and
// confidence are derived from the evidence weights; a hypothesis is only
// ever emitted with at least one evidence trace.
type hypothesis struct {
	// label is the candidate name, e.g. "Next.js".
	label string
	// evidence lists the auditable traces supporting the candidate.
	evidence []EvidenceTrace
}

// Label returns the candidate name.
func (h hypothesis) Label() string { return h.label }

// Evidence returns a defensive copy of the supporting traces in signal order.
func (h hypothesis) Evidence() []EvidenceTrace {
	return append([]EvidenceTrace(nil), h.evidence...)
}

// EvidenceCount returns the number of supporting traces.
func (h hypothesis) EvidenceCount() int { return len(h.evidence) }

// Score is the raw sum of the supporting evidence weights. For the canonical
// Next.js workspace it is exactly 0.30 (config:next.config.ts) + 0.60
// (dependency:next) = 0.90.
func (h hypothesis) Score() float64 {
	var s float64
	for _, t := range h.evidence {
		s += t.Weight
	}
	return s
}

// Confidence is the score clamped to the [0,1] interval. It is the value the
// PolicyEngine compares across hypotheses.
func (h hypothesis) Confidence() float64 {
	s := h.Score()
	if s > 1 {
		return 1
	}
	if s < 0 {
		return 0
	}
	return s
}

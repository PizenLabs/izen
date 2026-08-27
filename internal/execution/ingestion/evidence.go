package ingestion

import "time"

// IngestionTrace is the immutable forensic record of one transport
// normalization pass over a raw LLM response. It preserves the EXACT,
// unmutated model output alongside the normalized payload, and records every
// transport transformation as an ordered NormalizationStep so a post-mortem can
// replay precisely how the artifact crossed the transport boundary.
type IngestionTrace struct {
	// RawOutput is the exact, unmutated LLM response — fences, whitespace,
	// line endings and all. It is never altered by normalization.
	RawOutput string `json:"raw_output"`
	// NormalizedPayload is the transport-normalized artifact payload handed to
	// the L1 Execution Gate. It carries NO semantic repair.
	NormalizedPayload string `json:"normalized_payload"`
	// Classification records the envelope-integrity disposition of the
	// normalized payload.
	Classification PayloadClass `json:"classification"`
	// Steps records every transport transformation in order. Empty when the raw
	// output already satisfied the transport contract.
	Steps []NormalizationStep `json:"steps"`
	// Timestamp is when the ingestion pass ran.
	Timestamp time.Time `json:"timestamp"`
}

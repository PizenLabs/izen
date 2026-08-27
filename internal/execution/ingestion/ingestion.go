package ingestion

import (
	"errors"
	"fmt"
	"time"
)

// ErrSyntaxInvalid is returned by Process when the normalized payload fails
// basic envelope integrity. It signals the executor to reject the generation to
// the contract retry loop with an explicit syntax/parse failure — never to
// attempt a silent semantic repair.
var ErrSyntaxInvalid = errors.New("ingestion: normalized payload failed envelope integrity")

// Process is the primary ingestion entry point. It normalizes the raw LLM
// output through the transport-only pipeline, classifies the residual payload,
// and returns a complete IngestionTrace. When the payload is syntactically
// invalid the returned trace is still populated (RawOutput and NormalizedPayload
// preserved for forensics) and the error is ErrSyntaxInvalid.
func Process(rawOutput string) (*IngestionTrace, error) {
	normalized, steps := NormalizeTransport(rawOutput)
	cls := Classify(normalized, steps)
	trace := &IngestionTrace{
		RawOutput:         rawOutput,
		NormalizedPayload: normalized,
		Classification:    cls,
		Steps:             steps,
		Timestamp:         time.Now(),
	}
	if cls == ClassSyntaxInvalid {
		return trace, fmt.Errorf("%w: transport normalization produced a syntactically invalid payload", ErrSyntaxInvalid)
	}
	return trace, nil
}

package ingestion

import (
	"errors"
	"fmt"
	"strings"
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
//
// For ClassSyntaxInvalid payloads the engine MUST NEVER perform silent
// semantic fixes — instead the heuristics may emit a RepairCandidate for
// explicit L1 verification by the executor. Process therefore attaches a
// RepairCandidate when a heuristic applies (ProposeRepair) and records the
// repair_candidate_generated metric, but STILL returns ErrSyntaxInvalid.
// The executor decides whether to accept the candidate after AST validation
// and safety-threshold checks.
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
		if candidate := ProposeRepair(normalized); candidate != nil {
			trace.RepairCandidate = candidate
			RecordRepairGenerated()
		}
		// TRANSPORT FALLBACK (last resort): if the envelope failure is caused by
		// RESIDUAL TRANSPORT ARTIFACTS — an unterminated fence or stray closer
		// that NormalizeTransport could not close — extract the raw response
		// content and re-classify before declaring the payload syntactically
		// invalid. Genuine semantic envelope failures (unclosed structural tags)
		// carry no fence residue and never reach this path: they are rejected to
		// the contract retry loop exactly as before.
		if strings.Contains(normalized, "```") {
			if extracted := extractTransportContent(rawOutput); extracted != "" && extracted != normalized {
				trace.NormalizedPayload = extracted
				trace.Steps = append(trace.Steps, NormalizationStep{
					Kind:   "extract_raw_content",
					Detail: "re-extracted raw response content after residual transport markers",
				})
				if re := Classify(extracted, trace.Steps); re != ClassSyntaxInvalid {
					trace.Classification = re
					return trace, nil
				}
			}
		}
		// AGGRESSIVE RAW BLOCK RECOVERY: the envelope failure may be a
		// conversational wrapper NormalizeTransport could not close (no fence,
		// prose before a SEARCH/REPLACE block, or unbalanced patch snippets).
		// Scan the raw response directly for a standard artifact block and lift
		// it into the normalized payload BEFORE raising the transport
		// normalization error.
		if block, kind, ok := recoverArtifactBlock(rawOutput); ok {
			trace.NormalizedPayload = block
			trace.Steps = append(trace.Steps, NormalizationStep{
				Kind:   kind,
				Detail: "recovered raw artifact block from conversational wrapper",
			})
			if re := Classify(block, trace.Steps); re != ClassSyntaxInvalid {
				trace.Classification = re
				return trace, nil
			}
		}
		return trace, fmt.Errorf("%w: transport normalization produced a syntactically invalid payload", ErrSyntaxInvalid)
	}
	return trace, nil
}

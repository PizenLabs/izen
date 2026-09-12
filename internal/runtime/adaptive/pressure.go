package adaptive

import "strconv"

// PressureSignal is the closed set of deterministic runtime signals that
// may expand context. Self-reported model confidence is deliberately NOT
// a member: it can never trigger expansion.
type PressureSignal string

const (
	// SignalVerificationFailure fires when verification fails (failed
	// build, failing unit test, static-analysis error).
	SignalVerificationFailure PressureSignal = "VERIFICATION_FAILURE"
	// SignalUnresolvedSymbol fires when a symbol cannot be resolved from
	// AST indexing.
	SignalUnresolvedSymbol PressureSignal = "UNRESOLVED_SYMBOL"
	// SignalStructuralAmbiguity fires when the structural context is
	// ambiguous (multiple candidate definitions / call targets).
	SignalStructuralAmbiguity PressureSignal = "STRUCTURAL_AMBIGUITY"
	// SignalRepeatedFailedHypothesis fires when the same hypothesis fails
	// repeatedly under identical conditions.
	SignalRepeatedFailedHypothesis PressureSignal = "REPEATED_FAILED_HYPOTHESIS"
)

// Valid reports whether s is a member of the closed signal set.
func (s PressureSignal) Valid() bool {
	switch s {
	case SignalVerificationFailure, SignalUnresolvedSymbol,
		SignalStructuralAmbiguity, SignalRepeatedFailedHypothesis:
		return true
	default:
		return false
	}
}

// repeatedHypothesisThreshold is the consecutive-failure count that raises
// REPEATED_FAILED_HYPOTHESIS.
const repeatedHypothesisThreshold = 2

// PressureInput is the complete deterministic observation for one
// expansion decision. Confidence carries NO authority: it is recorded for
// audit only and never consults the decision branches below.
type PressureInput struct {
	VerificationFailed    bool
	SymbolUnresolved      bool
	StructurallyAmbiguous bool
	ConsecutiveFailures   int
	// SelfReportedConfidence is the model's own confidence score
	// (e.g. 0.2). It MUST NOT influence the outcome.
	SelfReportedConfidence float64
	// ExpandRequested is true when the worker explicitly asks for more
	// context. Without a signal above it is always rejected.
	ExpandRequested bool
}

// PressureDecision is the deterministic outcome of evaluation.
type PressureDecision struct {
	Expand bool
	Signal PressureSignal
	Reason string
}

// EvidencePressureEvaluator governs context expansion (Ln → Ln+1).
// The zero value is ready to use; evaluation is pure and goroutine-safe.
type EvidencePressureEvaluator struct{}

// Evaluate applies the expansion rule in priority order:
//
//	ExpandContext ⟺ VerificationFailure ∨ UnresolvedSymbol ∨
//	                 StructuralAmbiguity ∨ RepeatedFailedHypothesis
//
// A worker self-reporting `confidence: 0.2` without one of these signals
// yields Expand=false: the runtime MUST NOT expand context.
func (EvidencePressureEvaluator) Evaluate(in PressureInput) PressureDecision {
	switch {
	case in.VerificationFailed:
		return PressureDecision{
			Expand: true, Signal: SignalVerificationFailure,
			Reason: "verification failed; automatic tier increment for subsequent handoff",
		}
	case in.SymbolUnresolved:
		return PressureDecision{
			Expand: true, Signal: SignalUnresolvedSymbol,
			Reason: "symbol unresolvable from AST indexing; automatic tier increment for subsequent handoff",
		}
	case in.StructurallyAmbiguous:
		return PressureDecision{
			Expand: true, Signal: SignalStructuralAmbiguity,
			Reason: "structural ambiguity; automatic tier increment for subsequent handoff",
		}
	case in.ConsecutiveFailures >= repeatedHypothesisThreshold:
		return PressureDecision{
			Expand: true, Signal: SignalRepeatedFailedHypothesis,
			Reason: "repeated failed hypothesis under identical conditions; automatic tier increment for subsequent handoff",
		}
	default:
		if in.ExpandRequested {
			return PressureDecision{
				Expand: false,
				Reason: "unbacked expansion request rejected: no EvidencePressure signal (confidence " +
					trimFloat(in.SelfReportedConfidence) + " carries no authority)",
			}
		}
		return PressureDecision{
			Expand: false,
			Reason: "no evidence-pressure signal; tier unchanged",
		}
	}
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

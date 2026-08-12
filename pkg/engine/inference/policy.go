package inference

import (
	"fmt"
	"math"
)

// PolicyDecision is the outcome of evaluating one dimension of an
// InferenceSet.
type PolicyDecision string

// Policy decisions. The PolicyEngine picks exactly one per dimension.
const (
	// DecisionProceed means the top hypothesis is confident and clearly
	// separated from the runner-up: proceed with it.
	DecisionProceed PolicyDecision = "proceed"
	// DecisionFallback means the evidence is too thin to trust any
	// hypothesis: fall back to a non-inference strategy.
	DecisionFallback PolicyDecision = "fallback"
	// DecisionEscalateToHuman means the top hypotheses are close enough that
	// the engine should not choose unilaterally: ask the human.
	DecisionEscalateToHuman PolicyDecision = "escalate_to_human"
)

// PolicyVerdict is the result of evaluating one dimension. It carries the
// decision, the reason, and the compared hypotheses so the inspector can
// reproduce the choice.
type PolicyVerdict struct {
	// Dimension is the decision dimension evaluated.
	Dimension InferenceType `json:"dimension"`
	// Decision is the policy outcome.
	Decision PolicyDecision `json:"decision"`
	// Top is the highest-ranked hypothesis (zero value when none emitted).
	Top Hypothesis `json:"top"`
	// RunnerUp is the second-ranked hypothesis, or nil.
	RunnerUp *Hypothesis `json:"runner_up,omitempty"`
	// Delta is the confidence gap between Top and RunnerUp.
	Delta float64 `json:"delta"`
	// Reason is the human-readable justification of the decision.
	Reason string `json:"reason"`
}

// PolicyOption configures a PolicyEngine.
type PolicyOption func(*PolicyEngine)

// WithConfidenceThreshold sets the minimum top confidence required to
// Proceed instead of Falling back.
func WithConfidenceThreshold(t float64) PolicyOption {
	return func(p *PolicyEngine) { p.confidenceThreshold = t }
}

// WithDeltaThreshold sets the minimum confidence gap between the top two
// hypotheses required to Proceed instead of Escalating to a human.
func WithDeltaThreshold(t float64) PolicyOption {
	return func(p *PolicyEngine) { p.deltaThreshold = t }
}

// Default thresholds of the PolicyEngine.
const (
	// defaultConfidenceThreshold is the minimum top confidence to Proceed.
	defaultConfidenceThreshold = 0.45
	// defaultDeltaThreshold is the mandated separation between the top two
	// hypotheses (spec: < 0.15 → EscalateToHuman).
	defaultDeltaThreshold = 0.15
)

// PolicyEngine evaluates an InferenceSet dimension and decides whether to
// Proceed with the top hypothesis, Fall back, or Escalate to a human. It is
// pure: identical InferenceSet input yields identical verdicts.
type PolicyEngine struct {
	confidenceThreshold float64
	deltaThreshold      float64
}

// NewPolicyEngine returns a PolicyEngine with the default thresholds.
func NewPolicyEngine(opts ...PolicyOption) *PolicyEngine {
	p := &PolicyEngine{
		confidenceThreshold: defaultConfidenceThreshold,
		deltaThreshold:      defaultDeltaThreshold,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Evaluate decides one dimension of an InferenceSet:
//
//   - no hypothesis → Fallback (the engine has nothing to act on);
//   - top confidence below the threshold → Fallback;
//   - at least two hypotheses AND top confidence minus runner-up confidence
//     below the delta threshold → EscalateToHuman (two credible hypotheses
//     compete);
//   - otherwise → Proceed.
//
// A single hypothesis never escalates: with no runner-up there is no
// competition to defer to a human, so a confident lone candidate (e.g. the
// only detector firing for a Vanilla/Static Web project) always Proceeds.
func (p *PolicyEngine) Evaluate(set *InferenceSet, t InferenceType) PolicyVerdict {
	hyps := set.Hypotheses(t)
	if len(hyps) == 0 {
		return PolicyVerdict{
			Dimension: t,
			Decision:  DecisionFallback,
			Reason:    "no evidence for " + t.String() + ": no hypothesis emitted",
		}
	}
	top := hyps[0]
	var runnerUp *Hypothesis
	if len(hyps) > 1 {
		ru := hyps[1]
		runnerUp = &ru
	}
	delta := 0.0
	if runnerUp != nil {
		delta = round4(top.Confidence() - runnerUp.Confidence())
	}

	switch {
	case top.Confidence() < p.confidenceThreshold:
		return PolicyVerdict{
			Dimension: t,
			Decision:  DecisionFallback,
			Top:       top,
			RunnerUp:  runnerUp,
			Delta:     delta,
			Reason: fmt.Sprintf(
				"top %s confidence %.2f is below the %.2f threshold",
				top.Label, top.Confidence(), p.confidenceThreshold),
		}
	case runnerUp != nil && delta < p.deltaThreshold:
		// Escalation requires two competing hypotheses. A lone hypothesis
		// (runnerUp == nil, delta stays 0.00) is not a competition — never
		// defer a confident single candidate to a human.
		return PolicyVerdict{
			Dimension: t,
			Decision:  DecisionEscalateToHuman,
			Top:       top,
			RunnerUp:  runnerUp,
			Delta:     delta,
			Reason: fmt.Sprintf(
				"%s (%.2f) and %s (%.2f) are within %.2f — the engine cannot choose unilaterally",
				top.Label, top.Confidence(), runnerUp.Label, runnerUp.Confidence(), p.deltaThreshold),
		}
	default:
		return PolicyVerdict{
			Dimension: t,
			Decision:  DecisionProceed,
			Top:       top,
			RunnerUp:  runnerUp,
			Delta:     delta,
			Reason: fmt.Sprintf(
				"%s (%.2f) is confident and clearly separated from the runner-up (delta %.2f)",
				top.Label, top.Confidence(), delta),
		}
	}
}

// round4 rounds a delta to 4 decimal places so threshold comparisons are
// stable against binary floating-point noise (0.30-0.25 must be exactly
// 0.05, not 0.049999...).
func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

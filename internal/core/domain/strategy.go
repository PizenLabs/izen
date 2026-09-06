package domain

// ExecutionStrategy is the pure policy input to ContextCompiler and
// OutputAllocator. It is selected by the Control Plane from
// Objective × ExecutionEnvironment × CurrentPlan × CurrentEvidence × History.
type ExecutionStrategy struct {
	ContextPolicy      ContextPolicy      `json:"context_policy"`
	OutputPolicy       OutputPolicy       `json:"output_policy"`
	VerificationPolicy VerificationPolicy `json:"verification_policy"`
	ContinuationPolicy ContinuationPolicy `json:"continuation_policy"`
	ProgressPolicy     ProgressPolicy     `json:"progress_policy"`
}

// ContextPolicy enumerates the compiled representation (ARCH:18).
type ContextPolicy uint8

const (
	ContextNone         ContextPolicy = iota // trivial $prompt hi
	ContextFull
	ContextStructural
	ContextRegional
	ContextSymbol
	ContextDiff
	ContextDelta
	ContextSummary
	ContextVerification
	ContextContinuation
)

type OutputPolicy struct {
	MaxTokens      int  `json:"max_tokens"`
	AllowIterative bool `json:"allow_iterative"` // one-shot vs incremental — ARCH:20
}

type VerificationPolicy struct {
	RequiredLevel EvidenceLevel `json:"required_level"` // L0..L5
	MustVerify    bool          `json:"must_verify"`
}

type ContinuationPolicy uint8

const (
	ContinueOnProgress ContinuationPolicy = iota
	StopOnExhaustion
	ReplanOnStagnation
)

type ProgressPolicy struct {
	Signals []ProgressSignal `json:"signals"` // vector
}

type ProgressSignal uint8

const (
	SignalStateChanged       ProgressSignal = iota
	SignalConstraintSatisfied
	SignalEvidenceImproved
	SignalDependencyResolved
	SignalVerificationIncreased
	SignalFailureRemoved
	SignalCycleDetected
)

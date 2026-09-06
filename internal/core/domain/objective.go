package domain

import "fmt"

// Objective is the sole authoritative input to execution. It is immutable
// after IntentGateway validation.
type Objective struct {
	Intent              Intent       `json:"intent"`
	TargetScope         Scope        `json:"target_scope"`
	NegativeScope       Scope        `json:"negative_scope"`
	ConstraintChecklist []Constraint `json:"constraints"`
	RiskClass           RiskClass    `json:"risk_class"`
	EvidenceRequirement EvidenceState `json:"evidence_requirement"`
}

// Intent is the validated user intent payload.
type Intent struct {
	RawText    string     `json:"raw_text"`
	Normalized string     `json:"normalized"`
	Kind       IntentKind `json:"kind"`
	Confidence float64    `json:"confidence"`
	Locale     string     `json:"locale"`
}

// Scope is a closed set of file/symbol/directory selectors with a hash anchor.
type Scope struct {
	Includes   []ScopeSelector `json:"includes"`
	Excludes   []ScopeSelector `json:"excludes"`
	SourceHash string          `json:"source_hash"`
}

// ScopeSelector selects a file, symbol, directory, or glob pattern.
type ScopeSelector struct {
	Kind    SelectorKind `json:"kind"`
	Pattern string       `json:"pattern"`
}

// SelectorKind enumerates the kind of selector.
type SelectorKind string

const (
	SelectorFile      SelectorKind = "file"
	SelectorSymbol    SelectorKind = "symbol"
	SelectorDirectory SelectorKind = "directory"
	SelectorGlob      SelectorKind = "glob"
	SelectorUnknown   SelectorKind = "unknown"
)

// Constraint is an ordered constraint that must remain satisfied.
type Constraint struct {
	ID          string         `json:"id"`
	Description string         `json:"description"`
	Kind        ConstraintKind `json:"kind"`
	Required    bool           `json:"required"`
}

// ConstraintKind enumerates constraint categories.
type ConstraintKind string

const (
	ConstraintInvariant     ConstraintKind = "invariant"
	ConstraintStyle         ConstraintKind = "style"
	ConstraintCompatibility ConstraintKind = "compatibility"
	ConstraintPerformance   ConstraintKind = "performance"
)

// RiskClass constrains the ExecutionClass ceiling.
type RiskClass uint8

const (
	RiskTrivial RiskClass = iota
	RiskLow
	RiskMedium
	RiskHigh
)

// IntentKind enumerates user intent classifications.
type IntentKind string

const (
	IntentAsk         IntentKind = "ask"
	IntentInvestigate IntentKind = "investigate"
	IntentPlan        IntentKind = "plan"
	IntentBuild       IntentKind = "build"
	IntentReview      IntentKind = "review"
	IntentUnknown     IntentKind = "unknown"
)

// ValidateObjective enforces TargetScope ∩ NegativeScope = ∅.
// Returns an error if any TargetScope selector pattern overlaps with NegativeScope.
func ValidateObjective(o Objective) error {
	targetSet := make(map[string]struct{}, len(o.TargetScope.Includes))
	for _, s := range o.TargetScope.Includes {
		key := string(s.Kind) + ":" + s.Pattern
		targetSet[key] = struct{}{}
	}
	for _, s := range o.NegativeScope.Includes {
		key := string(s.Kind) + ":" + s.Pattern
		if _, exists := targetSet[key]; exists {
			return fmt.Errorf("objective scope violation: TargetScope ∩ NegativeScope != ∅ overlapping %q", s.Pattern)
		}
	}
	// Also check cross Includes/Excludes within scopes for direct overlap
	// NegativeScope Excludes vs TargetScope Includes etc. Simplified: check all patterns.
	negativePatterns := make(map[string]struct{})
	for _, s := range o.NegativeScope.Includes {
		negativePatterns[s.Pattern] = struct{}{}
	}
	for _, s := range o.NegativeScope.Excludes {
		negativePatterns[s.Pattern] = struct{}{}
	}
	for _, s := range o.TargetScope.Includes {
		if _, exists := negativePatterns[s.Pattern]; exists {
			return fmt.Errorf("objective scope violation: TargetScope ∩ NegativeScope != ∅ overlapping %q", s.Pattern)
		}
	}
	for _, s := range o.TargetScope.Excludes {
		if _, exists := negativePatterns[s.Pattern]; exists {
			return fmt.Errorf("objective scope violation: TargetScope overlap via excludes %q", s.Pattern)
		}
	}
	return nil
}

// NewObjective constructs an Objective with scope validation.
func NewObjective(intent Intent, targetScope, negativeScope Scope, constraints []Constraint, risk RiskClass, evidenceReq EvidenceState) (Objective, error) {
	o := Objective{
		Intent:              intent,
		TargetScope:         targetScope,
		NegativeScope:       negativeScope,
		ConstraintChecklist: constraints,
		RiskClass:           risk,
		EvidenceRequirement: evidenceReq,
	}
	if err := ValidateObjective(o); err != nil {
		return Objective{}, err
	}
	return o, nil
}

package mutationstrategy

// StrategyKind answers "what high-level approach should be used to satisfy
// this intent over this Change Surface?" It is a proposal semantic, not an
// execution directive.
type StrategyKind string

const (
	// StrategySingleStep proposes a single bounded mutation step.
	StrategySingleStep StrategyKind = "SINGLE_STEP"
	// StrategyMultiStep proposes multiple bounded steps with explicit
	// dependency ordering.
	StrategyMultiStep StrategyKind = "MULTI_STEP"
)

// String returns the canonical label.
func (s StrategyKind) String() string { return string(s) }

// Valid reports whether s is a known strategy.
func (s StrategyKind) Valid() bool {
	switch s {
	case StrategySingleStep, StrategyMultiStep:
		return true
	}
	return false
}

// PlanStatus distinguishes the planning outcome over the derived surface.
type PlanStatus string

const (
	// StatusReady means the task can be represented as bounded steps.
	StatusReady PlanStatus = "READY"
	// StatusPartial means some surface can be planned but relevant areas
	// remain uncertain.
	StatusPartial PlanStatus = "PARTIAL"
	// StatusTooLarge means the requested mutation cannot safely fit the
	// current step envelope and requires decomposition.
	StatusTooLarge PlanStatus = "TOO_LARGE"
	// StatusBlocked means a dependency prevents construction of a valid step.
	StatusBlocked PlanStatus = "BLOCKED"
	// StatusUnresolved means Project Understanding or Change Surface is
	// insufficient to construct a plan. Uncertainty is never converted
	// into fabricated planning certainty.
	StatusUnresolved PlanStatus = "UNRESOLVED"
)

// String returns the canonical label.
func (s PlanStatus) String() string { return string(s) }

// Valid reports whether s is a known status.
func (s PlanStatus) Valid() bool {
	switch s {
	case StatusReady, StatusPartial, StatusTooLarge, StatusBlocked, StatusUnresolved:
		return true
	}
	return false
}

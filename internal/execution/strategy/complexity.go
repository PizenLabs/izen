package strategy

import "fmt"

// OperationKind is the coarse operation class the engine derives from the
// request. It exists to pick the strategy family and complexity base — it is
// NOT the complexity itself. Complexity comes from measurable execution
// factors, never from the operation name alone.
type OperationKind int

const (
	// OperationContent is a localized content change in a known file.
	OperationContent OperationKind = iota
	// OperationCreate is the creation of a new file.
	OperationCreate
	// OperationFix is a bug fix requiring understanding of a failure.
	OperationFix
	// OperationRefactor is a structural or naming change.
	OperationRefactor
	// OperationDiagnose is a root-cause investigation.
	OperationDiagnose
	// OperationArchitect is a broad architectural request.
	OperationArchitect
	// OperationExplain is a read-only understanding request.
	OperationExplain
)

// String returns the canonical operation label.
func (o OperationKind) String() string {
	switch o {
	case OperationContent:
		return "content"
	case OperationCreate:
		return "create"
	case OperationFix:
		return "fix"
	case OperationRefactor:
		return "refactor"
	case OperationDiagnose:
		return "diagnose"
	case OperationArchitect:
		return "architect"
	case OperationExplain:
		return "explain"
	default:
		return "unknown"
	}
}

// baseScore is the execution-weight of each operation family. It reflects how
// many distinct decisions the operation forces the engine to make, not its
// keyword frequency.
func (o OperationKind) baseScore() int {
	switch o {
	case OperationContent, OperationCreate, OperationExplain:
		return 1
	case OperationFix, OperationRefactor:
		return 2
	case OperationDiagnose:
		return 3
	case OperationArchitect:
		return 4
	default:
		return 1
	}
}

// ComplexityInputs are the measurable execution factors. Every field is a
// deterministic property of the request or the workspace — there is no keyword
// frequency signal.
type ComplexityInputs struct {
	// Operation is the coarse operation class.
	Operation OperationKind
	// TargetCount is the number of resolved mutation targets.
	TargetCount int
	// FileCount is the number of distinct files the operation touches.
	FileCount int
	// DependencyCount is the count of cross-file structural dependencies
	// discovered deterministically (0 when unknown — never fabricated).
	DependencyCount int
	// Ambiguous reports whether the target set or the request is ambiguous.
	Ambiguous bool
	// RepositoryScope reports whether the request spans the repository rather
	// than a named target set.
	RepositoryScope bool
	// CrossFileCoupling reports whether the change touches coupled files.
	CrossFileCoupling bool
	// VerificationDepth is the deterministic verification required:
	// 0 none, 1 syntax, 2 build, 3 test.
	VerificationDepth int
	// ArtifactLines is the expected artifact size in lines (0 = unknown).
	ArtifactLines int
	// ExplicitTargets reports whether the request names explicit targets.
	ExplicitTargets bool
}

// Assess computes the execution complexity from the measurable factors. The
// returned Complexity carries every factor with its reason so the estimate is
// auditable and $inspect can show "why is this medium".
func Assess(in ComplexityInputs) Complexity {
	score := in.Operation.baseScore()
	factors := []Factor{{
		Name:   "operation",
		Score:  score,
		Weight: 1,
		Reason: "operation class " + in.Operation.String(),
	}}

	// Extra targets beyond the first each force an additional read/artifact.
	if n := in.TargetCount - 1; n > 0 {
		s := minInt(n, 2)
		score += s
		factors = append(factors, Factor{Name: "targets", Score: s, Weight: 1,
			Reason: fmt.Sprintf("%d additional targets", n)})
	}

	// Multiple distinct files cross the single-file boundary.
	if n := in.FileCount - 1; n > 0 {
		s := minInt(n, 2)
		score += s
		factors = append(factors, Factor{Name: "files", Score: s, Weight: 1,
			Reason: fmt.Sprintf("%d distinct files involved", in.FileCount)})
	}

	// Cross-file structural dependencies demand coordination.
	if in.DependencyCount > 0 {
		s := minInt(in.DependencyCount, 3)
		score += s
		factors = append(factors, Factor{Name: "dependencies", Score: s, Weight: 1,
			Reason: fmt.Sprintf("%d structural dependencies", in.DependencyCount)})
	}

	// Ambiguity forces the human into the loop or an extra discovery pass.
	if in.Ambiguous {
		score += 3
		factors = append(factors, Factor{Name: "ambiguity", Score: 3, Weight: 1,
			Reason: "target set is ambiguous"})
	}

	// Repository scope implies evidence discovery before reasoning.
	if in.RepositoryScope {
		score += 3
		factors = append(factors, Factor{Name: "repository-scope", Score: 3, Weight: 1,
			Reason: "request spans the repository, no explicit target set"})
	}

	// Cross-file coupling makes a mutation ripple.
	if in.CrossFileCoupling {
		score += 1
		factors = append(factors, Factor{Name: "coupling", Score: 1, Weight: 1,
			Reason: "change touches coupled files"})
	}

	// Deeper verification gates cost execution but also increase confidence.
	if in.VerificationDepth > 0 {
		s := minInt(in.VerificationDepth, 3)
		score += s
		factors = append(factors, Factor{Name: "verification", Score: s, Weight: 1,
			Reason: fmt.Sprintf("verification depth %d", in.VerificationDepth)})
	}

	// A large expected artifact forces a larger output budget but does not by
	// itself inflate reasoning complexity; it only adds +1 when material.
	if in.ArtifactLines > 200 {
		score += 1
		factors = append(factors, Factor{Name: "artifact-size", Score: 1, Weight: 1,
			Reason: fmt.Sprintf("expected artifact ~%d lines", in.ArtifactLines)})
	}

	level := ComplexityLow
	switch {
	case score >= 7:
		level = ComplexityHigh
	case score >= 4:
		level = ComplexityMedium
	}

	return Complexity{Level: level, Score: score, Factors: factors}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

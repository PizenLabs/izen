package domain

// OrthogonalityCheck is a compile-time / lint-time assertion that no struct
// in internal/core/domain fuses two dimensions without a projection seam.
//
// Forbidden examples (must fail lint):
//
//	type Bad struct { WorkflowState; ArtifactStore; CapabilitySet } // V2-A pattern
//	type Bad2 struct { Mode string; Context; Strategy }             // V2-C pattern
type OrthogonalityCheck interface {
	CheckOrthogonality()
}

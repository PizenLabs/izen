package domain

import "github.com/PizenLabs/izen/internal/core/domain/occ"

// ScopeProvenance records the explicit directive that authorized mutation.
// Its zero value is deliberately read-only, including restored legacy state.
type ScopeProvenance uint8

const (
	ScopeNone     ScopeProvenance = iota
	ScopeDynamic                  // $prompt: the runtime resolves the mutation scope.
	ScopeDeclared                 // $hot: the user declares a bounded mutation scope.
)

const ScopeAuthorizationError = "state error: mutation plan requires scope authorization via $prompt or $hot"

func (s ScopeProvenance) AllowsMutation() bool {
	return s == ScopeDynamic || s == ScopeDeclared
}

// ExecutionIntent is the Control Plane's validated request handed to
// RuntimeExecutor. It bundles the bounded ExecutionUnit with the full
// authorization evidence bundle so the 6-clause formula can be evaluated
// atomically before any side-effect reaches Substrate.
type ExecutionIntent struct {
	ScopeProvenance ScopeProvenance `json:"scope_provenance"`
	Objective       Objective       `json:"objective"`
	Unit            ExecutionUnit   `json:"unit"`
	Budget          ResourceBudget  `json:"budget"`
	// Capabilities is the scoped capability grant for this unit.
	Capabilities DomainCapabilitySet `json:"capabilities"`
	// SourceState is the workspace source fingerprint observed before execution.
	SourceState SourceState `json:"source_state"`
	// ArtifactRef describes the plan/patch artifact that authorizes the mutation.
	Artifact            ArtifactRef  `json:"artifact"`
	CheckpointID        CheckpointID `json:"checkpoint_id"`
	HasCheckpoint       bool         `json:"has_checkpoint"`
	HumanApproved       bool         `json:"human_approved"`
	BudgetIsPreApproval bool         `json:"budget_is_pre_approval"`
	// ExpectedVersion is the OCC version the caller observed at intent creation.
	ExpectedVersion occ.StateVersion `json:"expected_version"`
	// WorkflowState indicates the current workflow phase; building/repairing
	// triggers a checkpoint before mutation.
	WorkflowState WorkflowState `json:"workflow_state"`
}

// ArtifactRef describes the lifecycle state of the authorizing artifact.
type ArtifactRef struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Hash  string `json:"hash"`
}

package domain

// ExecutionUnit is the atomic payload the Runtime hands to the Execution Plane.
type ExecutionUnit struct {
	UnitID             UnitID              `json:"unit_id"`       // "unit_<ulid>"
	FrameID            FrameID             `json:"frame_id"`
	ObjectiveSlice     ObjectiveSlice      `json:"objective_slice"` // bounded slice of Objective
	InputContext       CompiledContext     `json:"input_context"`   // output of ContextCompiler
	OutputBudget       OutputPolicy        `json:"output_budget"`
	CapabilityBoundary DomainCapabilitySet `json:"capability_boundary"` // scoped grant for this unit
	Verification       VerificationPolicy  `json:"verification"`
	CheckpointID       CheckpointID        `json:"checkpoint_id"` // local checkpoint for this unit
}

type UnitID string

type ObjectiveSlice struct {
	Description string   `json:"description"`
	Targets     []string `json:"targets"` // file/symbol scope for this unit
	Constraints []string `json:"constraints"`
}

type CompiledContext struct {
	Channels []ContextChannel `json:"channels"`
	Tokens   int              `json:"tokens"`
	Digest   string           `json:"digest"` // sha256 of compiled prompt
}

type ContextChannel struct {
	Name    string `json:"name"`    // e.g., "symbol_graph", "dependency_graph", "evidence"
	Tokens  int    `json:"tokens"`
	Explain string `json:"explain"` // why this channel is included — strict compiler (V5-C)
}

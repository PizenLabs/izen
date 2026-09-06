package domain

// ExecutionFrame is the degenerate DAG node that scopes one bounded execution.
// Frames form a DAG (parent lineage), but the single-frame case is the common
// degenerate form.
type ExecutionFrame struct {
	FrameID      FrameID                `json:"frame_id"` // globally unique, non-sequential
	ParentID     *FrameID               `json:"parent_id,omitempty"`
	Lineage      FrameLineage           `json:"lineage"`       // causal ancestry chain
	Scope        Scope                  `json:"scope"`         // frozen at frame creation
	CheckpointID CheckpointID           `json:"checkpoint_id"` // git blob-store ref — ARCH:22
	Strategy     ExecutionStrategy      `json:"strategy"`
	Attempt      AttemptCounter         `json:"attempt"`
	DependsOn    []Dependency           `json:"depends_on"` // artifact deps with hashes
	Status       FrameStatus            `json:"status"`
	Unit         *ExecutionUnit         `json:"unit,omitempty"`
	Observations []ExecutionObservation `json:"observations"`
}

type FrameID string         // "frame_<ulid>" — globally unique
type CheckpointID string    // "chkpt_<sha256>" — content-addressed
type FrameLineage []FrameID // root → parent chain

type AttemptCounter struct {
	Current int `json:"current"` // 1-indexed
	Max     int `json:"max"`     // from ResourceBudget.MaxAttempts
}

type Dependency struct {
	ArtifactID string `json:"artifact_id"`
	Hash       string `json:"hash"`
	Path       string `json:"path,omitempty"`
}

type FrameStatus uint8

const (
	FramePending FrameStatus = iota
	FrameRunning
	FrameCompleted
	FrameFailed
	FrameRolledBack
	FrameAborted
)

// RollbackBoundary declares the granularity the runtime may use.
type RollbackBoundary uint8

const (
	RollbackLocal     RollbackBoundary = iota // this frame only
	RollbackAncestral                         // parent chain per dependency graph
	RollbackTask                              // entire objective
)

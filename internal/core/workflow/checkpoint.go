package workflow

import "fmt"

type CheckpointRef string

type CheckpointManager interface {
	CreateCheckpoint() (CheckpointRef, error)
	RollbackToCheckpoint(ref CheckpointRef) error
	HasCheckpoint() bool
	LatestCheckpoint() (CheckpointRef, error)
}

type CheckpointCoordinator struct {
	mgr    CheckpointManager
	ref    CheckpointRef
	hasRef bool
}

func NewCheckpointCoordinator(mgr CheckpointManager) *CheckpointCoordinator {
	return &CheckpointCoordinator{mgr: mgr}
}

func (cc *CheckpointCoordinator) CreateBeforeBuild() error {
	ref, err := cc.mgr.CreateCheckpoint()
	if err != nil {
		return fmt.Errorf("workflow: checkpoint create failed: %w", err)
	}
	cc.ref = ref
	cc.hasRef = true
	return nil
}

func (cc *CheckpointCoordinator) Rollback() error {
	if !cc.hasRef {
		return fmt.Errorf("workflow: no checkpoint to rollback to")
	}
	return cc.mgr.RollbackToCheckpoint(cc.ref)
}

func (cc *CheckpointCoordinator) HasRef() bool {
	return cc.hasRef
}

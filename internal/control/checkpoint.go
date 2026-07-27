package control

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
)

type CheckpointController struct {
	executor *execution.Engine
}

func NewCheckpointController(executor *execution.Engine) *CheckpointController {
	return &CheckpointController{executor: executor}
}

func (cc *CheckpointController) EnsureCheckpoint(message string) error {
	if cc.executor == nil {
		return fmt.Errorf("execution engine not initialized")
	}
	cp, err := cc.executor.Checkpoints.Create(message)
	if err != nil {
		return fmt.Errorf("checkpoint creation failed: %w", err)
	}
	if cp == nil {
		return fmt.Errorf("checkpoint was not created — no git changes and no checkpoint exists")
	}
	return nil
}

func (cc *CheckpointController) EnsureCheckpointOrCreate(message string) error {
	if cc.executor == nil {
		return fmt.Errorf("execution engine not initialized")
	}
	if len(cc.executor.Checkpoints.List()) > 0 {
		return nil
	}
	cp, err := cc.executor.Checkpoints.Create(message)
	if err != nil {
		return fmt.Errorf("on-the-fly checkpoint creation failed: %w", err)
	}
	if cp == nil {
		return fmt.Errorf("on-the-fly checkpoint was not created — no git changes to checkpoint")
	}
	return nil
}

type workflowCheckpointManager struct {
	checkpoints *execution.CheckpointManager
	root        string
}

func NewWorkflowCheckpointManager(checkpoints *execution.CheckpointManager, root string) workflow.CheckpointManager {
	return &workflowCheckpointManager{
		checkpoints: checkpoints,
		root:        root,
	}
}

func (w *workflowCheckpointManager) CreateCheckpoint() (workflow.CheckpointRef, error) {
	cp, err := w.checkpoints.Create("izen build: before execution")
	if err != nil {
		return "", err
	}
	if cp == nil {
		return workflow.CheckpointRef(""), nil
	}
	return workflow.CheckpointRef(cp.ID), nil
}

func (w *workflowCheckpointManager) RollbackToCheckpoint(ref workflow.CheckpointRef) error {
	return w.checkpoints.Restore(string(ref))
}

func (w *workflowCheckpointManager) HasCheckpoint() bool {
	return len(w.checkpoints.List()) > 0
}

func (w *workflowCheckpointManager) LatestCheckpoint() (workflow.CheckpointRef, error) {
	entries := w.checkpoints.List()
	if len(entries) == 0 {
		return "", nil
	}
	latest := entries[len(entries)-1]
	for _, e := range entries {
		if e > latest {
			latest = e
		}
	}
	return workflow.CheckpointRef(latest), nil
}

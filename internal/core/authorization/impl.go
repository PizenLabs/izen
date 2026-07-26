package authorization

import (
	"os"
	"path/filepath"

	"github.com/PizenLabs/izen/internal/core/workflow"
)

type noopSourceHashVerifier struct{}

func (n *noopSourceHashVerifier) VerifySourceHash(paths []string, snapshotHash string) error {
	return nil
}

func newNoopSourceHashVerifier() SourceHashVerifier {
	return &noopSourceHashVerifier{}
}

type productionCheckpointChecker struct {
	checkpointDir string
}

func newProductionCheckpointChecker(root string) CheckpointChecker {
	return &productionCheckpointChecker{
		checkpointDir: filepath.Join(root, ".izen", "checkpoints"),
	}
}

func (c *productionCheckpointChecker) HasCheckpoint() bool {
	info, err := os.Stat(c.checkpointDir)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(c.checkpointDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			cpFile := filepath.Join(c.checkpointDir, e.Name(), "checkpoint.json")
			if _, err := os.Stat(cpFile); err == nil {
				return true
			}
		}
	}
	return false
}

func (c *productionCheckpointChecker) LatestCheckpoint() (workflow.CheckpointRef, error) {
	entries, err := os.ReadDir(c.checkpointDir)
	if err != nil {
		return "", err
	}
	var latest string
	for _, e := range entries {
		if e.IsDir() {
			cpFile := filepath.Join(c.checkpointDir, e.Name(), "checkpoint.json")
			if _, err := os.Stat(cpFile); err == nil {
				if e.Name() > latest {
					latest = e.Name()
				}
			}
		}
	}
	if latest == "" {
		return "", nil
	}
	return workflow.CheckpointRef(latest), nil
}

func NewProductionAuthorizationEngine(root string, getState func() workflow.WorkflowState) *AuthorizationEngine {
	return NewAuthorizationEngine(
		newNoopSourceHashVerifier(),
		newProductionCheckpointChecker(root),
		getState,
	)
}

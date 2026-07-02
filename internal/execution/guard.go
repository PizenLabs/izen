package execution

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/session"
)

// Guard provides pre/post execution validation and checkpointing
type Guard struct {
	execEngine *Engine
	gitEng     *git.Engine
	session    *session.Session
	config     *config.Config
}

// NewGuard creates a new execution guard
func NewGuard() *Guard {
	return &Guard{}
}

// Initialize sets up the guard with required dependencies
func (g *Guard) Initialize(execEngine *Engine, gitEng *git.Engine, session *session.Session, config *config.Config) {
	g.execEngine = execEngine
	g.gitEng = gitEng
	g.session = session
	g.config = config
}

// ValidateAndCheckpoint runs pre-execution validations and creates a checkpoint
func (g *Guard) ValidateAndCheckpoint() error {
	// TODO: Implement actual validation logic
	// 1. Check git status
	// 2. Run go build/test in sandbox if configured
	// 3. Create checkpoint

	// For now, just create a checkpoint if we have the dependencies
	if g.execEngine != nil && g.session != nil {
		_, err := g.execEngine.Checkpoints.Create("pre-execution")
		return err
	}
	return nil
}

// ValidatePostExecution runs post-execution validations
func (g *Guard) ValidatePostExecution() error {
	// TODO: Implement actual validation logic
	// 1. Run go build/test to ensure nothing is broken
	// 2. Verify no unintended changes

	// For now, just return nil (no validation)
	return nil
}

// RollbackToCheckpoint rolls back to the latest checkpoint
func (g *Guard) RollbackToCheckpoint() error {
	// TODO: Implement actual rollback logic
	if g.execEngine != nil {
		checkpoints := g.execEngine.Checkpoints.List()
		if len(checkpoints) > 0 {
			// Get the latest checkpoint (last in the list)
			latestID := checkpoints[len(checkpoints)-1]
			return g.execEngine.Checkpoints.Restore(latestID)
		}
	}
	return fmt.Errorf("no checkpoints available to rollback to")
}

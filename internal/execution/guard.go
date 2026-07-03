package execution

import (
	"errors"
	"fmt"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/session"
)

var (
	ErrCapabilityShell      = errors.New("capability guard: shell execution denied by mode capabilities")
	ErrCapabilityWrite      = errors.New("capability guard: write operations denied by mode capabilities")
	ErrCapabilityPatch      = errors.New("capability guard: patch generation denied by mode capabilities")
	ErrCapabilityTest       = errors.New("capability guard: test execution denied by mode capabilities")
	ErrCapabilityCheckpoint = errors.New("capability guard: checkpoint operations denied by mode capabilities")
)

// Guard provides pre/post execution validation and checkpointing.
// Any command execution or context loading triggered via Guard explicitly
// verifies its permission flags against the target mode payload before
// dispatching.
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

// VerifyShellCap checks if the given mode allows shell execution. Returns
// ErrCapabilityShell if shell is not permitted.
func VerifyShellCap(m modes.Mode) error {
	if !m.CanShell() {
		return fmt.Errorf("%w (mode=%s)", ErrCapabilityShell, m)
	}
	return nil
}

// VerifyWriteCap checks if the given mode allows file writes.
func VerifyWriteCap(m modes.Mode) error {
	if !m.CanWrite() {
		return fmt.Errorf("%w (mode=%s)", ErrCapabilityWrite, m)
	}
	return nil
}

// VerifyPatchCap checks if the given mode allows patch generation/application.
func VerifyPatchCap(m modes.Mode) error {
	if !m.CanPatch() {
		return fmt.Errorf("%w (mode=%s)", ErrCapabilityPatch, m)
	}
	return nil
}

// VerifyTestCap checks if the given mode allows test execution.
func VerifyTestCap(m modes.Mode) error {
	if !m.CanTest() {
		return fmt.Errorf("%w (mode=%s)", ErrCapabilityTest, m)
	}
	return nil
}

// VerifyCheckpointCap checks if the given mode allows checkpoint operations.
func VerifyCheckpointCap(m modes.Mode) error {
	if !m.CanCheckpoint() {
		return fmt.Errorf("%w (mode=%s)", ErrCapabilityCheckpoint, m)
	}
	return nil
}

// ValidateAndCheckpoint runs pre-execution validations and creates a checkpoint.
// It first verifies that the current mode permits checkpoint operations.
func (g *Guard) ValidateAndCheckpoint() error {
	if g.session != nil {
		if err := VerifyCheckpointCap(g.session.Mode); err != nil {
			return err
		}
	}
	if g.execEngine != nil && g.session != nil {
		_, err := g.execEngine.Checkpoints.Create("pre-execution")
		return err
	}
	return nil
}

// ValidatePostExecution runs post-execution validations.
func (g *Guard) ValidatePostExecution() error {
	return nil
}

// RollbackToCheckpoint rolls back to the latest checkpoint.
func (g *Guard) RollbackToCheckpoint() error {
	if g.execEngine != nil {
		checkpoints := g.execEngine.Checkpoints.List()
		if len(checkpoints) > 0 {
			latestID := checkpoints[len(checkpoints)-1]
			return g.execEngine.Checkpoints.Restore(latestID)
		}
	}
	return fmt.Errorf("no checkpoints available to rollback to")
}

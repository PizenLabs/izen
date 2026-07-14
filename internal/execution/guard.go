package execution

import (
	"errors"
	"fmt"
	"strings"

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
	ErrNoActiveContext      = errors.New("guard: no active context ID set")
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
	contextID  string
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

// SetContextID binds a #number context to this guard for resource tracking.
func (g *Guard) SetContextID(id string) {
	g.contextID = id
}

// ActiveContextID returns the bound context ID.
func (g *Guard) ActiveContextID() string {
	return g.contextID
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

// ValidatePreRun kills orphaned processes from previous runs of the same
// context before allowing a new run to proceed.
func (g *Guard) ValidatePreRun() error {
	if g.contextID == "" {
		return ErrNoActiveContext
	}
	KillOrphanedByContext(g.contextID)
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

// CleanupContext kills all orphaned processes registered under the active
// context ID and prunes their entries.
func (g *Guard) CleanupContext() {
	if g.contextID != "" {
		KillOrphanedByContext(g.contextID)
	}
}

// RollbackToContextCheckpoint restores the exact checkpoint linked to the
// active context ID by scanning checkpoints for one whose ID prefix matches.
func (g *Guard) RollbackToContextCheckpoint() error {
	if g.execEngine == nil {
		return fmt.Errorf("execution engine not initialized")
	}
	if g.contextID == "" {
		return ErrNoActiveContext
	}

	checkpoints := g.execEngine.Checkpoints.List()
	for _, cp := range checkpoints {
		if strings.Contains(cp, g.contextID) || strings.HasPrefix(cp, g.contextID) {
			return g.execEngine.Checkpoints.Restore(cp)
		}
	}

	// Fallback: restore the latest checkpoint
	return g.RollbackToCheckpoint()
}

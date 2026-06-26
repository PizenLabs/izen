package execution

import (
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/session"
)

type Engine struct {
	Runner      *Runner
	Test        *TestRunner
	Patches     *PatchManager
	Checkpoints *CheckpointManager
	Git         *git.Engine
	root        string
}

func NewEngine(root string, cfg *config.Config, sess *session.Session) *Engine {
	return &Engine{
		Runner:      NewRunner(root, cfg.Execution.Sandbox, cfg.Execution.Confirm),
		Test:        NewTestRunner(root),
		Patches:     NewPatchManager(root),
		Checkpoints: NewCheckpointManager(root, sess),
		Git:         git.NewEngine(root),
		root:        root,
	}
}

func (e *Engine) Root() string {
	return e.root
}

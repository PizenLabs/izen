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
	PatchQueue  *PatchQueue
	StreamMon   *StreamMonitor
	root        string
}

func NewEngine(root string, cfg *config.Config, sess *session.Session) *Engine {
	e := &Engine{
		Runner:      NewRunner(root, cfg.Execution.Sandbox, cfg.Execution.Confirm),
		Test:        NewTestRunner(root),
		Patches:     NewPatchManager(root),
		Checkpoints: NewCheckpointManager(root, sess),
		Git:         git.NewEngine(root),
		root:        root,
	}
	e.PatchQueue = NewPatchQueue(root, e.Patches)
	e.StreamMon = NewStreamMonitor(e.PatchQueue)
	return e
}

func (e *Engine) Root() string {
	return e.root
}

func (e *Engine) IsPatchStaged() bool {
	return e.PatchQueue.IsStaged()
}

func (e *Engine) StagedPatches() []StagedPatch {
	return e.PatchQueue.List()
}

func (e *Engine) ApplyNextPatch() error {
	return e.PatchQueue.ApplyNext()
}

func (e *Engine) ApplyAllPatches() (int, error) {
	return e.PatchQueue.ApplyAll()
}

func (e *Engine) RejectPatches() {
	e.PatchQueue.Clear()
}

func (e *Engine) FeedStream(chunk string) {
	e.StreamMon.Feed(chunk)
}

func (e *Engine) SetStreamContextFiles(files []string) {
	e.StreamMon.SetContextFiles(files)
}

func (e *Engine) FlushStream() {
	e.StreamMon.Flush()
	e.StreamMon.Reset()
}

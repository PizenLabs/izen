package execution

import (
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
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
	PlanStore   *plan.PlanStore
	root        string

	Policy   *PolicyEngine
	Risk     *RiskClassifier
	Verifier *Verifier
	Diff     *DiffAnalyzer
	Pipeline *PipelineRunner
}

func NewEngine(root string, cfg *config.Config, sess *session.Session) *Engine {
	r := NewRunner(root, cfg.Execution.Sandbox, cfg.Execution.Confirm)
	t := NewTestRunner(root)
	p := NewPatchManager(root)
	c := NewCheckpointManager(root, sess)

	pe := NewPolicyEngine(func() modes.Capability {
		if sess != nil {
			return sess.Mode.Capabilities()
		}
		return 0
	})
	rc := NewRiskClassifier()
	v := NewVerifier(root)
	d := NewDiffAnalyzer()

	e := &Engine{
		Runner:      r,
		Test:        t,
		Patches:     p,
		Checkpoints: c,
		Git:         git.NewEngine(root),
		root:        root,
		Policy:      pe,
		Risk:        rc,
		Verifier:    v,
		Diff:        d,
	}
	e.PatchQueue = NewPatchQueue(root, e.Patches)
	e.StreamMon = NewStreamMonitor(e.PatchQueue)
	e.Pipeline = NewPipelineRunner(e)

	r.SetRiskClassifier(rc)
	sandboxMode := SandboxPolicy
	switch cfg.Execution.SandboxMode {
	case "all":
		sandboxMode = SandboxAll
	case "highrisk":
		sandboxMode = SandboxHighRisk
	case "disabled":
		sandboxMode = SandboxDisabled
	}
	r.SetSandboxMode(sandboxMode)

	if cfg.Execution.Verification.Enabled {
		configureVerifier(v, cfg.Execution.Verification)
	}

	return e
}

func configureVerifier(v *Verifier, vc config.VerificationConfig) {
	if len(vc.Steps) > 0 {
		steps := make([]VerificationStep, len(vc.Steps))
		for i, s := range vc.Steps {
			steps[i] = VerificationStep{
				Name:    s,
				Command: s,
			}
		}
		v.SetCustomSteps(steps)
	}
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
	e.PatchQueue.SetContextFiles(files)
}

func (e *Engine) FlushStream() {
	e.StreamMon.Flush()
	e.StreamMon.Reset()
}

func (e *Engine) StepCompleted(stepNum int) error {
	if e.PlanStore == nil {
		return nil
	}
	return e.PlanStore.TickTaskHoanThanh(stepNum)
}

func (e *Engine) SetPlanStore(ps *plan.PlanStore) {
	e.PlanStore = ps
}

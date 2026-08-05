package plan

import (
	"io/fs"

	"github.com/PizenLabs/izen/pkg/engine/strategy"
)

// ExecutableStep is one physical, lowered unit of an ExecutablePlan. It
// carries the concrete command, working directory, resolved absolute target
// and environment needed by an executor.
type ExecutableStep struct {
	step     Step
	command  string
	args     []string
	workDir  string
	resolved string
	env      map[string]string
	mode     fs.FileMode
	shell    bool
}

// Step returns the logical step this executable step was lowered from.
func (s ExecutableStep) Step() Step { return s.step }

// Command returns the physical command to run.
func (s ExecutableStep) Command() string { return s.command }

// Args returns the physical command arguments.
func (s ExecutableStep) Args() []string { return append([]string(nil), s.args...) }

// WorkDir returns the working directory the command runs in.
func (s ExecutableStep) WorkDir() string { return s.workDir }

// ResolvedTarget returns the absolute filesystem path of the step, or "" for
// command-only steps.
func (s ExecutableStep) ResolvedTarget() string { return s.resolved }

// Env returns the merged environment for the step.
func (s ExecutableStep) Env() map[string]string {
	out := make(map[string]string, len(s.env))
	for k, v := range s.env {
		out[k] = v
	}
	return out
}

// Mode returns the file mode applied to create/modify steps.
func (s ExecutableStep) Mode() fs.FileMode { return s.mode }

// Shell reports whether the command executes through a shell.
func (s ExecutableStep) Shell() bool { return s.shell }

// Verify reports whether the step is a logical verify.
func (s ExecutableStep) Verify() bool { return s.step.Kind() == StepVerify }

// ExecutablePlan is the immutable, physical plan produced by the PlanLowerer
// and consumed by ExecutionPreconditions and, ultimately, an executor.
type ExecutablePlan struct {
	id      string
	goal    strategy.Goal
	steps   []ExecutableStep
	workDir string
	env     map[string]string
}

// NewExecutablePlan constructs the immutable executable artifact.
func NewExecutablePlan(goal strategy.Goal, steps []ExecutableStep, workDir string, env map[string]string) *ExecutablePlan {
	return &ExecutablePlan{
		id:      newPlanID("executable"),
		goal:    goal,
		steps:   append([]ExecutableStep(nil), steps...),
		workDir: workDir,
		env:     env,
	}
}

// ID returns the immutable artifact identifier.
func (p *ExecutablePlan) ID() string { return p.id }

// Goal returns the strategy goal the plan serves.
func (p *ExecutablePlan) Goal() strategy.Goal { return p.goal }

// Steps returns a defensive copy of the executable steps.
func (p *ExecutablePlan) Steps() []ExecutableStep {
	return append([]ExecutableStep(nil), p.steps...)
}

// StepCount returns the number of executable steps.
func (p *ExecutablePlan) StepCount() int { return len(p.steps) }

// WorkDir returns the plan-wide working directory.
func (p *ExecutablePlan) WorkDir() string { return p.workDir }

// Env returns the plan-wide environment.
func (p *ExecutablePlan) Env() map[string]string {
	out := make(map[string]string, len(p.env))
	for k, v := range p.env {
		out[k] = v
	}
	return out
}

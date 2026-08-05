package plan

import (
	"fmt"
	"path/filepath"
	"strings"
)

// LowererOption configures a PlanLowerer.
type LowererOption func(*PlanLowerer)

// WithLowererEnv seeds the plan-wide environment that every lowered step
// inherits.
func WithLowererEnv(env map[string]string) LowererOption {
	return func(l *PlanLowerer) {
		for k, v := range env {
			l.env[k] = v
		}
	}
}

// PlanLowerer lowers an immutable ValidatedPlan into a physical
// ExecutablePlan: abstract steps become concrete commands with a working
// directory, resolved absolute targets and a merged environment. It is a
// pure transformation — no filesystem is touched — and never mutates its
// input.
type PlanLowerer struct {
	workDir string
	env     map[string]string
}

// NewPlanLowerer returns a lowerer rooted at the absolute working directory.
func NewPlanLowerer(workDir string, opts ...LowererOption) *PlanLowerer {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	l := &PlanLowerer{workDir: abs, env: map[string]string{}}
	for _, o := range opts {
		o(l)
	}
	return l
}

// WorkDir returns the absolute lowering root.
func (l *PlanLowerer) WorkDir() string { return l.workDir }

// Lower transforms the ValidatedPlan into an ExecutablePlan. An invalid or
// nil plan is rejected; a file target that escapes the working directory is
// rejected as a physical impossibility.
func (l *PlanLowerer) Lower(in *ValidatedPlan) (*ExecutablePlan, error) {
	if in == nil {
		return nil, fmt.Errorf("%w: validated", ErrNilPlan)
	}
	if !in.Valid() {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPlan, in.ID())
	}

	steps := in.Steps()
	out := make([]ExecutableStep, 0, len(steps))
	for _, s := range steps {
		es, err := l.lift(s)
		if err != nil {
			return nil, err
		}
		out = append(out, es)
	}
	return NewExecutablePlan(in.Goal(), out, l.workDir, l.env), nil
}

// lift lowers one logical Step into an ExecutableStep.
func (l *PlanLowerer) lift(s Step) (ExecutableStep, error) {
	es := ExecutableStep{
		step:    s,
		workDir: l.workDir,
		env:     l.env,
		mode:    defaultMode,
	}
	switch {
	case s.Kind() == StepCreate:
		resolved, err := l.resolve(s.Target())
		if err != nil {
			return ExecutableStep{}, err
		}
		es.command, es.resolved, es.shell = "write-file", resolved, false
	case s.Kind() == StepModify:
		resolved, err := l.resolve(s.Target())
		if err != nil {
			return ExecutableStep{}, err
		}
		es.command, es.resolved, es.shell = "write-file", resolved, false
	case s.Kind() == StepDelete:
		resolved, err := l.resolve(s.Target())
		if err != nil {
			return ExecutableStep{}, err
		}
		es.command, es.resolved, es.shell = "remove-file", resolved, false
	case s.Kind() == StepRead:
		resolved, err := l.resolve(s.Target())
		if err != nil {
			return ExecutableStep{}, err
		}
		es.command, es.resolved, es.shell = "read-file", resolved, false
	case s.Kind() == StepRun:
		es.command, es.shell = s.Target(), true
		es.args = splitCommand(s.Target())
	case s.Kind() == StepVerify:
		es.command, es.shell = "verify", false
		if target := strings.TrimSpace(s.Target()); target != "" && target != "verify" {
			es.args = []string{target}
		}
	default:
		return ExecutableStep{}, fmt.Errorf("plan: cannot lower unknown step kind %q", s.Kind())
	}
	return es, nil
}

// resolve joins a target against the working directory and guarantees the
// result stays inside it.
func (l *PlanLowerer) resolve(target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("plan: step has empty target")
	}
	joined := filepath.Join(l.workDir, filepath.FromSlash(target))
	if !withinRoot(joined, l.workDir) {
		return "", fmt.Errorf("plan: target %q escapes the working directory", target)
	}
	return filepath.Clean(joined), nil
}

// splitCommand splits a shell command string into its argv tokens.
func splitCommand(cmd string) []string {
	if cmd == "" {
		return nil
	}
	return strings.Fields(cmd)
}

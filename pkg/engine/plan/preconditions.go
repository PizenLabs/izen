package plan

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PreconditionCheck is one real-world filesystem or environment check
// verdict. Fatal marks a check that must pass before execution can start;
// a non-fatal failure is recorded as a warning.
type PreconditionCheck struct {
	// StepID is the step the check applied to, or "plan" for plan-wide
	// checks.
	StepID string
	// Name is the stable check identifier, e.g. "filesystem:containment".
	Name string
	// OK reports whether the check passed.
	OK bool
	// Fatal marks a check whose failure blocks execution.
	Fatal bool
	// Detail is a human-readable explanation.
	Detail string
}

// PreconditionReport is the immutable result of running execution
// preconditions against an ExecutablePlan.
type PreconditionReport struct {
	ready  bool
	checks []PreconditionCheck
}

// Ready reports whether no fatal precondition failed.
func (r *PreconditionReport) Ready() bool { return r.ready }

// Checks returns a defensive copy of the check verdicts.
func (r *PreconditionReport) Checks() []PreconditionCheck {
	return append([]PreconditionCheck(nil), r.checks...)
}

// Failed returns the checks that failed and are fatal.
func (r *PreconditionReport) Failed() []PreconditionCheck {
	var out []PreconditionCheck
	for _, c := range r.checks {
		if !c.OK && c.Fatal {
			out = append(out, c)
		}
	}
	return out
}

// Warnings returns the non-fatal failures.
func (r *PreconditionReport) Warnings() []PreconditionCheck {
	var out []PreconditionCheck
	for _, c := range r.checks {
		if !c.OK && !c.Fatal {
			out = append(out, c)
		}
	}
	return out
}

// ExecutionPreconditions performs real-world filesystem and environment
// checks on an ExecutablePlan before execution: the working directory must
// exist, file targets must stay inside it, parent directories must be
// creatable, and tools required by shell steps must be present. It never
// modifies the filesystem; directory creation is left to the executor.
type ExecutionPreconditions struct {
	workDir string
}

// NewExecutionPreconditions returns a precondition checker bound to the
// workspace root. Plans lowered for a different root are rejected.
func NewExecutionPreconditions(workDir string) *ExecutionPreconditions {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	return &ExecutionPreconditions{workDir: abs}
}

// Check inspects an ExecutablePlan and returns an immutable report. The
// plan is never mutated.
func (e *ExecutionPreconditions) Check(p *ExecutablePlan) (*PreconditionReport, error) {
	if p == nil {
		return nil, fmt.Errorf("%w: executable", ErrNilPlan)
	}

	var checks []PreconditionCheck
	add := func(c PreconditionCheck) {
		checks = append(checks, c)
	}

	// ── Plan-wide environment checks ────────────────────────────────────
	workDir := p.WorkDir()
	if !withinRoot(workDir, e.workDir) {
		add(PreconditionCheck{StepID: "plan", Name: "environment:root_mismatch", OK: false, Fatal: true,
			Detail: fmt.Sprintf("plan working directory %s is outside the bound root %s", workDir, e.workDir)})
	}
	info, err := os.Stat(workDir)
	switch {
	case err != nil:
		add(PreconditionCheck{StepID: "plan", Name: "environment:workdir", OK: false, Fatal: true,
			Detail: fmt.Sprintf("working directory %s is not accessible: %v", workDir, err)})
	case !info.IsDir():
		add(PreconditionCheck{StepID: "plan", Name: "environment:workdir", OK: false, Fatal: true,
			Detail: fmt.Sprintf("working directory %s is not a directory", workDir)})
	default:
		add(PreconditionCheck{StepID: "plan", Name: "environment:workdir", OK: true, Detail: workDir})
	}

	// ── Per-step filesystem checks ──────────────────────────────────────
	for _, es := range p.Steps() {
		s := es.Step()
		if resolved := es.ResolvedTarget(); resolved != "" {
			if !withinRoot(resolved, workDir) {
				add(PreconditionCheck{StepID: s.ID(), Name: "filesystem:containment", OK: false, Fatal: true,
					Detail: fmt.Sprintf("resolved target %s escapes the working directory", resolved)})
				continue
			}
			add(PreconditionCheck{StepID: s.ID(), Name: "filesystem:containment", OK: true, Detail: resolved})
			e.checkTarget(add, s, resolved)
		}

		if es.Shell() && es.Command() != "" {
			tool := strings.Fields(es.Command())[0]
			if _, err := exec.LookPath(tool); err != nil {
				add(PreconditionCheck{StepID: s.ID(), Name: "environment:tool", OK: false, Fatal: true,
					Detail: fmt.Sprintf("required tool %q is not on PATH", tool)})
			} else {
				add(PreconditionCheck{StepID: s.ID(), Name: "environment:tool", OK: true, Detail: tool})
			}
		}
	}

	ready := true
	for _, c := range checks {
		if !c.OK && c.Fatal {
			ready = false
			break
		}
	}
	return &PreconditionReport{ready: ready, checks: checks}, nil
}

// checkTarget verifies the filesystem expectations of one file-target step:
// the parent directory must exist or be creatable, create steps must not
// collide with an existing non-directory, and read/delete steps should
// reference a present path.
func (e *ExecutionPreconditions) checkTarget(add func(PreconditionCheck), s Step, resolved string) {
	parent := filepath.Dir(resolved)
	parentInfo, err := os.Stat(parent)
	switch {
	case err != nil:
		add(PreconditionCheck{StepID: s.ID(), Name: "filesystem:parent", OK: true, Fatal: false,
			Detail: fmt.Sprintf("parent %s does not exist yet; it will be created", parent)})
	case !parentInfo.IsDir():
		add(PreconditionCheck{StepID: s.ID(), Name: "filesystem:parent", OK: false, Fatal: true,
			Detail: fmt.Sprintf("parent %s exists and is not a directory", parent)})
	default:
		add(PreconditionCheck{StepID: s.ID(), Name: "filesystem:parent", OK: true, Detail: parent})
	}

	_, statErr := os.Stat(resolved)
	exists := statErr == nil
	switch s.Kind() {
	case StepCreate:
		if exists {
			add(PreconditionCheck{StepID: s.ID(), Name: "filesystem:clobber", OK: false, Fatal: false,
				Detail: fmt.Sprintf("target %s already exists; creation may overwrite it", resolved)})
		} else {
			add(PreconditionCheck{StepID: s.ID(), Name: "filesystem:free", OK: true, Detail: resolved})
		}
	case StepRead, StepDelete:
		if !exists {
			add(PreconditionCheck{StepID: s.ID(), Name: "filesystem:exists", OK: false, Fatal: false,
				Detail: fmt.Sprintf("target %s does not exist", resolved)})
		} else {
			add(PreconditionCheck{StepID: s.ID(), Name: "filesystem:exists", OK: true, Detail: resolved})
		}
	default: // modify
		if !exists {
			add(PreconditionCheck{StepID: s.ID(), Name: "filesystem:exists", OK: false, Fatal: false,
				Detail: fmt.Sprintf("target %s does not exist; modify will behave like create", resolved)})
		} else {
			add(PreconditionCheck{StepID: s.ID(), Name: "filesystem:exists", OK: true, Detail: resolved})
		}
	}
}

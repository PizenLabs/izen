package plan

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/domain/task"
)

// canonicalType maps a microkernel StepKind onto the canonical task.TaskType.
// The second return value is false for step kinds that have no canonical task
// representation (read steps carry no work for an executor and are excluded
// from canonical projections).
func canonicalType(k StepKind) (task.TaskType, bool) {
	switch k {
	case StepCreate:
		return task.TaskFileMutate, true
	case StepModify:
		return task.TaskFileEdit, true
	case StepDelete:
		return task.TaskFileMutate, true
	case StepRun:
		return task.TaskShellExec, true
	case StepVerify:
		return task.TaskVerify, true
	default:
		// StepRead and unknown kinds: no canonical task.
		return "", false
	}
}

// Canonical projects the step onto the canonical task.Task model. The second
// return value is false for steps without a canonical task representation
// (read steps). Steps are hardcoded: they are deterministic microkernel
// outputs that must survive evidence-based filters.
func (s Step) Canonical() (task.Task, bool) {
	typ, ok := canonicalType(s.kind)
	if !ok {
		return task.Task{}, false
	}
	return task.Task{
		ID:          s.id,
		Type:        typ,
		Status:      task.StatusIdle,
		Target:      s.target,
		Description: fmt.Sprintf("%s %s", s.kind, s.target),
		Rationale:   s.reason,
		IsHardcoded: true,
	}, true
}

// Canonical projects the immutable LogicalPlan onto the canonical task.Plan
// model, ordering the executable steps by their dependency chain. Read steps
// are excluded: they carry no work for an executor.
func (p *LogicalPlan) Canonical() *task.Plan {
	ordered, err := p.topologicalSteps()
	if err != nil {
		// A validated logical plan is acyclic; fall back to declaration order.
		ordered = p.steps
	}
	tasks := make([]task.Task, 0, len(ordered))
	for _, s := range ordered {
		if t, ok := s.Canonical(); ok {
			t.StepNum = len(tasks) + 1
			tasks = append(tasks, t)
		}
	}
	return task.NewPlan(tasks, false, p.summary)
}

// Canonical projects the immutable ExecutablePlan onto the canonical task.Plan
// model. Every lowered step maps to a canonical task in execution order.
func (p *ExecutablePlan) Canonical() *task.Plan {
	steps := p.Steps()
	tasks := make([]task.Task, 0, len(steps))
	for _, es := range steps {
		if t, ok := es.Step().Canonical(); ok {
			t.StepNum = len(tasks) + 1
			tasks = append(tasks, t)
		}
	}
	return task.NewPlan(tasks, false, fmt.Sprintf("executable: %d step(s)", len(tasks)))
}

// topologicalSteps returns the plan's steps ordered so that every step appears
// after its dependencies, or the declaration order when the graph is cyclic.
func (p *LogicalPlan) topologicalSteps() ([]Step, error) {
	byID := make(map[string]Step, len(p.steps))
	for _, s := range p.steps {
		byID[s.ID()] = s
	}
	indeg := make(map[string]int, len(p.steps))
	for _, s := range p.steps {
		for _, d := range s.DependsOn() {
			indeg[s.ID()]++
			_ = d
		}
	}
	var out []Step
	queue := make([]Step, 0, len(p.steps))
	for _, s := range p.steps {
		if indeg[s.ID()] == 0 {
			queue = append(queue, s)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		out = append(out, cur)
		for _, s := range p.steps {
			if s.Contains(cur.ID()) {
				indeg[s.ID()]--
				if indeg[s.ID()] == 0 {
					queue = append(queue, s)
				}
			}
		}
	}
	if len(out) != len(p.steps) {
		return nil, fmt.Errorf("plan: cyclic dependency graph")
	}
	return out, nil
}

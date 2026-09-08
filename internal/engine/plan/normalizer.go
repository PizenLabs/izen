package plan

import (
	"errors"
	"fmt"
	"strings"
)

// Stage errors shared across the plan pipeline.
var (
	// ErrNilPlan is returned when a stage receives a nil artifact.
	ErrNilPlan = errors.New("plan: nil plan artifact")
	// ErrEmptyPlan is returned when a stage receives a plan with no steps.
	ErrEmptyPlan = errors.New("plan: empty plan")
	// ErrDependencyCycle is returned when steps cannot be ordered because
	// their dependencies form a cycle.
	ErrDependencyCycle = errors.New("plan: dependency cycle")
	// ErrInvalidPlan is returned when a stage requires a validated plan.
	ErrInvalidPlan = errors.New("plan: plan failed validation")
)

// canonicalActions maps alternative action verbs onto their canonical form.
var canonicalActions = map[string]string{
	// create
	"create": "create", "add": "create", "new": "create", "scaffold": "create", "generate": "create",
	// modify
	"modify": "modify", "edit": "modify", "update": "modify", "change": "modify", "patch": "modify", "write": "modify",
	// delete
	"delete": "delete", "remove": "delete", "rm": "delete",
	// read
	"read": "read", "get": "read", "inspect": "read", "view": "read", "open": "read",
	// run
	"run": "run", "execute": "run", "invoke": "run",
	// verify
	"verify": "verify", "test": "verify", "check": "verify", "validate": "verify",
}

// canonicalAction returns the canonical verb for a step. The step kind is
// the ultimate fallback, so every step always yields a canonical action.
func canonicalAction(s Step) string {
	raw := strings.ToLower(strings.TrimSpace(s.Action()))
	if raw == "" {
		return string(s.Kind())
	}
	if c, ok := canonicalActions[raw]; ok {
		return c
	}
	return raw
}

// PlanNormalizer deduplicates and standardizes a LogicalPlan into an
// immutable NormalizedPlan. It never mutates its input: every returned step
// is a freshly constructed value.
type PlanNormalizer struct{}

// NewPlanNormalizer returns a normalizer.
func NewPlanNormalizer() *PlanNormalizer { return &PlanNormalizer{} }

// Normalize transforms the input LogicalPlan into a NormalizedPlan by
// (1) standardizing action verbs, (2) collapsing semantically duplicate
// steps, (3) topologically ordering by dependencies, and (4) renumbering
// step ids deterministically.
func (n *PlanNormalizer) Normalize(in *LogicalPlan) (*NormalizedPlan, error) {
	if in == nil {
		return nil, fmt.Errorf("%w: logical", ErrNilPlan)
	}
	input := in.Steps()
	if len(input) == 0 {
		return nil, fmt.Errorf("%w: logical", ErrEmptyPlan)
	}

	// ── Step 1+2: standardize and dedupe ────────────────────────────────
	seenContent := make(map[string]bool, len(input))
	seenID := make(map[string]bool, len(input))
	var kept []Step
	standardized := 0
	for _, s := range input {
		canon := canonicalAction(s)
		original := strings.ToLower(strings.TrimSpace(s.Action()))
		if original != "" && original != canon {
			standardized++
		}
		contentKey := string(s.Kind()) + "|" + canon + "|" + s.Target()
		if seenContent[contentKey] || seenID[s.ID()] {
			continue
		}
		seenContent[contentKey] = true
		seenID[s.ID()] = true
		kept = append(kept, NewStep(
			s.Kind(), s.Target(),
			WithID(s.ID()),
			WithAction(canon),
			WithDependencies(s.DependsOn()...),
			WithReason(s.Reason()),
		))
	}
	deduped := len(input) - len(kept)

	// ── Step 3: topological order by dependencies ───────────────────────
	ordered, err := topoSort(kept)
	if err != nil {
		return nil, err
	}

	// ── Step 4: deterministic renumbering + dep remap ───────────────────
	oldToNew := make(map[string]string, len(ordered))
	for i, s := range ordered {
		oldToNew[s.ID()] = fmt.Sprintf("s%d", i+1)
	}
	final := make([]Step, 0, len(ordered))
	for _, s := range ordered {
		var deps []string
		for _, d := range s.DependsOn() {
			if nd, ok := oldToNew[d]; ok {
				deps = append(deps, nd)
			}
		}
		final = append(final, s.WithIDDerived(oldToNew[s.ID()]).WithDependenciesDerived(deps))
	}

	return NewNormalizedPlan(in.Goal(), final, NormalizeMetrics{
		InputCount:   len(input),
		Deduped:      deduped,
		Standardized: standardized,
		Reordered:    !sameStepOrder(kept, ordered),
	}), nil
}

// topoSort orders steps so that every step appears after the steps it
// depends on. Missing dependency references (dangling ids) are ignored here;
// the PlanValidator surfaces them. A dependency cycle is an error.
func topoSort(steps []Step) ([]Step, error) {
	idx := make(map[string]int, len(steps))
	for i, s := range steps {
		idx[s.ID()] = i
	}
	indeg := make([]int, len(steps))
	for i, s := range steps {
		for _, d := range s.DependsOn() {
			if j, ok := idx[d]; ok && j != i {
				indeg[i]++
			}
		}
	}
	done := make([]bool, len(steps))
	out := make([]Step, 0, len(steps))
	for len(out) < len(steps) {
		pick := -1
		for i := range steps {
			if indeg[i] == 0 && !done[i] {
				pick = i
				break
			}
		}
		if pick < 0 {
			return nil, ErrDependencyCycle
		}
		done[pick] = true
		out = append(out, steps[pick])
		for j := range steps {
			if done[j] {
				continue
			}
			for _, d := range steps[j].DependsOn() {
				if k, ok := idx[d]; ok && k == pick {
					indeg[j]--
				}
			}
		}
	}
	return out, nil
}

// sameStepOrder reports whether two step slices carry the same id sequence.
func sameStepOrder(a, b []Step) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID() != b[i].ID() {
			return false
		}
	}
	return true
}

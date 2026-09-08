package plan

import "fmt"

// PlanValidator validates the internal logic and schema of a NormalizedPlan
// and returns an immutable ValidatedPlan carrying one verdict per check.
// The input is never mutated. A plan whose checks all pass reports
// Valid()==true; the PolicyEngine refuses to evaluate anything else.
type PlanValidator struct{}

// NewPlanValidator returns a validator.
func NewPlanValidator() *PlanValidator { return &PlanValidator{} }

// Validate inspects a NormalizedPlan and returns a ValidatedPlan. The
// boolean on the returned artifact reports whether every schema and logic
// check passed. An error is returned only for a nil input; an invalid plan
// is delivered as an artifact with Valid()==false so callers can inspect the
// individual verdicts.
func (v *PlanValidator) Validate(in *NormalizedPlan) (*ValidatedPlan, error) {
	if in == nil {
		return nil, fmt.Errorf("%w: normalized", ErrNilPlan)
	}
	steps := in.Steps()

	var results []ValidationResult
	valid := true

	// ── Schema checks ───────────────────────────────────────────────────
	if len(steps) == 0 {
		results = append(results, ValidationResult{
			Subject: "plan", Rule: "schema:nonempty", OK: false,
			Detail: "plan contains no steps",
		})
		valid = false
	}

	seenIDs := make(map[string]bool, len(steps))
	seenContent := make(map[string]bool, len(steps))
	for _, s := range steps {
		subject := s.ID()
		if subject == "" {
			subject = "(unnamed)"
		}
		ok := s.Kind().Valid()
		results = append(results, ValidationResult{
			Subject: subject, Rule: "schema:kind", OK: ok,
			Detail: fmt.Sprintf("kind %q %s", s.Kind(), verdict(ok)),
		})
		valid = valid && ok

		ok = s.Target() != ""
		results = append(results, ValidationResult{
			Subject: subject, Rule: "schema:target", OK: ok,
			Detail: "target must not be empty" + verdictLine(ok),
		})
		valid = valid && ok

		ok = s.Action() != ""
		results = append(results, ValidationResult{
			Subject: subject, Rule: "schema:action", OK: ok,
			Detail: "action must not be empty" + verdictLine(ok),
		})
		valid = valid && ok

		if seenIDs[s.ID()] {
			results = append(results, ValidationResult{
				Subject: subject, Rule: "schema:unique_ids", OK: false,
				Detail: "duplicate step id " + s.ID(),
			})
			valid = false
		}
		seenIDs[s.ID()] = true

		contentKey := string(s.Kind()) + "|" + s.Action() + "|" + s.Target()
		if seenContent[contentKey] {
			results = append(results, ValidationResult{
				Subject: subject, Rule: "logic:duplicate", OK: false,
				Detail: "duplicate step " + contentKey,
			})
			valid = false
		}
		seenContent[contentKey] = true
	}

	// ── Internal-logic checks ───────────────────────────────────────────
	idx := make(map[string]int, len(steps))
	for i, s := range steps {
		idx[s.ID()] = i
	}
	for _, s := range steps {
		for _, d := range s.DependsOn() {
			if d == s.ID() {
				results = append(results, ValidationResult{
					Subject: s.ID(), Rule: "logic:self_dependency", OK: false,
					Detail: "step depends on itself",
				})
				valid = false
				continue
			}
			if _, ok := idx[d]; !ok {
				results = append(results, ValidationResult{
					Subject: s.ID(), Rule: "logic:dangling_dependency", OK: false,
					Detail: "step depends on unknown id " + d,
				})
				valid = false
			}
		}
	}
	if !acyclic(steps, idx) {
		results = append(results, ValidationResult{
			Subject: "plan", Rule: "logic:acyclic", OK: false,
			Detail: "step dependencies form a cycle",
		})
		valid = false
	}

	return NewValidatedPlan(in.Goal(), steps, results, valid), nil
}

// acyclic reports whether the step dependency graph contains no cycle.
func acyclic(steps []Step, idx map[string]int) bool {
	state := make([]int, len(steps)) // 0=unvisited 1=visiting 2=done
	var visit func(i int) bool
	visit = func(i int) bool {
		switch state[i] {
		case 1:
			return false // back edge -> cycle
		case 2:
			return true
		}
		state[i] = 1
		for _, d := range steps[i].DependsOn() {
			if j, ok := idx[d]; ok && !visit(j) {
				return false
			}
		}
		state[i] = 2
		return true
	}
	for i := range steps {
		if !visit(i) {
			return false
		}
	}
	return true
}

func verdict(ok bool) string {
	if ok {
		return "is valid"
	}
	return "is invalid"
}

func verdictLine(ok bool) string {
	if ok {
		return ""
	}
	return " (failed)"
}

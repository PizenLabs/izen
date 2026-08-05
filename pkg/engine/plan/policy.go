package plan

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Policy is one static, auditable policy statement evaluated by the
// PolicyEngine. It replaces the legacy capability matrix: instead of a
// granted/denied bit set, each policy expresses permitted path bounds,
// action permissions and security limits, and every violation is recorded
// with the exact rule and target that triggered it.
type Policy struct {
	// ID is the stable policy identifier, e.g. "default-workspace".
	ID string
	// Description is a human-readable statement of what the policy enforces.
	Description string
	// AllowedRoots bounds every file-target step to one of these filesystem
	// roots. Empty means unbounded (not recommended).
	AllowedRoots []string
	// AllowedActions restricts the step kinds a plan may carry. Empty means
	// every kind is permitted.
	AllowedActions []StepKind
	// DeniedGlobs forbids file targets matching any glob (e.g. ".env").
	DeniedGlobs []string
	// DeniedPatterns forbids file targets matching any regular expression.
	DeniedPatterns []string
	// MaxSteps caps the number of steps in a plan. 0 disables the limit.
	MaxSteps int
	// MaxTargets caps the number of distinct file targets in a plan. 0
	// disables the limit.
	MaxTargets int
	// ForbidShell rejects any run/verify step that executes a command.
	ForbidShell bool
}

// Violation records one policy breach with the exact rule and target.
type Violation struct {
	// PolicyID is the policy that was breached.
	PolicyID string
	// Rule is the violated rule (e.g. "permitted_path").
	Rule string
	// Target is the offending step target, or "" for plan-wide limits.
	Target string
	// Reason is a human-readable explanation.
	Reason string
}

// PolicyDecision is the immutable verdict of a policy evaluation.
type PolicyDecision struct {
	approved   bool
	violations []Violation
	reasons    []string
}

// Approved reports whether every policy passed.
func (d PolicyDecision) Approved() bool { return d.approved }

// Violations returns a defensive copy of the recorded violations.
func (d PolicyDecision) Violations() []Violation {
	return append([]Violation(nil), d.violations...)
}

// Reasons returns the human-readable summary lines.
func (d PolicyDecision) Reasons() []string {
	return append([]string(nil), d.reasons...)
}

// Summary renders the decision as compact log lines.
func (d PolicyDecision) Summary() []string {
	lines := append([]string(nil), d.reasons...)
	for _, v := range d.violations {
		lines = append(lines, fmt.Sprintf("policy: deny %s/%s on %q — %s", v.PolicyID, v.Rule, v.Target, v.Reason))
	}
	if d.approved {
		lines = append(lines, "policy: approved")
	} else {
		lines = append(lines, "policy: NOT approved")
	}
	return lines
}

// PolicyEngine evaluates static policies against a ValidatedPlan. It is
// immutable after construction and safe for concurrent use. Deny always
// wins: a single violation from any policy rejects the whole plan.
type PolicyEngine struct {
	policies []Policy
}

// NewPolicyEngine returns an engine over the given policies. Policies
// without an ID are ignored.
func NewPolicyEngine(policies ...Policy) *PolicyEngine {
	filtered := make([]Policy, 0, len(policies))
	for _, p := range policies {
		if p.ID != "" {
			filtered = append(filtered, p)
		}
	}
	return &PolicyEngine{policies: filtered}
}

// Policies returns a defensive copy of the configured policies.
func (e *PolicyEngine) Policies() []Policy {
	return append([]Policy(nil), e.policies...)
}

// Evaluate checks a ValidatedPlan against every policy. The evaluation is a
// pure function of the plan and never mutates it.
func (e *PolicyEngine) Evaluate(in *ValidatedPlan) PolicyDecision {
	if in == nil {
		return PolicyDecision{
			approved: false,
			violations: []Violation{{
				PolicyID: "engine", Rule: "plan:not_validated",
				Reason: "nil plan supplied to policy engine",
			}},
			reasons: []string{"policy: input plan is nil"},
		}
	}
	if !in.Valid() {
		return PolicyDecision{
			approved: false,
			violations: []Violation{{
				PolicyID: "engine", Rule: "plan:invalid",
				Reason: "plan failed validation and cannot be policy-approved",
			}},
			reasons: []string{"policy: plan is not validated"},
		}
	}

	steps := in.Steps()
	var violations []Violation
	reasons := make([]string, 0, len(e.policies))

	for _, p := range e.policies {
		applied := false
		for _, s := range steps {
			kindOK := len(p.AllowedActions) == 0 || containsKind(p.AllowedActions, s.Kind())
			if !kindOK {
				applied = true
				violations = append(violations, Violation{
					PolicyID: p.ID, Rule: "action_permission", Target: s.Target(),
					Reason: fmt.Sprintf("kind %s not permitted by policy", s.Kind()),
				})
			}

			if s.Kind().FileTarget() {
				if !p.AllowedRootsAllows(s.Target()) {
					applied = true
					violations = append(violations, Violation{
						PolicyID: p.ID, Rule: "permitted_path", Target: s.Target(),
						Reason: "target outside every permitted root",
					})
				}
				if glob, bad := p.MatchesDeniedGlob(s.Target()); bad {
					applied = true
					violations = append(violations, Violation{
						PolicyID: p.ID, Rule: "forbidden_glob", Target: s.Target(),
						Reason: "target matches forbidden glob " + glob,
					})
				}
				if re, bad := p.MatchesDeniedPattern(s.Target()); bad {
					applied = true
					violations = append(violations, Violation{
						PolicyID: p.ID, Rule: "forbidden_pattern", Target: s.Target(),
						Reason: "target matches forbidden pattern " + re,
					})
				}
			}

			if p.ForbidShell && (s.Kind() == StepRun || s.Kind() == StepVerify) {
				applied = true
				violations = append(violations, Violation{
					PolicyID: p.ID, Rule: "forbid_shell", Target: s.Target(),
					Reason: "policy forbids shell execution",
				})
			}
		}

		if p.MaxSteps > 0 && len(steps) > p.MaxSteps {
			applied = true
			violations = append(violations, Violation{
				PolicyID: p.ID, Rule: "max_steps", Target: "",
				Reason: fmt.Sprintf("plan has %d steps, limit is %d", len(steps), p.MaxSteps),
			})
		}
		if p.MaxTargets > 0 {
			if n := distinctFileTargets(steps); n > p.MaxTargets {
				applied = true
				violations = append(violations, Violation{
					PolicyID: p.ID, Rule: "max_targets", Target: "",
					Reason: fmt.Sprintf("plan touches %d distinct files, limit is %d", n, p.MaxTargets),
				})
			}
		}

		if applied {
			reasons = append(reasons, fmt.Sprintf("policy: %s applied — %s", p.ID, p.Description))
		}
	}

	return PolicyDecision{
		approved:   len(violations) == 0,
		violations: violations,
		reasons:    reasons,
	}
}

// AllowedRootsAllows reports whether target lies within every configured
// root. An empty root set allows everything.
func (p Policy) AllowedRootsAllows(target string) bool {
	if len(p.AllowedRoots) == 0 {
		return true
	}
	for _, root := range p.AllowedRoots {
		if withinRoot(target, root) {
			return true
		}
	}
	return false
}

// MatchesDeniedGlob reports whether target matches a forbidden glob.
func (p Policy) MatchesDeniedGlob(target string) (string, bool) {
	for _, g := range p.DeniedGlobs {
		if matched, _ := filepath.Match(g, target); matched {
			return g, true
		}
	}
	return "", false
}

// MatchesDeniedPattern reports whether target matches a forbidden regex.
func (p Policy) MatchesDeniedPattern(target string) (string, bool) {
	for _, re := range p.DeniedPatterns {
		if r, err := regexp.Compile(re); err == nil && r.MatchString(target) {
			return re, true
		}
	}
	return "", false
}

// withinRoot reports whether target resolves inside root, guarding against
// traversal via "..". Relative targets are resolved against the root, so an
// abstract pre-lowering plan (relative targets) and a physical plan
// (absolute targets) can both be evaluated against the same bound.
func withinRoot(target, root string) bool {
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return false
	}
	var resolved string
	if filepath.IsAbs(target) {
		resolved = filepath.Clean(target)
	} else {
		resolved = filepath.Join(absRoot, filepath.FromSlash(target))
	}
	rel, err := filepath.Rel(absRoot, resolved)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func containsKind(kinds []StepKind, k StepKind) bool {
	for _, x := range kinds {
		if x == k {
			return true
		}
	}
	return false
}

func distinctFileTargets(steps []Step) int {
	seen := make(map[string]bool)
	n := 0
	for _, s := range steps {
		if s.Kind().FileTarget() && !seen[s.Target()] {
			seen[s.Target()] = true
			n++
		}
	}
	return n
}

// NewDefaultPolicy returns a permissive-but-bounded policy rooted at
// workspace: every file target must live under the workspace, run/verify
// steps are permitted, and the plan is capped at 64 steps.
func NewDefaultPolicy(workspace string) Policy {
	return Policy{
		ID:           "default-workspace",
		Description:  "all file targets must live under the workspace root",
		AllowedRoots: []string{workspace},
		MaxSteps:     64,
	}
}

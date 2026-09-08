// Package plan implements the microkernel's immutable plan pipeline. It
// transforms a strategy Goal through four immutable artifacts:
//
//	Goal          (pkg/engine/strategy)  - what success looks like
//	LogicalPlan                          - the abstract plan derived from the Goal
//	NormalizedPlan                       - deduplicated + standardized steps
//	ValidatedPlan                        - internally consistent, schema-clean
//	ExecutablePlan                       - physical, lowered, ready to run
//
// Every stage takes a read-only input artifact and returns a new artifact;
// no plan is ever mutated in place. PolicyEngine replaces the legacy
// capability matrix with static, auditable policy evaluation, and
// ExecutionPreconditions performs real-world filesystem and environment
// checks on the executable plan before any work begins.
package plan

import "io/fs"

// StepKind classifies one atomic unit of work in a plan.
type StepKind string

const (
	// StepCreate creates a new file from scratch.
	StepCreate StepKind = "create"
	// StepModify edits an existing file in place.
	StepModify StepKind = "modify"
	// StepDelete removes a file.
	StepDelete StepKind = "delete"
	// StepRead inspects a file without modifying it.
	StepRead StepKind = "read"
	// StepRun executes a shell command.
	StepRun StepKind = "run"
	// StepVerify checks that the plan's outcome holds.
	StepVerify StepKind = "verify"
)

// Valid reports whether k is one of the defined step kinds.
func (k StepKind) Valid() bool {
	switch k {
	case StepCreate, StepModify, StepDelete, StepRead, StepRun, StepVerify:
		return true
	default:
		return false
	}
}

// Mutation reports whether the kind modifies the filesystem.
func (k StepKind) Mutation() bool {
	switch k {
	case StepCreate, StepModify, StepDelete, StepRun:
		return true
	default:
		return false
	}
}

// FileTarget reports whether the kind addresses a filesystem path rather
// than a shell command.
func (k StepKind) FileTarget() bool {
	switch k {
	case StepCreate, StepModify, StepDelete, StepRead:
		return true
	default:
		return false
	}
}

// String returns the machine-readable kind label.
func (k StepKind) String() string { return string(k) }

// Step is one atomic, immutable unit of work in a plan. Steps are value
// types: every field is unexported and every deriving method returns a new
// Step, so a Step cannot change after construction.
type Step struct {
	id      string
	kind    StepKind
	action  string
	target  string
	depends []string
	reason  string
}

// StepOption configures a Step during construction.
type StepOption func(*stepBuilder)

type stepBuilder struct {
	id      string
	action  string
	depends []string
	reason  string
}

// WithAction sets the canonical action verb of the step.
func WithAction(action string) StepOption {
	return func(b *stepBuilder) { b.action = action }
}

// WithDependencies lists the step ids this step depends on.
func WithDependencies(ids ...string) StepOption {
	return func(b *stepBuilder) { b.depends = append(b.depends, ids...) }
}

// WithReason documents why the step exists.
func WithReason(reason string) StepOption {
	return func(b *stepBuilder) { b.reason = reason }
}

// WithID assigns an explicit step identifier.
func WithID(id string) StepOption {
	return func(b *stepBuilder) { b.id = id }
}

// NewStep constructs an immutable Step of the given kind targeting path or
// resource target.
func NewStep(kind StepKind, target string, opts ...StepOption) Step {
	b := &stepBuilder{action: string(kind)}
	for _, o := range opts {
		o(b)
	}
	return Step{
		id:      b.id,
		kind:    kind,
		action:  b.action,
		target:  target,
		depends: append([]string(nil), b.depends...),
		reason:  b.reason,
	}
}

// ID returns the step identifier.
func (s Step) ID() string { return s.id }

// Kind returns the step kind.
func (s Step) Kind() StepKind { return s.kind }

// Action returns the canonical action verb.
func (s Step) Action() string { return s.action }

// Target returns the file path or command the step addresses.
func (s Step) Target() string { return s.target }

// DependsOn returns the ids of the steps this step depends on.
func (s Step) DependsOn() []string {
	return append([]string(nil), s.depends...)
}

// Reason returns why the step exists.
func (s Step) Reason() string { return s.reason }

// WithIDDerived returns a new Step carrying the given id. The receiver is
// left unchanged. It is used by stages that renumber steps.
func (s Step) WithIDDerived(id string) Step {
	s.id = id
	return s
}

// WithDependenciesDerived returns a new Step carrying the given dependency
// ids. The receiver is left unchanged.
func (s Step) WithDependenciesDerived(deps []string) Step {
	s.depends = append([]string(nil), deps...)
	return s
}

// Equal reports whether two steps are semantically identical: same kind,
// canonical action and target. IDs, dependencies and reasons are ignored.
func (s Step) Equal(o Step) bool {
	return s.kind == o.kind && s.action == o.action && s.target == o.target
}

// Contains reports whether the step carries any of the given dependency ids.
func (s Step) Contains(depID string) bool {
	for _, d := range s.depends {
		if d == depID {
			return true
		}
	}
	return false
}

// defaultMode is the file mode applied to lowered create/modify steps.
const defaultMode fs.FileMode = 0o644

package layer4

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/engine/layer1"
)

// Sentinel errors returned by the plan resolver.
var (
	// ErrNoCapabilities is returned when the resolver has no capability
	// surface to plan against.
	ErrNoCapabilities = errors.New("layer4: no workspace capability surface")
	// ErrEmptyPlan is returned when a plan would contain no stages at all.
	ErrEmptyPlan = errors.New("layer4: empty validation plan")
)

// Stage identifies a validation stage in the DAG.
type Stage string

const (
	// StageStructural performs RAM-only, SoR-based structural analysis of the
	// proposed mutations: broken imports, dangling references and in-memory
	// syntax checks. It is the cheapest stage and always runs.
	StageStructural Stage = "structural"
	// StageSyntax parses the proposed sources in memory. It is cheap and
	// always runs.
	StageSyntax Stage = "syntax"
	// StageLint runs a workspace lint command when the workspace exposes the
	// lint capability.
	StageLint Stage = "lint"
	// StageBuild runs a workspace build command when the workspace exposes
	// the build capability.
	StageBuild Stage = "build"
	// StageTest runs a workspace test command when the workspace exposes the
	// test capability.
	StageTest Stage = "test"
)

// String returns the machine-readable stage label.
func (s Stage) String() string { return string(s) }

// Cheap reports whether the stage is a low-cost, in-RAM check that never
// shells out to the workspace toolchain.
func (s Stage) Cheap() bool {
	switch s {
	case StageStructural, StageSyntax:
		return true
	default:
		return false
	}
}

// Cost returns the relative execution cost of the stage, used to order
// stages cheap-first. Higher values are more expensive.
func (s Stage) Cost() int {
	switch s {
	case StageStructural:
		return 1
	case StageSyntax:
		return 2
	case StageLint:
		return 3
	case StageBuild:
		return 4
	case StageTest:
		return 5
	default:
		return 0
	}
}

// Step is a single planned validation stage.
type Step struct {
	// Stage is the validation stage to run.
	Stage Stage
	// ID is the stable node identifier used in the validation DAG.
	ID string
}

// ValidationPlan is the ordered, capability-driven validation plan for a
// workspace. Stages appear cheapest-first. The structural and syntax stages
// are always present because they are free in-RAM checks; lint, build and
// test stages are included only when the Layer 1 capability graph exposes the
// matching capability.
type ValidationPlan struct {
	// Steps lists the planned stages in execution order.
	Steps []Step
	// Stack is the detected workspace stack, when known.
	Stack layer1.Stack
}

// Len returns the number of planned stages.
func (p *ValidationPlan) Len() int {
	if p == nil {
		return 0
	}
	return len(p.Steps)
}

// HasStage reports whether the plan includes the given stage.
func (p *ValidationPlan) HasStage(s Stage) bool {
	if p == nil {
		return false
	}
	for _, st := range p.Steps {
		if st.Stage == s {
			return true
		}
	}
	return false
}

// Stages returns the planned stages in order.
func (p *ValidationPlan) Stages() []Stage {
	if p == nil {
		return nil
	}
	out := make([]Stage, len(p.Steps))
	for i, st := range p.Steps {
		out[i] = st.Stage
	}
	return out
}

// String renders the plan as a compact machine-readable label.
func (p *ValidationPlan) String() string {
	if p == nil {
		return ""
	}
	stages := make([]string, len(p.Steps))
	for i, st := range p.Steps {
		stages[i] = string(st.Stage)
	}
	return strings.Join(stages, " -> ")
}

// CapabilityReader is the read-only capability surface of a workspace. It is
// satisfied by *layer1.Graph.
type CapabilityReader interface {
	// Supports reports whether the workspace exposes the capability.
	Supports(cap layer1.Capability) bool
	// Resolve returns the concrete command for the capability.
	Resolve(cap layer1.Capability) (string, bool)
}

// Resolver dynamically builds ValidationPlans from the Layer 1 capability
// graph. It is immutable after construction and safe for concurrent use.
type Resolver struct {
	caps  CapabilityReader
	stack layer1.Stack
}

// Option configures a Resolver.
type Option func(*Resolver)

// WithStack overrides the stack label reported in a plan. The stack is
// informational only and never gates the presence of a stage; only the
// capability graph does.
func WithStack(s layer1.Stack) Option {
	return func(r *Resolver) { r.stack = s }
}

// NewResolver returns a resolver over the given capability surface. A nil
// surface yields a plan that contains only the free in-RAM stages.
func NewResolver(caps CapabilityReader, opts ...Option) *Resolver {
	r := &Resolver{caps: caps}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Supports reports whether the workspace exposes the capability.
func (r *Resolver) Supports(cap layer1.Capability) bool {
	return r.caps != nil && r.caps.Supports(cap)
}

// Plan resolves a ValidationPlan for the workspace. Build, test and lint
// stages are added only when the capability graph reports them; a workspace
// that cannot build or test never gets fabricated build or test stages. The
// plan always contains the structural and syntax stages.
func (r *Resolver) Plan() (*ValidationPlan, error) {
	if r.caps == nil {
		return nil, ErrNoCapabilities
	}
	plan := &ValidationPlan{Stack: r.stack}
	plan.Steps = append(plan.Steps,
		Step{Stage: StageStructural, ID: string(StageStructural)},
		Step{Stage: StageSyntax, ID: string(StageSyntax)},
	)
	for _, s := range []Stage{StageLint, StageBuild, StageTest} {
		if r.caps.Supports(capabilityOf(s)) {
			plan.Steps = append(plan.Steps, Step{Stage: s, ID: string(s)})
		}
	}
	if plan.Len() == 0 {
		return nil, ErrEmptyPlan
	}
	return plan, nil
}

// capabilityOf maps a validation stage to the Layer 1 capability that gates
// it. Stages without a backing capability return an invalid capability.
func capabilityOf(s Stage) layer1.Capability {
	switch s {
	case StageLint:
		return layer1.CapLint
	case StageBuild:
		return layer1.CapBuild
	case StageTest:
		return layer1.CapTest
	default:
		return ""
	}
}

// CommandFor returns the concrete workspace command for a stage's backing
// capability, when the workspace exposes it. An unsupported stage yields an
// empty command.
func (r *Resolver) CommandFor(s Stage) (string, bool) {
	cap := capabilityOf(s)
	if cap == "" || r.caps == nil {
		return "", false
	}
	return r.caps.Resolve(cap)
}

// BuildDAG wires the plan into an executable validation DAG. Every planned
// stage maps to a node whose validator is chosen from the supplied
// validatorFor hook; structural and syntax stages always receive a non-nil
// validator (the RAM-only defaults), while command-backed stages may be
// resolved by the caller. A stage without a validator yields ErrNoValidator.
//
// Dependency edges encode Cheap First, Expensive Last: the structural and
// syntax stages run concurrently, and every command-backed stage depends
// directly on all cheaper stages present in the plan. A structural or syntax
// failure therefore prevents the build and test nodes from ever starting.
func (r *Resolver) BuildDAG(validatorFor func(stage Stage) (Validator, error)) (*DAG, error) {
	plan, err := r.Plan()
	if err != nil {
		return nil, fmt.Errorf("layer4: resolve plan: %w", err)
	}
	if validatorFor == nil {
		return nil, ErrNoValidator
	}
	dag := New()
	for _, step := range plan.Steps {
		v, err := validatorFor(step.Stage)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, fmt.Errorf("%w: stage %s", ErrNoValidator, step.Stage)
		}
		if err := dag.AddNode(step.ID, step.Stage, v, dependenciesOf(step.Stage, plan)...); err != nil {
			return nil, err
		}
	}
	return dag, nil
}

// dependenciesOf returns the node ids a planned stage must wait for. The
// cheap in-RAM stages are roots; lint waits for both structural and syntax;
// build and test wait for every cheaper stage present in the plan, chaining
// through the cheapest available prerequisite when a capability is absent.
func dependenciesOf(stage Stage, plan *ValidationPlan) []string {
	switch stage {
	case StageLint:
		return []string{string(StageStructural), string(StageSyntax)}
	case StageBuild:
		return cheapPrerequisites(plan)
	case StageTest:
		if plan.HasStage(StageBuild) {
			return []string{string(StageBuild)}
		}
		return cheapPrerequisites(plan)
	default:
		return nil
	}
}

// cheapPrerequisites returns the in-RAM and lint stages present in the plan,
// the minimum gate every expensive stage must pass.
func cheapPrerequisites(plan *ValidationPlan) []string {
	deps := []string{string(StageStructural), string(StageSyntax)}
	if plan.HasStage(StageLint) {
		deps = append(deps, string(StageLint))
	}
	return deps
}

// Package op defines the Declarative Operation IR of the Izen Agent Runtime
// V3 (see op.go). policy.go extends that package with the Context Priority
// machinery that eliminates context-priority inversion at the prompt boundary:
//
//   - OperationSemantics is a strongly-typed, mutually-exclusive description of
//     what a user intent asks the runtime to do to a target.
//   - ContextPolicy is a strongly-typed execution policy (no boolean flag
//     combinations) that declares exactly how the LLM prompt context is
//     assembled for a given semantics.
//   - StrategyResolver implementations map OperationSemantics to ContextPolicy.
//     StrategyRegistry composes them extensionally (Open/Closed Principle):
//     new policies are added by Register-ing a resolver, never by editing a
//     switch statement.
//
// The package stays free of any AI/LLM prompt dependency: it only compiles
// the intent into the policy contract that outer layers (pkg/app) render.
package op

import (
	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/knowledge"
)

// OperationSemantics is a strongly-typed, mutually-exclusive description of
// what a user intent asks the runtime to do to a target. It is the input the
// StrategyResolver registry consumes to compile a ContextPolicy.
type OperationSemantics string

// Supported operation semantics.
const (
	// SemanticCreateProject synthesizes a brand-new target (greenfield). No
	// baseline workspace code exists yet or is authoritative.
	SemanticCreateProject OperationSemantics = "create_project"
	// SemanticRewriteProject performs a total replacement/redesign of an
	// existing target. The current workspace content is obsolete.
	SemanticRewriteProject OperationSemantics = "rewrite_project"
	// SemanticAddFeature adds a localized feature to an existing target,
	// preserving the surrounding structure.
	SemanticAddFeature OperationSemantics = "add_feature"
	// SemanticRefactor restructures existing code without changing external
	// behaviour.
	SemanticRefactor OperationSemantics = "refactor"
	// SemanticFixBug repairs a defect in existing workspace content.
	SemanticFixBug OperationSemantics = "fix_bug"
)

// String returns the machine-readable semantics label.
func (s OperationSemantics) String() string { return string(s) }

// SemanticsFromCategory maps the primary ir.Category classification onto the
// canonical OperationSemantics the policy registry consumes. It is the single
// translation seam between the Intent Compiler output and the strategy layer;
// unknown categories default to SemanticCreateProject.
func SemanticsFromCategory(c ir.Category) OperationSemantics {
	switch c {
	case ir.CategoryCreate:
		return SemanticCreateProject
	case ir.CategoryRedesign:
		return SemanticRewriteProject
	case ir.CategoryRefactor:
		return SemanticRefactor
	case ir.CategoryFixBug:
		return SemanticFixBug
	default:
		return SemanticCreateProject
	}
}

// ContextPolicy is a strongly-typed execution policy that declares exactly how
// the LLM prompt context is assembled for one intent. Every execution mode in
// the runtime derives strictly from a single ContextPolicy value — there are no
// boolean flag combinations to get wrong.
type ContextPolicy string

// Supported context policies.
const (
	// PolicyGenerate synthesizes brand-new artifacts (Greenfield). No baseline
	// code context is injected; the model generates purely from User Intent.
	PolicyGenerate ContextPolicy = "generate"
	// PolicyRewrite performs a total replacement/redesign. Target file paths
	// ONLY are injected and the obsolete file contents are stripped from the
	// LLM context. User Intent is the absolute Primary Source of Truth.
	PolicyRewrite ContextPolicy = "rewrite"
	// PolicyEdit performs a localized refactor or structural change. Baseline
	// code is injected together with explicit boundary markers so the model
	// preserves the surrounding structure.
	PolicyEdit ContextPolicy = "edit"
	// PolicyPatch performs an AST/Diff/Linter/Bug fix. Error traces and target
	// diff snippets are injected as the corrective context.
	PolicyPatch ContextPolicy = "patch"
)

// DefaultContextPolicy is the conservative fallback the StrategyRegistry
// returns when no registered resolver supports a semantics. It injects bounded
// baseline context (never strips content), so an unknown intent can never
// accidentally trigger a full rewrite.
const DefaultContextPolicy = PolicyEdit

// Valid reports whether p is one of the canonical context policies.
func (p ContextPolicy) Valid() bool {
	switch p {
	case PolicyGenerate, PolicyRewrite, PolicyEdit, PolicyPatch:
		return true
	default:
		return false
	}
}

// String returns the machine-readable context policy label.
func (p ContextPolicy) String() string { return string(p) }

// InjectsBaselineCode reports whether the policy feeds existing workspace code
// into the LLM prompt context. PolicyEdit and PolicyPatch do; PolicyGenerate
// and PolicyRewrite never do (generate starts blank, rewrite strips obsolete
// contents).
func (p ContextPolicy) InjectsBaselineCode() bool {
	return p == PolicyEdit || p == PolicyPatch
}

// StripsObsoleteContent reports whether the policy explicitly strips the
// obsolete file contents of the current workspace from the LLM prompt context
// (PolicyRewrite). When true, User Intent becomes the absolute primary source
// of truth.
func (p ContextPolicy) StripsObsoleteContent() bool {
	return p == PolicyRewrite
}

// InjectsPathsOnly reports whether the policy injects target file paths
// WITHOUT their contents (PolicyRewrite).
func (p ContextPolicy) InjectsPathsOnly() bool {
	return p == PolicyRewrite
}

// StrategyResolver maps a supported OperationSemantics onto a ContextPolicy. It
// is the extension point of the Open/Closed policy registry: a resolver owns
// both the predicate that declares which semantics it understands and the
// compiled policy it yields for them.
type StrategyResolver interface {
	// Supports reports whether the resolver understands the semantics.
	Supports(semantics OperationSemantics) bool
	// Resolve compiles the semantics into a ContextPolicy. It may consult the
	// shared RuntimeKnowledge graph but is not required to. The kg is a
	// pointer because the graph is a concurrency-safe handle: passing it by
	// value would copy the internal lock.
	Resolve(semantics OperationSemantics, kg *knowledge.KnowledgeGraph) ContextPolicy
}

// GenerateStrategyResolver maps greenfield creation semantics onto
// PolicyGenerate: brand-new artifacts, no baseline context.
type GenerateStrategyResolver struct{}

// Supports implements StrategyResolver.
func (GenerateStrategyResolver) Supports(semantics OperationSemantics) bool {
	return semantics == SemanticCreateProject
}

// Resolve implements StrategyResolver.
func (GenerateStrategyResolver) Resolve(semantics OperationSemantics, _ *knowledge.KnowledgeGraph) ContextPolicy {
	return PolicyGenerate
}

// RewriteStrategyResolver maps total-replacement/redesign semantics onto
// PolicyRewrite. It is reached by the redesign category through
// SemanticsFromCategory(ir.CategoryRedesign) == SemanticRewriteProject: the
// workspace is obsolete and User Intent is the absolute source of truth.
type RewriteStrategyResolver struct{}

// Supports implements StrategyResolver.
func (RewriteStrategyResolver) Supports(semantics OperationSemantics) bool {
	return semantics == SemanticRewriteProject
}

// Resolve implements StrategyResolver.
func (RewriteStrategyResolver) Resolve(semantics OperationSemantics, _ *knowledge.KnowledgeGraph) ContextPolicy {
	return PolicyRewrite
}

// EditStrategyResolver maps localized feature-addition and refactor semantics
// onto PolicyEdit: baseline code plus boundary markers.
type EditStrategyResolver struct{}

// Supports implements StrategyResolver.
func (EditStrategyResolver) Supports(semantics OperationSemantics) bool {
	return semantics == SemanticAddFeature || semantics == SemanticRefactor
}

// Resolve implements StrategyResolver.
func (EditStrategyResolver) Resolve(semantics OperationSemantics, _ *knowledge.KnowledgeGraph) ContextPolicy {
	return PolicyEdit
}

// PatchStrategyResolver maps defect-repair semantics onto PolicyPatch: error
// traces and target diff snippets are injected as corrective context.
type PatchStrategyResolver struct{}

// Supports implements StrategyResolver.
func (PatchStrategyResolver) Supports(semantics OperationSemantics) bool {
	return semantics == SemanticFixBug
}

// Resolve implements StrategyResolver.
func (PatchStrategyResolver) Resolve(semantics OperationSemantics, _ *knowledge.KnowledgeGraph) ContextPolicy {
	return PolicyPatch
}

// Compile-time assertions that every resolver satisfies StrategyResolver.
var (
	_ StrategyResolver = GenerateStrategyResolver{}
	_ StrategyResolver = RewriteStrategyResolver{}
	_ StrategyResolver = EditStrategyResolver{}
	_ StrategyResolver = PatchStrategyResolver{}
)

// StrategyRegistry is the extensible resolver registry. Resolution is first
// match wins in registration order, so registering a resolver for a semantics
// already claimed by an earlier resolver overrides it without modifying any
// resolver code (Open/Closed Principle). Semantics no resolver supports fall
// back to DefaultContextPolicy.
type StrategyRegistry struct {
	resolvers []StrategyResolver
}

// NewStrategyRegistry returns a registry pre-loaded with the four canonical
// resolvers in a deterministic, dependency-free order.
func NewStrategyRegistry() *StrategyRegistry {
	return &StrategyRegistry{resolvers: []StrategyResolver{
		GenerateStrategyResolver{},
		RewriteStrategyResolver{},
		EditStrategyResolver{},
		PatchStrategyResolver{},
	}}
}

// Register appends a resolver. A nil resolver is ignored so callers can
// conditionally register without branch noise.
func (r *StrategyRegistry) Register(resolver StrategyResolver) {
	if r == nil || resolver == nil {
		return
	}
	r.resolvers = append(r.resolvers, resolver)
}

// Resolvers returns a defensive copy of the registered resolvers in
// registration order.
func (r *StrategyRegistry) Resolvers() []StrategyResolver {
	if r == nil {
		return nil
	}
	return append([]StrategyResolver(nil), r.resolvers...)
}

// Resolve compiles semantics into a ContextPolicy. The first registered
// resolver whose Supports reports true decides; otherwise the conservative
// DefaultContextPolicy is returned. kg may be nil.
func (r *StrategyRegistry) Resolve(semantics OperationSemantics, kg *knowledge.KnowledgeGraph) ContextPolicy {
	if r == nil {
		return DefaultContextPolicy
	}
	for _, resolver := range r.resolvers {
		if resolver.Supports(semantics) {
			return resolver.Resolve(semantics, kg)
		}
	}
	return DefaultContextPolicy
}

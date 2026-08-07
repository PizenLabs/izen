package op

import (
	"testing"

	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/knowledge"
)

func TestContextPolicyValues(t *testing.T) {
	cases := []struct {
		policy ContextPolicy
		want   string
	}{
		{PolicyGenerate, "generate"},
		{PolicyRewrite, "rewrite"},
		{PolicyEdit, "edit"},
		{PolicyPatch, "patch"},
	}
	for _, c := range cases {
		if !c.policy.Valid() {
			t.Errorf("%q: expected valid", c.want)
		}
		if got := c.policy.String(); got != c.want {
			t.Errorf("%s.String() = %q, want %q", c.want, got, c.want)
		}
	}
	if ContextPolicy("bogus").Valid() {
		t.Error("expected unknown policy to be invalid")
	}
}

func TestContextPolicyExecutionSemantics(t *testing.T) {
	cases := []struct {
		policy         ContextPolicy
		injectBaseline bool
		stripsObsolete bool
		pathsOnly      bool
	}{
		{PolicyGenerate, false, false, false},
		{PolicyRewrite, false, true, true},
		{PolicyEdit, true, false, false},
		{PolicyPatch, true, false, false},
	}
	for _, c := range cases {
		if got := c.policy.InjectsBaselineCode(); got != c.injectBaseline {
			t.Errorf("%s InjectsBaselineCode = %v, want %v", c.policy, got, c.injectBaseline)
		}
		if got := c.policy.StripsObsoleteContent(); got != c.stripsObsolete {
			t.Errorf("%s StripsObsoleteContent = %v, want %v", c.policy, got, c.stripsObsolete)
		}
		if got := c.policy.InjectsPathsOnly(); got != c.pathsOnly {
			t.Errorf("%s InjectsPathsOnly = %v, want %v", c.policy, got, c.pathsOnly)
		}
	}
}

func TestSemanticsFromCategory(t *testing.T) {
	cases := []struct {
		category ir.Category
		want     OperationSemantics
	}{
		{ir.CategoryCreate, SemanticCreateProject},
		{ir.CategoryRedesign, SemanticRewriteProject},
		{ir.CategoryRefactor, SemanticRefactor},
		{ir.CategoryFixBug, SemanticFixBug},
		{ir.Category("unknown"), SemanticCreateProject},
	}
	for _, c := range cases {
		if got := SemanticsFromCategory(c.category); got != c.want {
			t.Errorf("SemanticsFromCategory(%s) = %s, want %s", c.category, got, c.want)
		}
	}
}

func TestResolversSupport(t *testing.T) {
	all := []OperationSemantics{
		SemanticCreateProject, SemanticRewriteProject,
		SemanticAddFeature, SemanticRefactor, SemanticFixBug,
	}
	cases := []struct {
		resolver  StrategyResolver
		supported []OperationSemantics
		policy    ContextPolicy
	}{
		{GenerateStrategyResolver{}, []OperationSemantics{SemanticCreateProject}, PolicyGenerate},
		{RewriteStrategyResolver{}, []OperationSemantics{SemanticRewriteProject}, PolicyRewrite},
		{EditStrategyResolver{}, []OperationSemantics{SemanticAddFeature, SemanticRefactor}, PolicyEdit},
		{PatchStrategyResolver{}, []OperationSemantics{SemanticFixBug}, PolicyPatch},
	}
	supported := func(resolver StrategyResolver) map[OperationSemantics]bool {
		m := make(map[OperationSemantics]bool)
		for _, s := range all {
			if resolver.Supports(s) {
				m[s] = true
			}
		}
		return m
	}
	for _, c := range cases {
		got := supported(c.resolver)
		for _, s := range c.supported {
			if !got[s] {
				t.Errorf("%T must support %s", c.resolver, s)
			}
		}
		for _, s := range all {
			want := false
			for _, ok := range c.supported {
				if s == ok {
					want = true
					break
				}
			}
			if got[s] != want {
				t.Errorf("%T supports(%s) = %v, want %v", c.resolver, s, got[s], want)
			}
			if got[s] && c.resolver.Resolve(s, nil) != c.policy {
				t.Errorf("%T.Resolve(%s) must yield %s", c.resolver, s, c.policy)
			}
		}
	}
}

func TestStrategyRegistryResolvesAllSemantics(t *testing.T) {
	registry := NewStrategyRegistry()
	cases := []struct {
		semantics OperationSemantics
		want      ContextPolicy
	}{
		{SemanticCreateProject, PolicyGenerate},
		{SemanticRewriteProject, PolicyRewrite},
		{SemanticAddFeature, PolicyEdit},
		{SemanticRefactor, PolicyEdit},
		{SemanticFixBug, PolicyPatch},
		{OperationSemantics("unknown"), DefaultContextPolicy},
	}
	for _, c := range cases {
		if got := registry.Resolve(c.semantics, nil); got != c.want {
			t.Errorf("Resolve(%s) = %s, want %s", c.semantics, got, c.want)
		}
	}
}

// TestStrategyRegistryOpenClosedProves an unresolvable semantics can be served
// by Register-ing a new resolver without touching any existing resolver code:
// the registry stays open for extension and closed for modification.
func TestStrategyRegistryOpenClosedProvesExtension(t *testing.T) {
	registry := NewStrategyRegistry()
	semantics := OperationSemantics("semantic_terraform")

	if got := registry.Resolve(semantics, nil); got != DefaultContextPolicy {
		t.Fatalf("Resolve(%s) = %s, want default before registration", semantics, got)
	}

	registry.Register(resolverFunc{
		supports: func(s OperationSemantics) bool { return s == semantics },
		policy:   PolicyGenerate,
	})

	if got := registry.Resolve(semantics, nil); got != PolicyGenerate {
		t.Fatalf("Resolve(%s) after Register = %s, want generate", semantics, got)
	}
	if got := registry.Resolve(SemanticFixBug, nil); got != PolicyPatch {
		t.Fatalf("existing resolution must be unaffected, got %s", got)
	}
}

// TestStrategyRegistryFirstMatchWins proves registration order decides when
// two resolvers claim the same semantics: the first registered resolver wins.
func TestStrategyRegistryFirstMatchWins(t *testing.T) {
	registry := &StrategyRegistry{}
	registry.Register(resolverFunc{
		supports: func(s OperationSemantics) bool { return s == SemanticFixBug },
		policy:   PolicyRewrite,
	})
	registry.Register(resolverFunc{
		supports: func(s OperationSemantics) bool { return s == SemanticFixBug },
		policy:   PolicyPatch,
	})

	if got := registry.Resolve(SemanticFixBug, nil); got != PolicyRewrite {
		t.Fatalf("Resolve(SemanticFixBug) = %s, want first-registered rewrite", got)
	}
	if got := registry.Resolve(SemanticCreateProject, nil); got != DefaultContextPolicy {
		t.Fatalf("unclaimed semantics must fall back to default, got %s", got)
	}
}

func TestStrategyRegistryIgnoresNilResolverAndNilReceiver(t *testing.T) {
	registry := NewStrategyRegistry()
	registry.Register(nil)
	if got := len(registry.Resolvers()); got != 4 {
		t.Fatalf("Resolvers length = %d, want 4 (nil register must be ignored)", got)
	}

	var nilRegistry *StrategyRegistry
	if got := nilRegistry.Resolve(SemanticRewriteProject, nil); got != DefaultContextPolicy {
		t.Fatalf("nil registry must return the default policy, got %s", got)
	}
	if got := nilRegistry.Resolvers(); got != nil {
		t.Fatalf("nil registry Resolvers must return nil, got %v", got)
	}
}

// resolverFunc adapts a function value to the StrategyResolver contract for
// tests. It always resolves to the compiled policy it was built with.
type resolverFunc struct {
	supports func(OperationSemantics) bool
	policy   ContextPolicy
}

func (f resolverFunc) Supports(semantics OperationSemantics) bool { return f.supports(semantics) }
func (f resolverFunc) Resolve(semantics OperationSemantics, _ *knowledge.KnowledgeGraph) ContextPolicy {
	return f.policy
}

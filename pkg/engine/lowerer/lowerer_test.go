package lowerer

import (
	"reflect"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/adapter"
	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
)

func TestCapabilityRegistryRegisterResolve(t *testing.T) {
	r := NewCapabilityRegistry()
	next := adapter.NewNextJSAdapter()
	if err := r.Register(adapter.FrameworkNextJS, CapabilityRendering, next); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Resolve(adapter.FrameworkNextJS, CapabilityRendering)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != next {
		t.Fatal("Resolve returned a different adapter")
	}
	if _, err := r.Resolve(adapter.FrameworkNextJS, CapabilityRouting); err == nil {
		t.Fatal("expected error resolving unregistered capability")
	}
	if _, err := r.Resolve(adapter.FrameworkAstro, CapabilityRendering); err == nil {
		t.Fatal("expected error resolving unregistered framework")
	}
	if err := r.Register(adapter.FrameworkNextJS, CapabilityRendering, nil); err == nil {
		t.Fatal("expected error registering a nil adapter")
	}
}

func TestDefaultRegistryCoversAllFrameworks(t *testing.T) {
	r := DefaultRegistry()
	for _, fw := range []adapter.Framework{
		adapter.FrameworkNextJS, adapter.FrameworkAstro,
		adapter.FrameworkReactVite, adapter.FrameworkGoGin,
	} {
		for _, cap := range []Capability{
			CapabilityRouting, CapabilityRendering, CapabilityStyling,
			CapabilityTesting, CapabilityData,
		} {
			if _, err := r.Resolve(fw, cap); err != nil {
				t.Fatalf("default registry missing %s/%s: %v", fw, cap, err)
			}
		}
	}
}

func TestResolveCapabilitiesUnion(t *testing.T) {
	l := NewPlanLowerer(nil)
	plan, err := ir.NewLogicalPlan([]ir.IRNode{
		&ir.CreatePageNode{Name: "About"},
		&ir.CreateEndpointNode{Method: "GET", Route: "/api/users", Name: "ListUsers"},
	}, nil)
	if err != nil {
		t.Fatalf("NewLogicalPlan: %v", err)
	}
	caps := l.ResolveCapabilities(adapter.FrameworkNextJS, plan)
	have := map[Capability]bool{}
	for _, c := range caps {
		have[c] = true
	}
	for _, want := range []Capability{CapabilityRendering, CapabilityRouting, CapabilityData} {
		if !have[want] {
			t.Errorf("ResolveCapabilities missing %q", want)
		}
	}
}

func TestLowerMapsAndRendersPlan(t *testing.T) {
	l := NewPlanLowerer(DefaultRegistry())
	page := &ir.CreatePageNode{Name: "About"}
	plan, err := ir.NewLogicalPlan([]ir.IRNode{page}, nil)
	if err != nil {
		t.Fatalf("NewLogicalPlan: %v", err)
	}

	// The same LogicalPlan renders per-framework.
	next, err := l.Lower(plan, adapter.FrameworkNextJS)
	if err != nil {
		t.Fatalf("Lower(nextjs): %v", err)
	}
	if len(next) != 1 || next[0].Path != "app/about/page.tsx" {
		t.Fatalf("nextjs artifacts = %+v, want app/about/page.tsx", next)
	}

	astro, err := l.Lower(plan, adapter.FrameworkAstro)
	if err != nil {
		t.Fatalf("Lower(astro): %v", err)
	}
	if len(astro) != 1 || astro[0].Path != "src/pages/about.astro" {
		t.Fatalf("astro artifacts = %+v, want src/pages/about.astro", astro)
	}

	// The plan and node are untouched by lowering.
	if page.Name != "About" {
		t.Fatalf("node mutated by lowering: %q", page.Name)
	}
	if plan.Len() != 1 {
		t.Fatalf("plan mutated by lowering: %d nodes", plan.Len())
	}
}

func TestMapPlanBindsCapabilities(t *testing.T) {
	l := NewPlanLowerer(DefaultRegistry())
	plan, err := ir.NewLogicalPlan([]ir.IRNode{
		&ir.CreatePageNode{Name: "About"},
		&ir.CreateEndpointNode{Method: "POST", Route: "/api/users", Name: "CreateUser"},
		&ir.CreateDatabaseMigrationNode{Name: "create_users_table"},
	}, nil)
	if err != nil {
		t.Fatalf("NewLogicalPlan: %v", err)
	}
	mapped, err := l.MapPlan(plan, adapter.FrameworkNextJS)
	if err != nil {
		t.Fatalf("MapPlan: %v", err)
	}
	if len(mapped) != 3 {
		t.Fatalf("mapped %d nodes, want 3", len(mapped))
	}
	wantCaps := []Capability{CapabilityRendering, CapabilityRouting, CapabilityData}
	for i, m := range mapped {
		if m.Capability != wantCaps[i] {
			t.Errorf("node %d capability = %q, want %q", i, m.Capability, wantCaps[i])
		}
		if m.Adapter.Framework() != adapter.FrameworkNextJS {
			t.Errorf("node %d adapter = %q, want nextjs", i, m.Adapter.Framework())
		}
	}
}

func TestLowerUnsupportedFrameworkFails(t *testing.T) {
	l := NewPlanLowerer(NewCapabilityRegistry()) // empty registry
	plan, _ := ir.NewLogicalPlan([]ir.IRNode{&ir.CreatePageNode{Name: "About"}}, nil)
	if _, err := l.Lower(plan, adapter.FrameworkNextJS); err == nil {
		t.Fatal("expected error lowering with an empty registry")
	}
}

func TestLowerNilPlanFails(t *testing.T) {
	l := NewPlanLowerer(DefaultRegistry())
	if _, err := l.Lower(nil, adapter.FrameworkNextJS); err == nil {
		t.Fatal("expected error lowering a nil plan")
	}
}

func TestRegistryCapabilitiesForIsStable(t *testing.T) {
	r := DefaultRegistry()
	a := r.CapabilitiesFor(adapter.FrameworkNextJS)
	b := r.CapabilitiesFor(adapter.FrameworkNextJS)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("CapabilitiesFor not stable: %v vs %v", a, b)
	}
	if len(a) != 6 {
		t.Fatalf("nextjs capabilities = %v, want 6", a)
	}
	if _, err := r.Resolve(adapter.FrameworkStaticWeb, CapabilityRendering); err != nil {
		t.Fatalf("static-web must serve rendering capability: %v", err)
	}
}

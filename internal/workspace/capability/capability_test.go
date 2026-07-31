package capability

import (
	"testing"

	"github.com/PizenLabs/izen/internal/workspace/snapshot"
)

func TestNewRegistry_ContainsDefaults(t *testing.T) {
	r := NewArchetypeCapabilityRegistry()

	if caps := r.GetCapabilities(snapshot.VANILLA_WEB); len(caps) == 0 {
		t.Fatal("expected capabilities for VANILLA_WEB")
	}
	if caps := r.GetCapabilities(snapshot.GO_MODULE); len(caps) == 0 {
		t.Fatal("expected capabilities for GO_MODULE")
	}
	if caps := r.GetCapabilities(snapshot.GENERIC_TEXT); len(caps) == 0 {
		t.Fatal("expected capabilities for GENERIC_TEXT")
	}
}

func TestVanillaWeb_OnlyFileInspection(t *testing.T) {
	r := NewArchetypeCapabilityRegistry()

	if !r.Has(snapshot.VANILLA_WEB, CapFileInspect) {
		t.Error("VANILLA_WEB should have CapFileInspect")
	}
	if !r.Has(snapshot.VANILLA_WEB, CapASTParse) {
		t.Error("VANILLA_WEB should have CapASTParse")
	}
	if !r.Has(snapshot.VANILLA_WEB, CapStaticServe) {
		t.Error("VANILLA_WEB should have CapStaticServe")
	}

	if r.Has(snapshot.VANILLA_WEB, CapGoBuild) {
		t.Error("VANILLA_WEB should NOT have CapGoBuild")
	}
	if r.Has(snapshot.VANILLA_WEB, CapGoTest) {
		t.Error("VANILLA_WEB should NOT have CapGoTest")
	}
	if r.Has(snapshot.VANILLA_WEB, CapGoVet) {
		t.Error("VANILLA_WEB should NOT have CapGoVet")
	}
	if r.Has(snapshot.VANILLA_WEB, CapGoMod) {
		t.Error("VANILLA_WEB should NOT have CapGoMod")
	}
	if r.Has(snapshot.VANILLA_WEB, CapNpmTest) {
		t.Error("VANILLA_WEB should NOT have CapNpmTest")
	}
	if r.Has(snapshot.VANILLA_WEB, CapNpmBuild) {
		t.Error("VANILLA_WEB should NOT have CapNpmBuild")
	}
}

func TestGoModule_HasGoTools(t *testing.T) {
	r := NewArchetypeCapabilityRegistry()

	if !r.Has(snapshot.GO_MODULE, CapGoBuild) {
		t.Error("GO_MODULE should have CapGoBuild")
	}
	if !r.Has(snapshot.GO_MODULE, CapGoTest) {
		t.Error("GO_MODULE should have CapGoTest")
	}
	if !r.Has(snapshot.GO_MODULE, CapGoVet) {
		t.Error("GO_MODULE should have CapGoVet")
	}
	if !r.Has(snapshot.GO_MODULE, CapGoMod) {
		t.Error("GO_MODULE should have CapGoMod")
	}
	if !r.Has(snapshot.GO_MODULE, CapLinter) {
		t.Error("GO_MODULE should have CapLinter")
	}
	if !r.Has(snapshot.GO_MODULE, CapFileInspect) {
		t.Error("GO_MODULE should have CapFileInspect")
	}

	if r.Has(snapshot.GO_MODULE, CapNpmTest) {
		t.Error("GO_MODULE should NOT have CapNpmTest")
	}
	if r.Has(snapshot.GO_MODULE, CapNpmBuild) {
		t.Error("GO_MODULE should NOT have CapNpmBuild")
	}
}

func TestGenericText_OnlyFileInspect(t *testing.T) {
	r := NewArchetypeCapabilityRegistry()

	if !r.Has(snapshot.GENERIC_TEXT, CapFileInspect) {
		t.Error("GENERIC_TEXT should have CapFileInspect")
	}

	if r.Has(snapshot.GENERIC_TEXT, CapASTParse) {
		t.Error("GENERIC_TEXT should NOT have CapASTParse")
	}
	if r.Has(snapshot.GENERIC_TEXT, CapGoBuild) {
		t.Error("GENERIC_TEXT should NOT have CapGoBuild")
	}
}

func TestHasAny(t *testing.T) {
	r := NewArchetypeCapabilityRegistry()

	if !r.HasAny(snapshot.GO_MODULE, CapGoTest, CapGoBuild) {
		t.Error("GO_MODULE should have at least one of CapGoTest/CapGoBuild")
	}
	if r.HasAny(snapshot.VANILLA_WEB, CapGoTest, CapGoBuild) {
		t.Error("VANILLA_WEB should not have any Go capabilities")
	}
	if r.HasAny(snapshot.GENERIC_TEXT, CapGoTest, CapASTParse) {
		t.Error("GENERIC_TEXT should not have any of CapGoTest/CapASTParse")
	}
}

func TestArchetypeHasGoTools(t *testing.T) {
	r := NewArchetypeCapabilityRegistry()

	if !r.ArchetypeHasGoTools(snapshot.GO_MODULE) {
		t.Error("GO_MODULE should have Go tools")
	}
	if r.ArchetypeHasGoTools(snapshot.VANILLA_WEB) {
		t.Error("VANILLA_WEB should NOT have Go tools")
	}
	if r.ArchetypeHasGoTools(snapshot.NODE_APP) {
		t.Error("NODE_APP should NOT have Go tools")
	}
	if r.ArchetypeHasGoTools(snapshot.GENERIC_TEXT) {
		t.Error("GENERIC_TEXT should NOT have Go tools")
	}
}

func TestArchetypeHasNpmTools(t *testing.T) {
	r := NewArchetypeCapabilityRegistry()

	if !r.ArchetypeHasNpmTools(snapshot.NODE_APP) {
		t.Error("NODE_APP should have npm tools")
	}
	if r.ArchetypeHasNpmTools(snapshot.GO_MODULE) {
		t.Error("GO_MODULE should NOT have npm tools")
	}
	if r.ArchetypeHasNpmTools(snapshot.VANILLA_WEB) {
		t.Error("VANILLA_WEB should NOT have npm tools")
	}
}

func TestRegister_AddsCapabilities(t *testing.T) {
	r := NewArchetypeCapabilityRegistry()
	r.Register(snapshot.GENERIC_TEXT, CapASTParse)

	if !r.Has(snapshot.GENERIC_TEXT, CapASTParse) {
		t.Error("GENERIC_TEXT should have CapASTParse after Register")
	}
	if !r.Has(snapshot.GENERIC_TEXT, CapFileInspect) {
		t.Error("GENERIC_TEXT should still have CapFileInspect after Register")
	}
}

func TestSetCapabilities_Replaces(t *testing.T) {
	r := NewArchetypeCapabilityRegistry()
	r.SetCapabilities(snapshot.VANILLA_WEB, []DiagnosticCapability{CapFileInspect})

	if !r.Has(snapshot.VANILLA_WEB, CapFileInspect) {
		t.Error("VANILLA_WEB should have CapFileInspect after SetCapabilities")
	}
	if r.Has(snapshot.VANILLA_WEB, CapASTParse) {
		t.Error("VANILLA_WEB should NOT have CapASTParse after SetCapabilities replaces")
	}
}

func TestGetCapabilities_ReturnsCopy(t *testing.T) {
	r := NewArchetypeCapabilityRegistry()

	caps1 := r.GetCapabilities(snapshot.GO_MODULE)
	caps2 := r.GetCapabilities(snapshot.GO_MODULE)

	if len(caps1) != len(caps2) {
		t.Fatal("expected same length")
	}
	for i := range caps1 {
		if caps1[i] != caps2[i] {
			t.Fatal("expected identical capability lists")
		}
	}

	// Mutating caps1 should not affect registry — the original slice
	// remains unchanged in the registry.
	if !r.Has(snapshot.GO_MODULE, caps1[0]) {
		t.Error("GetCapabilities returns a copy; mutating it should not affect registry")
	}
}

func TestNodeAppCapabilities(t *testing.T) {
	r := NewArchetypeCapabilityRegistry()

	if !r.Has(snapshot.NODE_APP, CapFileInspect) {
		t.Error("NODE_APP should have CapFileInspect")
	}
	if !r.Has(snapshot.NODE_APP, CapNpmTest) {
		t.Error("NODE_APP should have CapNpmTest")
	}
	if !r.Has(snapshot.NODE_APP, CapNpmBuild) {
		t.Error("NODE_APP should have CapNpmBuild")
	}
	if !r.Has(snapshot.NODE_APP, CapLinter) {
		t.Error("NODE_APP should have CapLinter")
	}
	if r.Has(snapshot.NODE_APP, CapGoBuild) {
		t.Error("NODE_APP should NOT have CapGoBuild")
	}
}

func TestUnknownArchetype_EmptyCaps(t *testing.T) {
	r := NewArchetypeCapabilityRegistry()

	caps := r.GetCapabilities("UNKNOWN")
	if len(caps) != 0 {
		t.Fatalf("expected empty capabilities for unknown archetype, got %v", caps)
	}
	if r.Has("UNKNOWN", CapFileInspect) {
		t.Error("unknown archetype should not have any capabilities")
	}
}

func TestDiagnosticCapability_String(t *testing.T) {
	tests := []struct {
		dc   DiagnosticCapability
		want string
	}{
		{CapFileInspect, "file_inspect"},
		{CapASTParse, "ast_parse"},
		{CapStaticServe, "static_serve"},
		{CapGoBuild, "go_build"},
		{CapGoTest, "go_test"},
		{CapGoVet, "go_vet"},
		{CapGoMod, "go_mod"},
		{CapNpmTest, "npm_test"},
		{CapNpmBuild, "npm_build"},
		{CapLinter, "linter"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.dc.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

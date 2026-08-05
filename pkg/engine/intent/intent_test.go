package intent

import (
	"strings"
	"testing"
)

func TestNewValidatesFamily(t *testing.T) {
	if _, err := New(Family("nonsense")); err == nil {
		t.Fatal("expected error for invalid family")
	}
	if _, err := New(Family("nonsense"), FacetMutates); err == nil {
		t.Fatal("expected error for invalid family with facet")
	}
	if _, err := New(Family("bugfix"), Facet("bogus")); err == nil {
		t.Fatal("expected error for invalid facet")
	}
}

func TestNewDerivesFacetsFromFamily(t *testing.T) {
	tests := []struct {
		family   Family
		wantOn   []Facet
		wantOff  []Facet
		mutating bool
	}{
		{FamilyGreenfield, []Facet{FacetMutates}, []Facet{FacetReadOnly, FacetRequiresTest}, true},
		{FamilyFeature, []Facet{FacetMutates, FacetRunsTools, FacetRequiresTest}, []Facet{FacetReadOnly}, true},
		{FamilyBugFix, []Facet{FacetMutates, FacetRunsTools, FacetRequiresTest, FacetExploratory}, []Facet{FacetReadOnly}, true},
		{FamilyRefactor, []Facet{FacetMutates, FacetRunsTools, FacetRequiresTest}, []Facet{FacetReadOnly}, true},
		{FamilyArchitecture, []Facet{FacetReadOnly}, []Facet{FacetMutates, FacetRunsTools}, false},
		{FamilyQuestion, []Facet{FacetReadOnly}, []Facet{FacetMutates}, false},
		{FamilyGeneral, []Facet{FacetReadOnly}, []Facet{FacetMutates, FacetRunsTools}, false},
	}
	for _, tt := range tests {
		in, err := New(tt.family)
		if err != nil {
			t.Fatalf("New(%s): %v", tt.family, err)
		}
		for _, on := range tt.wantOn {
			if !in.Has(on) {
				t.Errorf("New(%s): facet %s should be on", tt.family, on)
			}
		}
		for _, off := range tt.wantOff {
			if in.Has(off) {
				t.Errorf("New(%s): facet %s should be off", tt.family, off)
			}
		}
		if in.Family() != tt.family {
			t.Errorf("New(%s): family = %s", tt.family, in.Family())
		}
		if got := tt.family.Mutating(); got != tt.mutating {
			t.Errorf("Family(%s).Mutating() = %v, want %v", tt.family, got, tt.mutating)
		}
	}
}

func TestMutatesAndReadOnlyAreMutuallyExclusive(t *testing.T) {
	in := Must(FamilyFeature)
	ro := in.With(FacetReadOnly)
	if !ro.Has(FacetReadOnly) {
		t.Fatal("With(ReadOnly) did not enable ReadOnly")
	}
	if ro.Has(FacetMutates) {
		t.Fatal("With(ReadOnly) should clear Mutates")
	}

	back := ro.With(FacetMutates)
	if !back.Has(FacetMutates) {
		t.Fatal("With(Mutates) did not enable Mutates")
	}
	if back.Has(FacetReadOnly) {
		t.Fatal("With(Mutates) should clear ReadOnly")
	}
}

func TestIntentImmutability(t *testing.T) {
	base := Must(FamilyBugFix)
	_ = base.With(FacetHighRisk)
	if base.Has(FacetHighRisk) {
		t.Fatal("With mutated the receiver")
	}
	without := base.Without(FacetRequiresTest)
	if !base.Has(FacetRequiresTest) {
		t.Fatal("Without mutated the receiver")
	}
	if without.Has(FacetRequiresTest) {
		t.Fatal("Without did not remove the facet")
	}
	// Original must remain fully intact.
	if !base.Has(FacetRequiresTest) || !base.Has(FacetExploratory) {
		t.Fatal("original intent was damaged by derivations")
	}
}

func TestIntentValidate(t *testing.T) {
	good := Must(FamilyFeature)
	if err := good.Validate(); err != nil {
		t.Fatalf("valid intent rejected: %v", err)
	}
	if err := (Intent{family: Family("x")}).Validate(); err == nil {
		t.Fatal("invalid family accepted")
	}
}

func TestIsZeroAndString(t *testing.T) {
	var zero Intent
	if !zero.IsZero() {
		t.Fatal("zero intent should be IsZero")
	}
	if strings.Contains(zero.String(), "<zero>") == false {
		t.Fatalf("zero intent String = %q", zero.String())
	}
	in := Must(FamilyFeature)
	s := in.String()
	if !strings.Contains(s, "feature") || !strings.Contains(s, "mutates") {
		t.Fatalf("String = %q", s)
	}
}

func TestFacetsSorted(t *testing.T) {
	in := Must(FamilyBugFix).With(FacetHighRisk)
	fs := in.Facets()
	for i := 1; i < len(fs); i++ {
		if fs[i-1] >= fs[i] {
			t.Fatalf("facets not sorted: %v", fs)
		}
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		prompt string
		family Family
	}{
		{"", FamilyGeneral},
		{"explain what the service does", FamilyQuestion},
		{"Create a new project from scratch", FamilyGreenfield},
		{"generate a new Go service with a health endpoint", FamilyGreenfield},
		{"the handler crashes with a nil pointer on startup", FamilyBugFix},
		{"fix the failing test regression in the auth package", FamilyBugFix},
		{"refactor the checkout module to decouple payments", FamilyRefactor},
		{"show me the architecture overview and route map", FamilyArchitecture},
		{"implement an endpoint to fetch user profiles", FamilyFeature},
		{"tell me the weather today", FamilyGeneral},
	}
	for _, tt := range tests {
		got := Classify(tt.prompt)
		if got.Family() != tt.family {
			t.Errorf("Classify(%q) family = %s, want %s", tt.prompt, got.Family(), tt.family)
		}
		if tt.family != FamilyGeneral && tt.family.ReadOnly() && !got.Has(FacetReadOnly) {
			t.Errorf("Classify(%q) should be read-only", tt.prompt)
		}
		if tt.family != FamilyGeneral && tt.family.Mutating() && !got.Has(FacetMutates) {
			t.Errorf("Classify(%q) should be mutating", tt.prompt)
		}
	}
}

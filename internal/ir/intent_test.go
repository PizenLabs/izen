package ir

import (
	"strings"
	"testing"
)

func TestCategoryValidityAndString(t *testing.T) {
	for c, want := range map[Category]bool{
		CategoryCreate:    true,
		CategoryRedesign:  true,
		CategoryRefactor:  true,
		CategoryFixBug:    true,
		Category(""):      false,
		Category("write"): false,
	} {
		if got := c.Valid(); got != want {
			t.Errorf("Valid(%q) = %v, want %v", c, got, want)
		}
	}
	if CategoryRedesign.String() != "redesign" {
		t.Errorf("String() = %q, want redesign", CategoryRedesign.String())
	}
}

func TestCategoryPreservesWorkspace(t *testing.T) {
	if CategoryRedesign.PreservesWorkspace() {
		t.Error("redesign must not preserve the workspace")
	}
	for _, c := range []Category{CategoryCreate, CategoryRefactor, CategoryFixBug} {
		if !c.PreservesWorkspace() {
			t.Errorf("%s must preserve the workspace", c)
		}
	}
}

func TestIntentIRValidate(t *testing.T) {
	good := IntentIR{Category: CategoryRedesign, TargetType: "portfolio"}
	if err := good.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if err := (IntentIR{Category: Category("bogus"), TargetType: "x"}).Validate(); err == nil {
		t.Error("Validate = nil for invalid category")
	}
	if err := (IntentIR{Category: CategoryCreate, TargetType: ""}).Validate(); err == nil {
		t.Error("Validate = nil for empty target type")
	}
}

func TestIntentIRString(t *testing.T) {
	i := IntentIR{
		Category:          CategoryRedesign,
		TargetType:        "portfolio",
		Entities:          map[string]string{"author": "Alex Josie"},
		Technologies:      []string{"html", "css", "js"},
		DecisionAmbiguity: true,
	}
	s := i.String()
	for _, want := range []string{"redesign", "portfolio", "author=Alex Josie", "html", "!ambig"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, want it to contain %q", s, want)
		}
	}
	// The rendered entity order must be deterministic.
	first := i.String()
	if first != i.String() {
		t.Error("String() is not deterministic")
	}
}

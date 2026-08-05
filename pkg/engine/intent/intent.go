// Package intent is the microkernel's primary request classifier. An Intent
// pairs a primary Family with a set of boolean Facets that shape how the
// strategy and plan pipeline treats the request. Intents are immutable
// values: every deriving method returns a new Intent and never mutates its
// receiver.
package intent

import (
	"fmt"
	"sort"
	"strings"
)

// Family is the primary, mutually-exclusive classification of a user request.
// It selects how context is gathered and how plans are derived.
type Family string

const (
	// FamilyGreenfield asks the engine to generate a new project or files
	// from scratch. It is a mutating family.
	FamilyGreenfield Family = "greenfield"
	// FamilyFeature asks for new functionality on top of existing files. It
	// is a mutating family.
	FamilyFeature Family = "feature"
	// FamilyBugFix asks to repair a defect. It is a mutating family that
	// demands verification.
	FamilyBugFix Family = "bugfix"
	// FamilyRefactor asks to restructure existing code without changing
	// behaviour. It is a mutating family that demands verification.
	FamilyRefactor Family = "refactor"
	// FamilyArchitecture asks a structural question about the workspace. It
	// is a read-only family.
	FamilyArchitecture Family = "architecture"
	// FamilyQuestion asks an explanatory question. It is a read-only family.
	FamilyQuestion Family = "question"
	// FamilyGeneral is the fallback for requests with no dominant signal.
	FamilyGeneral Family = "general"
)

// allFamilies preserves declaration order for AllFamilies and Valid.
var allFamilies = []Family{
	FamilyGreenfield, FamilyFeature, FamilyBugFix, FamilyRefactor,
	FamilyArchitecture, FamilyQuestion, FamilyGeneral,
}

// Valid reports whether f is one of the defined families.
func (f Family) Valid() bool {
	for _, x := range allFamilies {
		if f == x {
			return true
		}
	}
	return false
}

// String returns the machine-readable family label.
func (f Family) String() string { return string(f) }

// Mutating reports whether the family may modify the filesystem.
func (f Family) Mutating() bool {
	switch f {
	case FamilyGreenfield, FamilyFeature, FamilyBugFix, FamilyRefactor:
		return true
	default:
		return false
	}
}

// ReadOnly reports whether the family is strictly read-only.
func (f Family) ReadOnly() bool { return !f.Mutating() }

// AllFamilies returns every defined family in declaration order.
func AllFamilies() []Family {
	return append([]Family(nil), allFamilies...)
}

// Facet is a boolean modifier attached to an Intent. Facets are not
// mutually exclusive; any subset may be active.
type Facet string

const (
	// FacetMutates marks an intent whose plan may touch the filesystem.
	FacetMutates Facet = "mutates"
	// FacetReadOnly marks an intent whose plan may only read.
	FacetReadOnly Facet = "read_only"
	// FacetRunsTools marks an intent whose plan may execute shell commands.
	FacetRunsTools Facet = "runs_tools"
	// FacetRequiresTest marks an intent whose plan must include a verify
	// step before completion.
	FacetRequiresTest Facet = "requires_test"
	// FacetExploratory marks a diagnostic or investigative intent.
	FacetExploratory Facet = "exploratory"
	// FacetHighRisk marks an intent that carries elevated risk and should
	// demand stricter policy scrutiny.
	FacetHighRisk Facet = "high_risk"
)

// Valid reports whether f is one of the defined facets.
func (f Facet) Valid() bool {
	switch f {
	case FacetMutates, FacetReadOnly, FacetRunsTools, FacetRequiresTest,
		FacetExploratory, FacetHighRisk:
		return true
	default:
		return false
	}
}

// String returns the machine-readable facet label.
func (f Facet) String() string { return string(f) }

// Intent is an immutable classification of one user request. It carries a
// primary Family and a set of boolean Facets. Every field is unexported and
// every deriving method returns a new Intent, so an Intent can never change
// after construction.
type Intent struct {
	family Family
	facets map[Facet]bool
}

// New constructs an Intent from a primary family and optional facets. It
// normalises the facet set: Mutates and ReadOnly are mutually exclusive
// (the last one applied wins), and Mutates/RunsTools/RequiresTest are
// derived from the family when not explicitly provided.
func New(family Family, facets ...Facet) (Intent, error) {
	if !family.Valid() {
		return Intent{}, fmt.Errorf("intent: invalid family %q", family)
	}
	set := make(map[Facet]bool, len(facets)+3)
	for _, f := range facets {
		if !f.Valid() {
			return Intent{}, fmt.Errorf("intent: invalid facet %q", f)
		}
		set[f] = true
	}

	// Derive defaults from the family for facets the caller did not set.
	if _, explicit := set[FacetMutates]; !explicit {
		if _, explicitRO := set[FacetReadOnly]; !explicitRO {
			if family.Mutating() {
				set[FacetMutates] = true
			} else {
				set[FacetReadOnly] = true
			}
		}
	}
	// Mutates and ReadOnly are mutually exclusive.
	if set[FacetMutates] {
		delete(set, FacetReadOnly)
	} else if set[FacetReadOnly] {
		delete(set, FacetMutates)
	}

	switch family {
	case FamilyBugFix, FamilyFeature, FamilyRefactor:
		if _, explicit := set[FacetRequiresTest]; !explicit {
			set[FacetRequiresTest] = true
		}
		set[FacetRunsTools] = true
	}
	if family == FamilyBugFix {
		set[FacetExploratory] = true
	}

	return Intent{family: family, facets: set}, nil
}

// Must is like New but panics on error. It is intended for tests and
// compile-time constants.
func Must(family Family, facets ...Facet) Intent {
	in, err := New(family, facets...)
	if err != nil {
		panic(err)
	}
	return in
}

// Family returns the primary family of the intent.
func (i Intent) Family() Family { return i.family }

// Has reports whether the given facet is active.
func (i Intent) Has(f Facet) bool { return i.facets[f] }

// Facets returns the active facets in stable sorted order.
func (i Intent) Facets() []Facet {
	out := make([]Facet, 0, len(i.facets))
	for f, on := range i.facets {
		if on {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

// With returns a new Intent with the given facet enabled. The receiver is
// left unchanged.
func (i Intent) With(f Facet) Intent {
	if !f.Valid() {
		return i
	}
	next := i.clone()
	next.facets[f] = true
	switch f {
	case FacetMutates:
		delete(next.facets, FacetReadOnly)
	case FacetReadOnly:
		delete(next.facets, FacetMutates)
	}
	return next
}

// Without returns a new Intent with the given facet disabled. The receiver
// is left unchanged.
func (i Intent) Without(f Facet) Intent {
	next := i.clone()
	delete(next.facets, f)
	return next
}

// IsZero reports whether the intent is the zero value.
func (i Intent) IsZero() bool { return i.family == "" && len(i.facets) == 0 }

// Validate reports whether the intent is well-formed.
func (i Intent) Validate() error {
	if !i.family.Valid() {
		return fmt.Errorf("intent: invalid family %q", i.family)
	}
	for f := range i.facets {
		if !f.Valid() {
			return fmt.Errorf("intent: invalid facet %q", f)
		}
	}
	return nil
}

// String renders a compact human-readable intent label.
func (i Intent) String() string {
	if i.IsZero() {
		return "<zero>"
	}
	return string(i.family) + "[" + strings.Join(facetStrings(i.Facets()), ",") + "]"
}

func (i Intent) clone() Intent {
	next := Intent{family: i.family, facets: make(map[Facet]bool, len(i.facets))}
	for f, on := range i.facets {
		next.facets[f] = on
	}
	return next
}

func facetStrings(fs []Facet) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = string(f)
	}
	return out
}

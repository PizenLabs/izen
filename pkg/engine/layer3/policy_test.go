package layer3

import (
	"errors"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/layer1"
)

type fakeCaps struct {
	caps map[layer1.Capability]bool
}

func (f fakeCaps) Supports(c layer1.Capability) bool {
	return f.caps[c]
}

func TestIntentDeterminism(t *testing.T) {
	deterministic := []Intent{IntentRename, IntentFormat, IntentAddImport, IntentRemoveImport}
	generative := []Intent{IntentRefactor, IntentNewFeature, IntentBugFix}

	for _, i := range deterministic {
		if !i.Deterministic() {
			t.Errorf("%s: expected deterministic", i)
		}
		if i.Generative() {
			t.Errorf("%s: expected non-generative", i)
		}
	}
	for _, i := range generative {
		if i.Deterministic() {
			t.Errorf("%s: expected generative", i)
		}
		if !i.Generative() {
			t.Errorf("%s: expected generative", i)
		}
	}
	if Intent("bogus").Valid() {
		t.Error("bogus intent unexpectedly valid")
	}
	for _, i := range AllIntents() {
		if !i.Valid() {
			t.Errorf("%s: expected valid", i)
		}
	}
}

func TestPolicyGuardRoute(t *testing.T) {
	g := NewPolicyGuard(nil)

	cases := []struct {
		intent Intent
		want   Route
	}{
		{IntentRename, RouteASTRewrite},
		{IntentFormat, RouteASTRewrite},
		{IntentAddImport, RouteASTRewrite},
		{IntentRemoveImport, RouteASTRewrite},
		{IntentRefactor, RouteGenerative},
		{IntentNewFeature, RouteGenerative},
		{IntentBugFix, RouteGenerative},
	}
	for _, tc := range cases {
		req := Request{Intent: tc.intent}
		switch tc.intent {
		case IntentRename:
			req.TargetSymbol = "Foo"
			req.NewName = "Bar"
		case IntentFormat, IntentRemoveImport:
			req.TargetFile = "a.go"
		case IntentAddImport:
			req.TargetFile = "a.go"
			req.NewImport = "os"
		default:
			req.Description = "do the thing"
		}
		got, err := g.Route(req)
		if err != nil {
			t.Errorf("%s: Route error: %v", tc.intent, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: route = %v, want %v", tc.intent, got, tc.want)
		}
	}

	if _, err := g.Route(Request{Intent: Intent("bogus")}); err == nil {
		t.Error("expected error for bogus intent")
	} else if !errors.Is(err, ErrInvalidIntent) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPolicyGuardValidate(t *testing.T) {
	g := NewPolicyGuard(nil)

	cases := []struct {
		name string
		req  Request
		want error
	}{
		{"rename missing symbol", Request{Intent: IntentRename, NewName: "Bar"}, ErrMissingSymbol},
		{"rename missing new name", Request{Intent: IntentRename, TargetSymbol: "Foo"}, ErrMissingNewName},
		{"format missing target", Request{Intent: IntentFormat}, ErrMissingTarget},
		{"add import missing target", Request{Intent: IntentAddImport, NewImport: "os"}, ErrMissingTarget},
		{"add import missing path", Request{Intent: IntentAddImport, TargetFile: "a.go"}, ErrMissingImport},
		{"remove import missing target", Request{Intent: IntentRemoveImport}, ErrMissingTarget},
		{"refactor missing description", Request{Intent: IntentRefactor}, ErrMissingDescription},
		{"new feature missing description", Request{Intent: IntentNewFeature}, ErrMissingDescription},
		{"bug fix missing description", Request{Intent: IntentBugFix}, ErrMissingDescription},
	}

	valid := Request{
		Intent:       IntentRename,
		TargetSymbol: "Foo",
		NewName:      "Bar",
	}
	if err := g.Validate(valid); err != nil {
		t.Errorf("valid request rejected: %v", err)
	}

	for _, tc := range cases {
		err := g.Validate(tc.req)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestPolicyGuardRequiresLLM(t *testing.T) {
	g := NewPolicyGuard(nil)
	if g.RequiresLLM(Request{Intent: IntentRename, TargetSymbol: "Foo", NewName: "Bar"}) {
		t.Error("rename must not require an LLM")
	}
	if !g.RequiresLLM(Request{Intent: IntentRefactor, Description: "refactor"}) {
		t.Error("refactor must require an LLM")
	}
}

func TestPolicyGuardValidationMode(t *testing.T) {
	if m := NewPolicyGuard(nil).ValidationMode(); m != ValidationStructural {
		t.Errorf("nil caps mode = %v, want structural", m)
	}
	if m := NewPolicyGuard(fakeCaps{}).ValidationMode(); m != ValidationStructural {
		t.Errorf("empty caps mode = %v, want structural", m)
	}
	if m := NewPolicyGuard(fakeCaps{caps: map[layer1.Capability]bool{layer1.CapBuild: true}}).ValidationMode(); m != ValidationCommand {
		t.Errorf("build caps mode = %v, want command", m)
	}
	if m := NewPolicyGuard(fakeCaps{caps: map[layer1.Capability]bool{layer1.CapTest: true}}).ValidationMode(); m != ValidationCommand {
		t.Errorf("test caps mode = %v, want command", m)
	}
	if m := NewPolicyGuard(fakeCaps{caps: map[layer1.Capability]bool{layer1.CapLint: true}}).ValidationMode(); m != ValidationStructural {
		t.Errorf("lint-only caps mode = %v, want structural", m)
	}
}

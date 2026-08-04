package registry

import (
	"context"
	"errors"
	"testing"
)

// fakeStrategy is a configurable strategy plugin for tests.
type fakeStrategy struct {
	name string
	res  *Result
	err  error
}

func (f *fakeStrategy) Name() string { return f.name }

func (f *fakeStrategy) Execute(_ context.Context, _ Task) (*Result, error) {
	return f.res, f.err
}

func TestStrategyRegistryRegisterAndGet(t *testing.T) {
	r := NewStrategyRegistry()
	s := &fakeStrategy{name: "patch", res: &Result{Status: StatusOK}}
	if err := r.Register("patch", s, CapabilityCoding, CapabilityCoding); err != nil {
		t.Fatal(err)
	}
	got, caps, ok := r.Get("patch")
	if !ok {
		t.Fatal("strategy not found")
	}
	if got != s {
		t.Error("retrieved different strategy instance")
	}
	if len(caps) != 1 || caps[0] != CapabilityCoding {
		t.Errorf("capabilities = %v, want [coding] (deduped)", caps)
	}
	if _, _, ok := r.Get("missing"); ok {
		t.Error("missing strategy should not be found")
	}
}

func TestStrategyRegistryErrors(t *testing.T) {
	r := NewStrategyRegistry()
	if err := r.Register("patch", &fakeStrategy{name: "patch"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("patch", &fakeStrategy{name: "patch"}); err == nil {
		t.Error("duplicate registration should fail")
	}
	if err := r.Register("", nil); err == nil {
		t.Error("nil strategy should fail")
	}
}

func TestStrategyRegistryNames(t *testing.T) {
	r := NewStrategyRegistry()
	_ = r.Register("b", &fakeStrategy{name: "b"})
	_ = r.Register("a", &fakeStrategy{name: "a"})
	names := r.Names()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Errorf("Names = %v, want sorted [a b]", names)
	}
}

func TestCapabilityRegistry(t *testing.T) {
	r := NewCapabilityRegistry()
	if err := r.Register(CapabilityToolUse, "shell", "shell"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(CapabilityCoding, "anthropic"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(CapabilityToolUse); err == nil {
		t.Error("empty provider list should fail")
	}
	providers := r.ProvidersFor(CapabilityToolUse)
	if len(providers) != 1 || providers[0] != "shell" {
		t.Errorf("providers = %v, want [shell]", providers)
	}
	satisfied, unmet := r.Resolve([]Capability{CapabilityToolUse, CapabilityTest})
	if len(satisfied) != 1 {
		t.Errorf("satisfied = %v, want 1", satisfied)
	}
	if len(unmet) != 1 || unmet[0] != CapabilityTest {
		t.Errorf("unmet = %v, want [test]", unmet)
	}
}

func TestValidationRegistryRun(t *testing.T) {
	r := NewValidationRegistry()
	r.Add(&fixedValidator{name: "v1", ok: true})
	r.Add(&fixedValidator{name: "v2", ok: false})
	res := r.Run(context.Background(), []string{"a.go", "b.go"})
	if res.OK {
		t.Error("run should fail when any validator fails")
	}
	if len(res.Reports) != 4 {
		t.Errorf("reports = %d, want 4 (2 validators x 2 targets)", len(res.Reports))
	}
	if len(r.Pipeline()) != 2 {
		t.Error("pipeline snapshot should have 2 validators")
	}
}

func TestValidationRegistryNilTargets(t *testing.T) {
	r := NewValidationRegistry()
	res := r.Run(context.Background(), nil)
	if !res.OK || len(res.Reports) != 0 {
		t.Error("empty target run should be OK with no reports")
	}
}

func TestValidationRegistryCancelled(t *testing.T) {
	r := NewValidationRegistry()
	r.Add(&fixedValidator{name: "v", ok: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := r.Run(ctx, []string{"a.go"})
	if res.OK || res.Err == nil {
		t.Error("cancelled run should not be OK and should carry the error")
	}
}

// fixedValidator returns a fixed verdict.
type fixedValidator struct {
	name string
	ok   bool
}

func (f *fixedValidator) Name() string { return f.name }

func (f *fixedValidator) Validate(_ context.Context, path string) (*ValidationReport, error) {
	if !f.ok {
		return &ValidationReport{Name: f.name, Path: path, OK: false, Output: "nope", Err: errors.New("nope")}, nil
	}
	return &ValidationReport{Name: f.name, Path: path, OK: true, Output: "ok"}, nil
}

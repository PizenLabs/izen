package capability

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeCapability is a configurable Capability used to exercise the registry.
// It exercises the decoupling contract: the capability owns its prompt
// representation and its validation logic.
type fakeCapability struct {
	id        CapabilityID
	desc      string
	prompts   map[string]string
	valid     bool
	reasons   []string
	requires  []string
	validateN int
}

func (f *fakeCapability) ID() CapabilityID { return f.id }

func (f *fakeCapability) Description() string { return f.desc }

func (f *fakeCapability) PromptRepresentation(tier string) string {
	if p, ok := f.prompts[tier]; ok {
		return p
	}
	return "default:" + tier
}

func (f *fakeCapability) Validate(_ context.Context, _ []byte) ValidationResult {
	f.validateN++
	if f.valid {
		return Pass("ok: " + f.id.String())
	}
	return Fail(f.reasons...)
}

func (f *fakeCapability) RuntimeRequirements() []string { return f.requires }

func newFake(id CapabilityID) *fakeCapability {
	return &fakeCapability{
		id:      id,
		desc:    "capability " + id.String(),
		prompts: map[string]string{"small": "short prompt", "full": "long prompt"},
		valid:   true,
	}
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	c := newFake("filegen")
	if err := r.Register(c); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Lookup("filegen")
	if !ok {
		t.Fatal("registered capability not found")
	}
	if got != c {
		t.Error("lookup returned a different instance")
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1", r.Len())
	}
	if !r.Has("filegen") {
		t.Error("Has(filegen) = false")
	}
	if r.Has("missing") {
		t.Error("Has(missing) = true")
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Error("lookup of unknown id succeeded")
	}
}

func TestRegistryRejectsBadRegistrations(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); !errors.Is(err, ErrNilCapability) {
		t.Errorf("nil capability error = %v, want ErrNilCapability", err)
	}
	if err := r.Register(&fakeCapability{prompts: map[string]string{}}); !errors.Is(err, ErrEmptyID) {
		t.Errorf("empty id error = %v, want ErrEmptyID", err)
	}
	if err := r.Register(newFake("x")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(newFake("x")); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate error = %v, want ErrDuplicate", err)
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1 after failed duplicates", r.Len())
	}
}

func TestRegistryRegisterAllStopsOnFirstError(t *testing.T) {
	r := NewRegistry()
	err := r.RegisterAll(newFake("a"), newFake("b"), newFake("a"), newFake("c"))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("RegisterAll error = %v, want ErrDuplicate", err)
	}
	if !r.Has("a") || !r.Has("b") {
		t.Error("capabilities before the failure must stay registered")
	}
	if r.Has("c") {
		t.Error("capability after the failure must not be registered")
	}
}

func TestRegistryResolvePreservesOrder(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterAll(newFake("a"), newFake("b"), newFake("c")); err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve("c", "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID() != "c" || got[1].ID() != "a" {
		t.Errorf("Resolve(c, a) = %v, want [c a]", idsOf(got))
	}
	one, err := r.ResolveOne("b")
	if err != nil {
		t.Fatal(err)
	}
	if one.ID() != "b" {
		t.Errorf("ResolveOne(b) = %s, want b", one.ID())
	}
}

func TestRegistryResolveUnknownFails(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(newFake("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve("a", "ghost"); err == nil {
		t.Error("Resolve with unknown id must fail")
	}
	if _, err := r.ResolveOne("ghost"); err == nil {
		t.Error("ResolveOne of unknown id must fail")
	}
	if _, err := r.Validate(context.Background(), "ghost", nil); err == nil {
		t.Error("Validate of unknown id must fail")
	}
	if _, err := r.PromptRepresentation("ghost", "small"); err == nil {
		t.Error("PromptRepresentation of unknown id must fail")
	}
}

func TestRegistryIDsAndSorted(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterAll(newFake("b"), newFake("a"), newFake("c")); err != nil {
		t.Fatal(err)
	}
	ids := r.IDs()
	if len(ids) != 3 || ids[0] != "b" || ids[1] != "a" || ids[2] != "c" {
		t.Errorf("IDs = %v, want registration order [b a c]", ids)
	}
	sorted := r.SortedIDs()
	if sorted[0] != "a" || sorted[1] != "b" || sorted[2] != "c" {
		t.Errorf("SortedIDs = %v, want [a b c]", sorted)
	}
}

func TestRegistryValidateAndPromptDelegation(t *testing.T) {
	r := NewRegistry()
	ok := newFake("ok")
	bad := &fakeCapability{
		id:      "bad",
		prompts: map[string]string{},
		valid:   false,
		reasons: []string{"missing header", "bad schema"},
	}
	if err := r.RegisterAll(ok, bad); err != nil {
		t.Fatal(err)
	}

	res, err := r.Validate(context.Background(), "ok", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || len(res.Reasons) == 0 {
		t.Errorf("Validate(ok) = %+v, want passed with reasons", res)
	}

	res, err = r.Validate(context.Background(), "bad", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Error("Validate(bad) must fail")
	}
	if len(res.Reasons) != 2 || res.Reasons[0] != "missing header" {
		t.Errorf("reasons = %v, want [missing header bad schema]", res.Reasons)
	}

	p, err := r.PromptRepresentation("ok", "small")
	if err != nil {
		t.Fatal(err)
	}
	if p != "short prompt" {
		t.Errorf("PromptRepresentation(small) = %q, want short prompt", p)
	}
	p, err = r.PromptRepresentation("ok", "full")
	if err != nil {
		t.Fatal(err)
	}
	if p != "long prompt" {
		t.Errorf("PromptRepresentation(full) = %q, want long prompt", p)
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup

	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := CapabilityID(string(rune('a' + i%26)))
			_ = r.Register(newFake(id))
		}(i)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for j := 0; j < 500; j++ {
			_ = r.Len()
			_ = r.IDs()
			_ = r.Has("a")
			if caps, err := r.Resolve("a"); err == nil {
				_ = caps
			}
		}
	}()

	for i := 0; i < 64; i++ {
		wg.Wait()
	}
	<-done
}

func TestValidationResultHelpers(t *testing.T) {
	p := Pass("fine")
	if !p.Passed || len(p.Reasons) != 1 || p.Reasons[0] != "fine" {
		t.Errorf("Pass() = %+v", p)
	}
	if p.Failed() {
		t.Error("Pass().Failed() = true")
	}
	f := Fail()
	if f.Passed || f.Failed() == false {
		t.Errorf("Fail() = %+v", f)
	}
}

func idsOf(caps []Capability) []CapabilityID {
	out := make([]CapabilityID, len(caps))
	for i, c := range caps {
		out[i] = c.ID()
	}
	return out
}

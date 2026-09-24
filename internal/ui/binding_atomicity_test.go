package ui

import (
	"context"
	"io"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/authority"
	model_picker "github.com/PizenLabs/izen/internal/ui/widgets/model_picker"
)

// stubBindingProvider is a minimal ai.Provider for binding-transition tests.
type stubBindingProvider struct{ name string }

func (p stubBindingProvider) Name() string { return p.name }

func (p stubBindingProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	return &ai.Response{}, nil
}

func (p stubBindingProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, io.EOF
}

// stubPersistBinding swaps the persistence seam so activation paths never
// touch the real home directory. It records every persisted binding.
func stubPersistBinding(t *testing.T) *[]config.ActiveBindingConfig {
	t.Helper()
	var persisted []config.ActiveBindingConfig
	old := persistActiveBindingFn
	persistActiveBindingFn = func(provider, model, variant string) error {
		persisted = append(persisted, config.ActiveBindingConfig{
			Provider: provider,
			Model:    model,
			Variant:  variant,
		})
		return nil
	}
	t.Cleanup(func() { persistActiveBindingFn = old })
	return &persisted
}

// newBindingTestModel builds a UI model starting from ollama +
// qwen2.5-coder:7b with stub providers registered, mirroring the reported
// reproduction's initial state.
func newBindingTestModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := config.Default()
	cfg.Bindings.Active = config.ActiveBindingConfig{
		Provider: "ollama",
		Model:    "qwen2.5-coder:7b",
	}
	mgr := ai.NewManager()
	mgr.Register("ollama", stubBindingProvider{name: "ollama"})
	mgr.Register("openrouter", stubBindingProvider{name: "openrouter"})
	m := &model{
		cfg:            cfg,
		mgr:            mgr,
		modelAuthority: appruntime.NewRuntimeAuthority(),
		ti:             textinput.New(),
	}
	m.modelAuthority.Activate(authority.ModelBinding{
		ProviderID: authority.ProviderID("ollama"),
		ModelID:    authority.ModelID("qwen2.5-coder:7b"),
	})
	return m
}

func assertAtomicBinding(t *testing.T, m *model, wantProvider, wantModel string) {
	t.Helper()
	auth := m.ensureModelAuthority().ActiveBinding()
	if string(auth.ProviderID) != wantProvider || string(auth.ModelID) != wantModel {
		t.Fatalf("authority binding = %s + %s, want %s + %s",
			auth.ProviderID, auth.ModelID, wantProvider, wantModel)
	}
	got := m.cfg.Bindings.Active
	if got.Provider != wantProvider || got.Model != wantModel {
		t.Fatalf("session config binding = %s + %s, want %s + %s",
			got.Provider, got.Model, wantProvider, wantModel)
	}
	if prov := m.cfg.ActiveProviderName(); prov != wantProvider {
		t.Fatalf("ActiveProviderName() = %q, want %q", prov, wantProvider)
	}
	if mdl := m.cfg.ActiveModelName(); mdl != wantModel {
		t.Fatalf("ActiveModelName() = %q, want %q", mdl, wantModel)
	}
	// The mixed states must be unrepresentable: provider and model always
	// agree on the OpenRouter vendor-slug schema.
	if (wantProvider == "ollama") == containsSlash(wantModel) {
		t.Fatalf("mixed binding reached executable state: %s + %s", wantProvider, wantModel)
	}
}

func containsSlash(s string) bool {
	for _, c := range s {
		if c == '/' {
			return true
		}
	}
	return false
}

// TestIneligibleModelRemainsBlocked pins the prior OpenRouter eligibility
// repair through the refactored activation paths:
// thinkingmachines/inkling:free (harness-only) is rejected at selection
// time, while thinkingmachines/inkling-small:free remains selectable (its
// provider-side 403 is a separate compatibility concern, not a binding
// defect).
func TestIneligibleModelRemainsBlocked(t *testing.T) {
	m := newBindingTestModel(t)
	if !m.rejectIneligibleModel("openrouter", "thinkingmachines/inkling:free") {
		t.Fatal("harness-only inkling:free must be rejected at selection time")
	}
	if m.rejectIneligibleModel("openrouter", "thinkingmachines/inkling-small:free") {
		t.Fatal("inkling-small:free must remain selectable")
	}
}

// TestPersistAndActivateBindingIsAtomic pins the core invariant: one call
// transitions ollama -> openrouter with provider, model, session config and
// authority agreeing atomically.
func TestPersistAndActivateBindingIsAtomic(t *testing.T) {
	persisted := stubPersistBinding(t)
	m := newBindingTestModel(t)

	binding := authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("anthropic/claude-3.5-sonnet"),
	}
	if err := m.persistAndActivateBinding(binding); err != nil {
		t.Fatalf("persistAndActivateBinding: %v", err)
	}
	assertAtomicBinding(t, m, "openrouter", "anthropic/claude-3.5-sonnet")
	if len(*persisted) != 1 {
		t.Fatalf("persisted %d bindings, want exactly 1", len(*persisted))
	}
}

// TestSwitchModelDirectProducesAtomicBinding covers the /model <name> path:
// it must produce the same atomic binding as picker activation, with no
// stale provider left behind.
func TestSwitchModelDirectProducesAtomicBinding(t *testing.T) {
	stubPersistBinding(t)
	m := newBindingTestModel(t)

	// Do not execute the returned provider-switch command; the synchronous
	// portion already performs the binding + provider transition.
	_ = m.switchModelDirect("anthropic/claude-3.5-sonnet")

	assertAtomicBinding(t, m, "openrouter", "anthropic/claude-3.5-sonnet")
	if m.provider == nil || m.provider.Name() != "openrouter" {
		t.Fatalf("live provider = %v, want openrouter instance", m.provider)
	}
}

// TestCommitModelAssignmentMatchesDirectPath covers the /models picker
// assignment path and requires it to converge on the identical binding the
// direct path produces.
func TestCommitModelAssignmentMatchesDirectPath(t *testing.T) {
	stubPersistBinding(t)
	m := newBindingTestModel(t)

	_ = m.commitModelAssignment(model_picker.ModelAssignmentRequestedMsg{
		Provider: "openrouter",
		ModelID:  "anthropic/claude-3.5-sonnet",
	})

	assertAtomicBinding(t, m, "openrouter", "anthropic/claude-3.5-sonnet")
}

// TestSwitchProviderMirrorsBinding covers the provider-first ordering: when
// the provider switches while a stale cross-provider model is active, the
// session config is re-seeded and mirrored instead of keeping the stale
// ollama binding.
func TestSwitchProviderMirrorsBinding(t *testing.T) {
	stubPersistBinding(t)
	m := newBindingTestModel(t)

	_ = m.switchProvider("openrouter")

	got := m.cfg.Bindings.Active
	if got.Provider != "openrouter" {
		t.Fatalf("session config provider after switch = %q, want openrouter", got.Provider)
	}
	auth := m.ensureModelAuthority().ActiveBinding()
	if string(auth.ProviderID) != got.Provider || string(auth.ModelID) != got.Model {
		t.Fatalf("authority (%s + %s) diverged from session config (%s + %s)",
			auth.ProviderID, auth.ModelID, got.Provider, got.Model)
	}
}

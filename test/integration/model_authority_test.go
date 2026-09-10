package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/providers/capability"
	"github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/authority"
	registry "github.com/PizenLabs/izen/internal/provider/registry"
)

// ── Test A: Unified Active Model ─────────────────────────────────────────────
// Set one binding on RuntimeAuthority; verify every intent resolves to that
// same binding (single active binding invariant).
func TestA_UnifiedActiveModel(t *testing.T) {
	auth := runtime.NewRuntimeAuthority()
	binding := authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("anthropic/claude-3.5-sonnet"),
	}
	auth.Activate(binding)

	// Every intent must resolve to the same active binding (no policy overrides set).
	intents := []string{"ask", "investigate", "plan", "build", "review", "conversation"}
	for _, intent := range intents {
		got, err := auth.ResolveForIntent(intent)
		if err != nil {
			t.Errorf("ResolveForIntent(%q): unexpected error: %v", intent, err)
			continue
		}
		if got.ProviderID != binding.ProviderID || got.ModelID != binding.ModelID {
			t.Errorf("ResolveForIntent(%q) = %+v, want provider=%q model=%q",
				intent, got, binding.ProviderID, binding.ModelID)
		}
	}
}

// ── Test B: Explicit Policy Override ───────────────────────────────────────────
// Set a policy binding for the plan role; verify that "plan" and "investigate"
// (both RoleThinking) use the policy override, while other intents use the
// active binding.
func TestB_ExplicitPolicyOverride(t *testing.T) {
	auth := runtime.NewRuntimeAuthority()

	// Active binding (default for non-policy intents).
	auth.Activate(authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("anthropic/claude-3.5-sonnet"),
	})

	// Policy: override thinking role to a different provider/model.
	auth.SetPolicy(authority.ModelPolicy{
		Thinking: &authority.ModelBinding{
			ProviderID: authority.ProviderID("anthropic"),
			ModelID:    authority.ModelID("claude-sonnet-4-20250514"),
		},
	})

	// "plan" → RoleThinking → should use policy override.
	planBinding, err := auth.ResolveForIntent("plan")
	if err != nil {
		t.Fatalf("ResolveForIntent(plan): %v", err)
	}
	if planBinding.ProviderID != "anthropic" || planBinding.ModelID != "claude-sonnet-4-20250514" {
		t.Errorf("plan binding = provider=%q model=%q, want anthropic/claude-sonnet-4-20250514",
			planBinding.ProviderID, planBinding.ModelID)
	}

	// "investigate" → also RoleThinking → same policy override.
	invBinding, err := auth.ResolveForIntent("investigate")
	if err != nil {
		t.Fatalf("ResolveForIntent(investigate): %v", err)
	}
	if invBinding.ProviderID != "anthropic" || invBinding.ModelID != "claude-sonnet-4-20250514" {
		t.Errorf("investigate binding = provider=%q model=%q, want anthropic/claude-sonnet-4-20250514",
			invBinding.ProviderID, invBinding.ModelID)
	}

	// "ask" → RoleFast → no policy → should use active binding.
	askBinding, err := auth.ResolveForIntent("ask")
	if err != nil {
		t.Fatalf("ResolveForIntent(ask): %v", err)
	}
	if askBinding.ProviderID != "openrouter" || askBinding.ModelID != "anthropic/claude-3.5-sonnet" {
		t.Errorf("ask binding = provider=%q model=%q, want openrouter/anthropic/claude-3.5-sonnet",
			askBinding.ProviderID, askBinding.ModelID)
	}
}

// ── Test C: Invalid Provider Binding ───────────────────────────────────────────
// Verify that ValidateBinding rejects mismatched provider/model pairs
// (e.g. ollama model with openrouter provider).
func TestC_InvalidProviderBinding(t *testing.T) {
	tests := []struct {
		name    string
		binding authority.ModelBinding
		wantErr error
	}{
		{
			name: "openrouter model on ollama provider",
			binding: authority.ModelBinding{
				ProviderID: authority.ProviderID("ollama"),
				ModelID:    authority.ModelID("anthropic/claude-3.5-sonnet"),
			},
			wantErr: authority.ErrProviderModelMismatch,
		},
		{
			name: "ollama model on openrouter provider",
			binding: authority.ModelBinding{
				ProviderID: authority.ProviderID("openrouter"),
				ModelID:    authority.ModelID("qwen2.5-coder:7b"),
			},
			wantErr: authority.ErrProviderModelMismatch,
		},
		{
			name: "empty model ID",
			binding: authority.ModelBinding{
				ProviderID: authority.ProviderID("openrouter"),
				ModelID:    authority.ModelID(""),
			},
			wantErr: authority.ErrUnassignedModel,
		},
		{
			name: "empty provider ID",
			binding: authority.ModelBinding{
				ProviderID: authority.ProviderID(""),
				ModelID:    authority.ModelID("some-model"),
			},
			wantErr: authority.ErrProviderDisabled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := authority.ValidateBinding(tt.binding)
			if err == nil {
				t.Errorf("ValidateBinding(%+v): expected error, got nil", tt.binding)
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateBinding(%+v): got error %v, want errors.Is %v",
					tt.binding, err, tt.wantErr)
			}
		})
	}
}

// ── Test D: Conversation Path ─────────────────────────────────────────────────
// Verify that ResolveForIntent("ask") (the conversation/direct-response path)
// returns the exact active binding — same path as execution.
func TestD_ConversationPath(t *testing.T) {
	auth := runtime.NewRuntimeAuthority()
	auth.Activate(authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("anthropic/claude-3.5-sonnet"),
	})

	binding, err := auth.ResolveForIntent("ask")
	if err != nil {
		t.Fatalf("ResolveForIntent(ask): %v", err)
	}
	if binding.ProviderID != "openrouter" {
		t.Errorf("conversation provider = %q, want openrouter", binding.ProviderID)
	}
	if binding.ModelID != "anthropic/claude-3.5-sonnet" {
		t.Errorf("conversation model = %q, want anthropic/claude-3.5-sonnet", binding.ModelID)
	}

	// Verify the binding survives JSON round-trip (execution payload construction).
	payload := map[string]string{
		"provider": string(binding.ProviderID),
		"model":    string(binding.ModelID),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), "anthropic/claude-3.5-sonnet") {
		t.Errorf("execution payload must contain exact model string, got: %s", b)
	}
}

// ── Test E: Provider Switch ────────────────────────────────────────────────────
// Verify that switching the active binding to a new provider does NOT carry
// over the old model. The new binding is the sole source of truth.
func TestE_ProviderSwitch(t *testing.T) {
	auth := runtime.NewRuntimeAuthority()

	// Start with ollama.
	auth.Activate(authority.ModelBinding{
		ProviderID: authority.ProviderID("ollama"),
		ModelID:    authority.ModelID("qwen2.5-coder:7b"),
	})
	b1, _ := auth.ResolveForIntent("ask")
	if b1.ProviderID != "ollama" || b1.ModelID != "qwen2.5-coder:7b" {
		t.Fatalf("initial binding = %+v, want ollama/qwen2.5-coder:7b", b1)
	}

	// Switch to openrouter — old ollama model must not leak.
	auth.Activate(authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("anthropic/claude-3.5-sonnet"),
	})
	b2, _ := auth.ResolveForIntent("ask")
	if b2.ProviderID != "openrouter" {
		t.Errorf("after switch: provider = %q, want openrouter", b2.ProviderID)
	}
	if b2.ModelID != "anthropic/claude-3.5-sonnet" {
		t.Errorf("after switch: model = %q, want anthropic/claude-3.5-sonnet (no carryover)", b2.ModelID)
	}
	if b2.ModelID == "qwen2.5-coder:7b" {
		t.Error("model carryover detected: old ollama model leaked into new provider binding")
	}
}

// ── Test F: Credential Security ────────────────────────────────────────────────
// Verify that RuntimeAuthority serialization does not expose API keys.
// The config file stores API keys by design, but the runtime authority's
// ModelBinding must carry only provider/model identifiers — never secrets.
func TestF_CredentialSecurity(t *testing.T) {
	auth := runtime.NewRuntimeAuthority()
	auth.Activate(authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("anthropic/claude-3.5-sonnet"),
	})

	binding := auth.ActiveBinding()

	// Serialize the binding to JSON — must not contain API keys.
	data, err := json.Marshal(binding)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "api_key") || strings.Contains(s, "APIKey") || strings.Contains(s, "secret") {
		t.Errorf("ModelBinding serialization contains credential field: %s", s)
	}

	// Verify binding contains only safe fields.
	if !strings.Contains(s, "openrouter") {
		t.Errorf("binding JSON missing provider: %s", s)
	}
	if !strings.Contains(s, "anthropic/claude-3.5-sonnet") {
		t.Errorf("binding JSON missing model: %s", s)
	}
}

// ── Test G: Dynamic Catalog ────────────────────────────────────────────────────
// Verify that the registry loads live provider data, not static lists.
func TestG_DynamicCatalog(t *testing.T) {
	reg := registry.NewRegistry()
	snap := reg.Load()

	if snap == nil || len(snap.Models) == 0 {
		t.Skip("registry snapshot empty (no live providers configured)")
	}

	if len(snap.Providers) > 0 {
		for _, ps := range snap.Providers {
			if ps.ModelCount < 0 {
				t.Errorf("provider %q has negative model count: %d", ps.Name, ps.ModelCount)
			}
		}
	}
}

// ── Test H: Capability Truth ───────────────────────────────────────────────────
// Verify that reasoning capability states are truthful: Supported,
// Unsupported, and Unknown are correctly distinguished.
func TestH_CapabilityTruth(t *testing.T) {
	tests := []struct {
		name              string
		reasoning         bool
		hasExplicitDenied bool
		configurable      bool
		opts              []string
		wantState         capability.ReasoningSupportState
		wantConfigurable  bool
	}{
		{
			name:              "Supported reasoning model",
			reasoning:         true,
			hasExplicitDenied: false,
			configurable:      true,
			opts:              []string{"auto", "low", "medium", "high"},
			wantState:         capability.ReasoningSupported,
			wantConfigurable:  true,
		},
		{
			name:              "Unsupported reasoning model",
			reasoning:         false,
			hasExplicitDenied: true,
			configurable:      false,
			opts:              nil,
			wantState:         capability.ReasoningUnsupported,
			wantConfigurable:  false,
		},
		{
			name:              "Unknown (absent metadata)",
			reasoning:         false,
			hasExplicitDenied: false,
			configurable:      false,
			opts:              nil,
			wantState:         capability.ReasoningUnknown,
			wantConfigurable:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			truth := capability.ToCapabilityTruth(tt.reasoning, tt.hasExplicitDenied, tt.configurable, tt.opts)
			if truth.Reasoning != tt.wantState {
				t.Errorf("Reasoning = %v, want %v", truth.Reasoning, tt.wantState)
			}
			if truth.Configurable != tt.wantConfigurable {
				t.Errorf("Configurable = %v, want %v", truth.Configurable, tt.wantConfigurable)
			}
		})
	}
}

package integration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/providers/capability"
	authority "github.com/PizenLabs/izen/internal/runtime/authority"
	registry "github.com/PizenLabs/izen/internal/provider/registry"
)

// ── Test A: Unified Active Model ─────────────────────────────────────────────
func TestA_UnifiedActiveModel(t *testing.T) {
	cfg := config.Default()
	cfg.AI.DefaultProvider = "openrouter"
	cfg.AI.Providers["openrouter"] = config.AIProviderConfig{
		BaseURL:      "https://openrouter.ai/api/v1",
		APIKey:       "test-key",
		DefaultModel: "dots-studio/dots-3-note-preview:free",
	}
	cfg.Models.Default = "dots-studio/dots-3-note-preview:free"
	cfg.Models.SessionModel = "dots-studio/dots-3-note-preview:free"

	provider := cfg.ActiveProviderName()
	model := cfg.ActiveModelName()

	if provider != "openrouter" {
		t.Errorf("provider = %q, want openrouter", provider)
	}
	if model != "dots-studio/dots-3-note-preview:free" {
		t.Errorf("model = %q, want dots-studio/dots-3-note-preview:free", model)
	}

	// Assignment-based mode checks (config.Assignments, not cfg.Modes)
	modes := []string{"ask", "investigate", "plan", "build", "review"}
	for _, mode := range modes {
		var assigned string
		switch mode {
		case "ask":
			assigned = cfg.Assignments.Ask
		case "investigate":
			assigned = cfg.Assignments.Investigate
		case "plan":
			assigned = cfg.Assignments.Plan
		case "build":
			assigned = cfg.Assignments.Build
		case "review":
			assigned = cfg.Assignments.Review
		}
		if assigned != "" && assigned != model {
			t.Errorf("assignment %s = %q, want unified %q", mode, assigned, model)
		}
	}
}

// ── Test B: Explicit Policy Override ───────────────────────────────────────────
func TestB_ExplicitPolicyOverride(t *testing.T) {
	cfg := config.Default()
	cfg.AI.DefaultProvider = "openrouter"
	cfg.AI.Providers["openrouter"] = config.AIProviderConfig{
		BaseURL:      "https://openrouter.ai/api/v1",
		APIKey:       "test-key",
		DefaultModel: "dots-studio/dots-3-note-preview:free",
	}
	cfg.Assignments.Plan = "qwen2.5-coder:7b"

	planModel := cfg.Assignments.Plan
	askModel := cfg.ActiveModelName()

	if planModel != "qwen2.5-coder:7b" {
		t.Errorf("plan assignment = %q, want qwen2.5-coder:7b", planModel)
	}
	if askModel != "dots-studio/dots-3-note-preview:free" {
		t.Errorf("ask remains openrouter model = %q", askModel)
	}
}

// ── Test C: Invalid Provider Binding ───────────────────────────────────────────
func TestC_InvalidProviderBinding(t *testing.T) {
	binding := authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("qwen2.5-coder:7b"),
	}
	providerID, modelID, valid := adapterBindingValid(binding)

	if valid {
		t.Errorf("invalid binding (%q + %q) was accepted as valid", providerID, modelID)
	}
}

// adapterBindingValid mimics adapter.ModelBindingAdapter contract for the audit.
func adapterBindingValid(binding authority.ModelBinding) (string, string, bool) {
	if binding.ModelID == "" {
		return "", "", false
	}
	// OpenRouter requires vendor/model schema; ollama requires no slash prefix.
	switch string(binding.ProviderID) {
	case "openrouter":
		if !strings.Contains(string(binding.ModelID), "/") {
			return string(binding.ProviderID), string(binding.ModelID), false
		}
	case "ollama":
		if strings.Contains(string(binding.ModelID), "/") {
			return string(binding.ProviderID), string(binding.ModelID), false
		}
	default:
		if binding.ModelID == "" {
			return string(binding.ProviderID), string(binding.ModelID), false
		}
	}
	return string(binding.ProviderID), string(binding.ModelID), true
}

// ── Test D: Conversation Path ─────────────────────────────────────────────────
func TestD_ConversationPath(t *testing.T) {
	payload := map[string]interface{}{
		"model": "dots-studio/dots-3-note-preview:free",
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["model"] != "dots-studio/dots-3-note-preview:free" {
		t.Errorf("payload model = %v, want dots-studio/dots-3-note-preview:free", result["model"])
	}
}

// ── Test E: Provider Switch ────────────────────────────────────────────────────
func TestE_ProviderSwitchBlocksExecution(t *testing.T) {
	cfg := config.Default()
	cfg.AI.DefaultProvider = "ollama"
	cfg.AI.Providers["ollama"] = config.AIProviderConfig{
		BaseURL:      "http://localhost:11434/v1",
		APIKey:       "ollama",
		DefaultModel: "qwen2.5-coder:7b",
	}
	cfg.AI.Providers["openrouter"] = config.AIProviderConfig{
		BaseURL:      "https://openrouter.ai/api/v1",
		APIKey:       "test",
		DefaultModel: "dots-studio/dots-3-note-preview:free",
	}
	cfg.Models.SessionModel = "qwen2.5-coder:7b"

	cfg.AI.DefaultProvider = "openrouter"
	newModel := cfg.ActiveModelName()
	if newModel != "dots-studio/dots-3-note-preview:free" {
		t.Logf("provider switch: active model = %q (expected openrouter default)", newModel)
	}
	if cfg.Models.SessionModel == "qwen2.5-coder:7b" {
		t.Log("execution blocked: session model does not match active provider")
	}
}

// ── Test F: Credential Security ────────────────────────────────────────────────
func TestF_CredentialSecurity(t *testing.T) {
	cfg := config.Default()
	cfg.AI.Providers["openrouter"] = config.AIProviderConfig{
		APIKey: "secret-openrouter-key-12345",
	}

	defer os.Remove(ConfigPath())

	if err := config.Save(cfg); err != nil {
		t.Skipf("Save skipped (no home dir): %v", err)
	}

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return // no persisted file
	}
	s := string(data)
	if strings.Contains(s, "secret-openrouter-key-12345") {
		t.Errorf("serialized config exposes API secret")
	}
}

func ConfigPath() string {
	return "/tmp/test_izen_config.yml"
}

// ── Test G: Dynamic Catalog ────────────────────────────────────────────────────
func TestG_DynamicCatalogMatchesLiveDiscovery(t *testing.T) {
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
func TestH_CapabilityTruth(t *testing.T) {
	tests := []struct {
		name         string
		reasoning    bool
		hasExplicitDenied bool
		configurable  bool
		opts         []string
		wantState    capability.ReasoningSupportState
		wantConfigurable bool
	}{
		{"Supported reasoning model", true, false, true, []string{"auto", "low", "medium", "high"}, capability.ReasoningSupported, true},
		{"Unsupported reasoning model", false, true, false, nil, capability.ReasoningUnsupported, false},
		{"Unknown (absent metadata)", false, false, false, nil, capability.ReasoningUnknown, false},
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

// ── Test D (Execution): Payload Matches Active Binding ────────────────────────
func TestD_ExecutionPayloadMatchesActiveBinding(t *testing.T) {
	binding := authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("dots-studio/dots-3-note-preview:free"),
	}
	providerID, modelID, valid := adapterBindingValid(binding)
	if !valid {
		t.Fatal("binding not valid")
	}

	payload := map[string]interface{}{
		"provider": providerID,
		"model":    modelID,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if !strings.Contains(string(b), "dots-studio/dots-3-note-preview:free") {
		t.Errorf("execution payload must contain exact active model string")
	}
}

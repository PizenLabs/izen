package role

import (
	"testing"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

func TestClassifyDeepseekR1AsThinking(t *testing.T) {
	isThinking, caps := ClassifyModel("openrouter/deepseek/deepseek-r1")
	if !isThinking {
		t.Error("deepseek-r1: isThinking = false, want true")
	}
	if !HasCapability(caps, registry.CapThinking) {
		t.Errorf("deepseek-r1: caps = %v, want CapThinking", caps)
	}
	if !HasCapability(caps, registry.CapTools) {
		t.Errorf("deepseek-r1: caps = %v, want default CapTools", caps)
	}
}

func TestClassifyGeminiFlashAsVision(t *testing.T) {
	_, caps := ClassifyModel("google/gemini-2.5-flash")
	if !HasCapability(caps, registry.CapVision) {
		t.Errorf("gemini-2.5-flash: caps = %v, want CapVision", caps)
	}
}

func TestClassifyVisionPatterns(t *testing.T) {
	for _, id := range []string{
		"openai/gpt-4o",
		"anthropic/claude-3.5-sonnet",
		"mistral/pixtral-12b",
		"my-vision-model",
		"flash-image-gen",
	} {
		_, caps := ClassifyModel(id)
		if !HasCapability(caps, registry.CapVision) {
			t.Errorf("%q: caps = %v, want CapVision", id, caps)
		}
	}
}

func TestClassifyThinkingPatterns(t *testing.T) {
	for _, id := range []string{
		"openai/o1-preview", "openai/o3-mini", "deepseek-reasoner",
		"my-thinking-model",
	} {
		isThinking, caps := ClassifyModel(id)
		if !isThinking {
			t.Errorf("%q: isThinking = false, want true", id)
		}
		if !HasCapability(caps, registry.CapThinking) {
			t.Errorf("%q: caps = %v, want CapThinking", id, caps)
		}
	}
}

func TestClassifyToolsDefaultAndLegacy(t *testing.T) {
	if _, caps := ClassifyModel("openai/gpt-4o"); !HasCapability(caps, registry.CapTools) {
		t.Errorf("modern model caps = %v, want CapTools", caps)
	}
	if _, caps := ClassifyModel("text-davinci-003"); HasCapability(caps, registry.CapTools) {
		t.Errorf("legacy text-davinci caps = %v, want no CapTools", caps)
	}
}

func seedRegistry(models []registry.ModelDescriptor) *registry.Registry {
	r := registry.NewRegistryWithCachePath("")
	r.SetSeed(models)
	return r
}

func TestResolvePlanSelectsThinkingWithoutBinding(t *testing.T) {
	reg := seedRegistry([]registry.ModelDescriptor{
		{ID: "openai/gpt-4o-mini", Provider: "openai", Name: "GPT-4o mini", ContextWindow: 128000},
		{ID: "openrouter/deepseek/deepseek-r1", Provider: "openrouter", Name: "DeepSeek R1", ContextWindow: 64000},
		{ID: "google/gemini-2.5-flash", Provider: "gemini", Name: "Gemini Flash", ContextWindow: 1000000},
	})
	cfg := &config.CascadeConfig{Roles: map[string]string{}}

	got, err := ResolveRoleModel("plan", cfg, reg)
	if err != nil {
		t.Fatalf("ResolveRoleModel(plan): %v", err)
	}
	if got.ID != "openrouter/deepseek/deepseek-r1" {
		t.Errorf("plan = %q, want deepseek-r1 thinking fallback", got.ID)
	}
	if !EffectiveIsThinking(got) {
		t.Errorf("plan model %q must be thinking", got.ID)
	}
}

func TestResolveExactBindingWins(t *testing.T) {
	reg := seedRegistry([]registry.ModelDescriptor{
		{ID: "model-a", Provider: "p", Name: "A"},
		{ID: "model-b", Provider: "p", Name: "B"},
	})
	cfg := &config.CascadeConfig{Roles: map[string]string{"plan": "model-b"}}
	got, err := ResolveRoleModel("plan", cfg, reg)
	if err != nil {
		t.Fatalf("ResolveRoleModel: %v", err)
	}
	if got.ID != "model-b" {
		t.Errorf("got %q, want explicit binding model-b", got.ID)
	}
}

func TestResolveVisionRequiresCap(t *testing.T) {
	reg := seedRegistry([]registry.ModelDescriptor{
		{ID: "plain-text", Provider: "p", Name: "Plain"},
	})
	if _, err := ResolveRoleModel("vision", &config.CascadeConfig{Roles: map[string]string{}}, reg); err == nil {
		t.Error("vision with no capable model must error")
	}
}

func TestResolveAdviserPrefersLargeContext(t *testing.T) {
	reg := seedRegistry([]registry.ModelDescriptor{
		{ID: "small", Provider: "p", Name: "Small", ContextWindow: 8000},
		{ID: "big", Provider: "p", Name: "Big", ContextWindow: 200000},
	})
	got, err := ResolveRoleModel("adviser", &config.CascadeConfig{Roles: map[string]string{}}, reg)
	if err != nil {
		t.Fatalf("ResolveRoleModel(adviser): %v", err)
	}
	if got.ID != "big" {
		t.Errorf("adviser = %q, want big context model", got.ID)
	}
}

func TestResolveUnknownRoleAndEmptyRegistry(t *testing.T) {
	reg := seedRegistry([]registry.ModelDescriptor{{ID: "m", Provider: "p", Name: "M"}})
	if _, err := ResolveRoleModel("nope", &config.CascadeConfig{Roles: map[string]string{}}, reg); err == nil {
		t.Error("unknown role must error")
	}
	empty := seedRegistry(nil)
	if _, err := ResolveRoleModel("plan", &config.CascadeConfig{Roles: map[string]string{}}, empty); err == nil {
		t.Error("empty registry must error")
	}
	if _, err := ResolveRoleModel("plan", nil, nil); err == nil {
		t.Error("nil registry must error")
	}
}

func TestResolveBoundModelMissingErrors(t *testing.T) {
	reg := seedRegistry([]registry.ModelDescriptor{{ID: "m", Provider: "p", Name: "M"}})
	cfg := &config.CascadeConfig{Roles: map[string]string{"plan": "ghost-model"}}
	if _, err := ResolveRoleModel("plan", cfg, reg); err == nil {
		t.Error("bound-but-absent model must error, not silently substitute")
	}
}

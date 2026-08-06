package prompt

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/domain/capability"
)

func TestResolveTierForModel(t *testing.T) {
	tests := []struct {
		model    string
		provider string
		want     capability.ModelTier
	}{
		{"cohere/north-mini-code", "openrouter", capability.TierSLM},
		{"anthropic/claude-3.7-sonnet", "openrouter", capability.TierFrontier},
		{"openai/o3", "openai", capability.TierFrontier},
		{"meta-llama/llama-3.3-70b", "openrouter", capability.TierMid},
		{"", "", capability.TierMid},
	}
	for _, tt := range tests {
		if got := ResolveTierForModel(tt.model, tt.provider); got != tt.want {
			t.Errorf("ResolveTierForModel(%q, %q) = %v, want %v", tt.model, tt.provider, got, tt.want)
		}
	}
}

func TestSLMBuildContractPositiveOnly(t *testing.T) {
	contract := SLMBuildContract()
	// Positive instructions only: zero negative/prohibitive rules.
	for _, forbid := range []string{"Do NOT", "DO NOT", "Never", "must not", "FORBIDDEN"} {
		if strings.Contains(contract, forbid) {
			t.Errorf("SLMBuildContract must not contain %q:\n%s", forbid, contract)
		}
	}
	if !strings.Contains(contract, "```") {
		t.Error("SLMBuildContract must reference markdown code fences")
	}
	if !strings.Contains(contract, SLMCoTTermination) {
		t.Error("SLMBuildContract must carry the CoT termination rule")
	}
}

func TestSLMContractsCarryCoTTermination(t *testing.T) {
	for _, c := range []string{
		NewFileContractForTier(capability.TierSLM),
		ExistingFileContractForTier(capability.TierSLM),
		SLMBuildContract(),
		PlanSynthesisSystemPromptForTier(capability.TierSLM),
	} {
		if !strings.Contains(c, "Keep reasoning under 200 tokens") {
			t.Errorf("SLM contract missing CoT termination:\n%s", c)
		}
	}
}

func TestNewFileContractForTier(t *testing.T) {
	slm := NewFileContractForTier(capability.TierSLM)
	if !strings.Contains(slm, "```") {
		t.Error("SLM new-file contract must use markdown code fences")
	}
	full := NewFileContractForTier(capability.TierFrontier)
	// The frontier variant keeps the canonical SEARCH/REPLACE-free full-file
	// instruction set from NewFileContract.
	if !strings.Contains(full, "FILE_CREATE") {
		t.Errorf("frontier new-file contract should match canonical NewFileContract, got:\n%s", full)
	}
}

func TestExistingFileContractForTier(t *testing.T) {
	slm := ExistingFileContractForTier(capability.TierSLM)
	if strings.Contains(slm, "SEARCH") {
		t.Error("SLM existing-file contract must not force the SEARCH/REPLACE protocol")
	}
	full := ExistingFileContractForTier(capability.TierFrontier)
	if !strings.Contains(full, "SEARCH") {
		t.Error("frontier existing-file contract must carry the SEARCH/REPLACE protocol")
	}
}

func TestBuildContractForTier(t *testing.T) {
	if BuildContractForTier(capability.TierSLM) != SLMBuildContract() {
		t.Error("TierSLM build contract must equal the compact SLM contract")
	}
	if BuildContractForTier(capability.TierFrontier) != BuildContract() {
		t.Error("TierFrontier build contract must equal the canonical contract")
	}
}

func TestStrategyContractForTier(t *testing.T) {
	if got := StrategyContractForTier("existing_file", capability.TierSLM); strings.Contains(got, "SEARCH") {
		t.Errorf("SLM existing_file strategy must not carry SEARCH protocol:\n%s", got)
	}
	if got := StrategyContractForTier("new_file", capability.TierSLM); !strings.Contains(got, "```") {
		t.Errorf("SLM new_file strategy must use markdown code fences:\n%s", got)
	}
	if got := StrategyContractForTier("existing_file", capability.TierFrontier); !strings.Contains(got, "SEARCH") {
		t.Errorf("frontier existing_file strategy must carry SEARCH protocol:\n%s", got)
	}
}

func TestTierAdapterSystemPromptForMode(t *testing.T) {
	slm := NewTierAdapter(capability.TierSLM)
	build := slm.SystemPromptForMode("build")
	if !strings.Contains(build, "Keep reasoning under 200 tokens") {
		t.Errorf("SLM build prompt missing CoT termination:\n%s", build)
	}
	if !strings.Contains(build, "You are IZEN") {
		t.Errorf("SLM build prompt missing identity header:\n%s", build)
	}

	frontier := NewTierAdapter(capability.TierFrontier)
	full := frontier.SystemPromptForMode("build")
	if !strings.Contains(full, "SEARCH/REPLACE") {
		t.Errorf("frontier build prompt must carry SEARCH/REPLACE protocol:\n%s", full)
	}
	if strings.Contains(full, "Keep reasoning under 200 tokens") {
		t.Error("frontier build prompt must not carry the SLM CoT termination rule")
	}
}

func TestTierAdapterComposeTiered(t *testing.T) {
	slm := NewTierAdapter(capability.TierSLM)
	out := slm.ComposeTiered("MODE: TEST", RuntimeFacts{Username: "Dev", HostOS: "darwin"})
	for _, want := range []string{"You are IZEN", "MODE: TEST", "Keep reasoning under 200 tokens"} {
		if !strings.Contains(out, want) {
			t.Errorf("ComposeTiered missing %q:\n%s", want, out)
		}
	}
}

func TestUnknownTierFallsBackToMid(t *testing.T) {
	adapter := NewTierAdapter(capability.ModelTier(99))
	if adapter.Tier() != capability.TierMid {
		t.Errorf("unknown tier should fall back to TierMid, got %v", adapter.Tier())
	}
}

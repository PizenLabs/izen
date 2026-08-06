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
		if !strings.Contains(c, "Keep internal thinking under 200 tokens") {
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
	if !strings.Contains(build, "Keep internal thinking under 200 tokens") {
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
	if strings.Contains(full, "Keep internal thinking under 200 tokens") {
		t.Error("frontier build prompt must not carry the SLM CoT termination rule")
	}
}

func TestTierAdapterComposeTiered(t *testing.T) {
	slm := NewTierAdapter(capability.TierSLM)
	out := slm.ComposeTiered("MODE: TEST", RuntimeFacts{Username: "Dev", HostOS: "darwin"})
	for _, want := range []string{"You are IZEN", "MODE: TEST", "Keep internal thinking under 200 tokens"} {
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

// TestTierSLMFastTrackPromptStripsJSONSchemas pins the "One Question, One
// Owner" guardrail: the TierSLM fast-track prompt must contain ZERO native
// tool / JSON schemas (no write_file, no apply_patch, no tool-call syntax) and
// must instead enforce standard markdown code blocks tagged ```lang:path with
// the CoT termination rule injected, so a small model emits raw code fences
// within seconds instead of looping on tool-call JSON.
func TestTierSLMFastTrackPromptStripsJSONSchemas(t *testing.T) {
	fileCtx := "## Current Content of: cmd/api/main.go\n```go\npackage main\n```\n"
	goals := "Task 1 [FILE_MUTATE]\nTarget file: cmd/api/main.go\nDescription: add a handler\n"
	p := FastTrackPromptForTier(capability.TierSLM, fileCtx, goals)

	for _, forbidden := range []string{
		"write_file",
		"apply_patch",
		`"tools"`,
		`"type": "function"`,
		`{"action"`,
		"native tool",
	} {
		if strings.Contains(p, forbidden) {
			t.Errorf("TierSLM fast-track prompt must not contain %q:\n%s", forbidden, p)
		}
	}

	if !strings.Contains(p, "```html:index.html") {
		t.Errorf("TierSLM fast-track prompt must enforce ```lang:path markdown blocks:\n%s", p)
	}
	if !strings.Contains(p, SLMCoTTermination) {
		t.Errorf("TierSLM fast-track prompt must carry the CoT termination rule:\n%s", p)
	}
	if !strings.Contains(p, fileCtx) {
		t.Errorf("TierSLM fast-track prompt must preserve the injected file context:\n%s", p)
	}
	if !strings.Contains(p, goals) {
		t.Errorf("TierSLM fast-track prompt must preserve the file operations:\n%s", p)
	}
}

// TestFastTrackPromptForTier_TierAware asserts the tier split: Mid/Frontier keep
// the native write_file / apply_patch tool protocol while TierSLM strips it.
func TestFastTrackPromptForTier_TierAware(t *testing.T) {
	fileCtx := "## Current Content of: x.go\n```go\n// x\n```\n"
	goals := "Task 1 [FILE_MUTATE]\nTarget file: x.go\nDescription: change x\n"

	slm := FastTrackPromptForTier(capability.TierSLM, fileCtx, goals)
	if strings.Contains(slm, "write_file") || strings.Contains(slm, "apply_patch") {
		t.Errorf("TierSLM prompt must not reference native tools:\n%s", slm)
	}
	if !strings.Contains(slm, "markdown code block") {
		t.Errorf("TierSLM prompt must instruct markdown code blocks:\n%s", slm)
	}

	frontier := FastTrackPromptForTier(capability.TierFrontier, fileCtx, goals)
	for _, want := range []string{"write_file", "apply_patch", "native tool calls"} {
		if !strings.Contains(frontier, want) {
			t.Errorf("TierFrontier prompt must keep the native tool protocol (%q):\n%s", want, frontier)
		}
	}

	// The Mid/Frontier prompt must be byte-identical to the pre-tier-split
	// fast-track prompt so existing Mid/Frontier build behavior is unchanged.
	mid := FastTrackPromptForTier(capability.TierMid, fileCtx, goals)
	if mid != frontier {
		t.Error("TierMid and TierFrontier fast-track prompts must be identical")
	}
	wantNative := "Execute ALL of the following file operations in a single unified session.\n\n" +
		"Use native write_file (for new files) and apply_patch (for existing files) tools ONLY.\n" +
		"Do NOT output SEARCH/REPLACE blocks, unified diffs, or markdown code blocks.\n" +
		"Do NOT include any conversational text, explanations, or summaries.\n" +
		"Output ONLY native tool calls.\n\n" +
		"## File Operations\n\n" + goals
	if frontier != fileCtx+"\n"+wantNative {
		t.Errorf("TierMid/Frontier fast-track prompt must equal the legacy prompt exactly:\n%s", frontier)
	}
}

// TestNativeToolsForTier pins the tool-schema gating decision: only TierSLM
// omits native tool schemas from fast-track requests.
func TestNativeToolsForTier(t *testing.T) {
	for tier, want := range map[capability.ModelTier]bool{
		capability.TierSLM:      false,
		capability.TierMid:      true,
		capability.TierFrontier: true,
	} {
		if got := NativeToolsForTier(tier); got != want {
			t.Errorf("NativeToolsForTier(%v) = %v, want %v", tier, got, want)
		}
	}
}

// TestSLMOutputDirectiveEnforcesLangPath ensures the shared SLM output
// directive (appended to every composed SLM system prompt) enforces the
// lang:path code-block form and carries the CoT termination rule.
func TestSLMOutputDirectiveEnforcesLangPath(t *testing.T) {
	if !strings.Contains(SLMOutputDirective, "```html:index.html") {
		t.Errorf("SLMOutputDirective must enforce lang:path markdown blocks:\n%s", SLMOutputDirective)
	}
	if !strings.Contains(SLMOutputDirective, SLMCoTTermination) {
		t.Errorf("SLMOutputDirective must carry the CoT termination rule:\n%s", SLMOutputDirective)
	}
}

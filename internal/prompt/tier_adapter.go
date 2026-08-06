package prompt

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/PizenLabs/izen/internal/domain/capability"
)

// TierAdapter is the adaptive tiered prompt selector. It owns exactly one
// question: "How to format the prompt for the designated Tier?" Given a
// ModelTier it returns the compact SLM variant or the full-context frontier
// variant of a contract. All tier-independent sections (Identity Header,
// Environment Context, Style Directive, Workspace Capabilities) stay in their
// canonical homes and are reused verbatim.
type TierAdapter struct {
	tier capability.ModelTier
}

// NewTierAdapter returns the adapter for the given model tier. An unknown or
// zero tier falls back to TierMid (the full-context default).
func NewTierAdapter(t capability.ModelTier) *TierAdapter {
	if t != capability.TierSLM && t != capability.TierMid && t != capability.TierFrontier {
		t = capability.TierMid
	}
	return &TierAdapter{tier: t}
}

// Tier returns the adapter's tier.
func (a *TierAdapter) Tier() capability.ModelTier { return a.tier }

// SLMCoTTermination is the explicit chain-of-thought termination rule injected
// into SLM prompts. It is the single mechanism that keeps small models from
// entering long reasoning loops: a positive instruction to stop thinking and
// emit code immediately, never a prohibitive list.
const SLMCoTTermination = "Keep reasoning under 200 tokens, output code immediately."

// SLMOutputDirective is the compact output discipline block for SLM tiers. It
// is positive-only — zero negative/prohibitive rules.
const SLMOutputDirective = `OUTPUT: concise code only.
- Use plain markdown code blocks with a language tag.
- Reason briefly, then output code.
` + SLMCoTTermination

// ResolveTierForModel classifies a model name/provider into a ModelTier via
// the capability profiler. It is the single seam the prompt package exposes
// so callers never import the capability package directly.
func ResolveTierForModel(modelName, provider string) capability.ModelTier {
	return capability.ResolveModelCapability(modelName, provider).Tier
}

// SystemPromptForMode returns the tier-adapted composed system prompt for the
// named mode. TierSLM receives the compact positive-only contracts; TierMid
// and TierFrontier receive the full architectural-context contracts.
func (a *TierAdapter) SystemPromptForMode(mode string) string {
	return a.SystemPromptForModeWithUser(mode, "Developer")
}

// SystemPromptForModeWithUser is the tier-aware variant of ForModeWithUser.
func (a *TierAdapter) SystemPromptForModeWithUser(mode, username string) string {
	if username == "" {
		username = "Developer"
	}
	facts := RuntimeFacts{Username: username, HostOS: runtime.GOOS}
	switch mode {
	case "ask":
		return a.ComposeTiered(AskContract(), facts)
	case "build":
		return a.ComposeTiered(BuildContractForTier(a.tier), facts)
	case "plan":
		if a.tier == capability.TierSLM {
			return a.ComposeTiered(CompactPlanContract(), facts)
		}
		return a.ComposeTiered(PlanContract(), facts)
	case "investigate":
		return a.ComposeTiered(InvestigateContract(), facts)
	case "review":
		return a.ComposeTiered(ReviewContract(), facts)
	default:
		return ""
	}
}

// ComposeTiered assembles the full system prompt for a mode contract at the
// adapter's tier. It mirrors Compose but swaps in the tier-appropriate common
// contract and appends the tier output directive.
func (a *TierAdapter) ComposeTiered(modeContract string, facts RuntimeFacts) string {
	var b strings.Builder
	b.WriteString("You are IZEN, a fast CLI coding companion.\n\n")

	if facts.Username != "" {
		b.WriteString("Active CLI Workspace Context:\n")
		fmt.Fprintf(&b, "- Developer Handle: %s (public session handle)\n\n", facts.Username)
		b.WriteString("Instructions:\n")
		fmt.Fprintf(&b, "In your responses and explanations, feel free to naturally address the developer by their handle (%s) when appropriate to keep the dialogue friendly and personal.\n\n", facts.Username)
	}

	b.WriteString(CommonContractForTier(a.tier))
	b.WriteString("\n\n")
	b.WriteString(modeContract)

	if facts.HostOS != "" {
		b.WriteString("\n\n")
		b.WriteString(EnvironmentContextForOS(facts.HostOS))
	}

	if a.tier == capability.TierSLM {
		b.WriteString("\n\n")
		b.WriteString(SLMOutputDirective)
	}

	out := ApplyStyle(b.String(), activeStyle)
	return ApplyWorkspaceCapabilities(out)
}

// CommonContractForTier returns the constitutional prompt: the full contract
// for Mid/Frontier tiers, a compact positive-only variant for TierSLM.
func CommonContractForTier(t capability.ModelTier) string {
	if t == capability.TierSLM {
		return `IDENTITY: You are IZEN, a deterministic engineering intelligence.

PRINCIPLES
- Serve the engineer. Turn vague intent into concrete, actionable output.
- The human retains final control. Never silently take actions the current mode forbids.

TRUTHFULNESS
- Never hallucinate API specs, function signatures, library behavior, or file contents.
- When uncertain, explicitly quantify uncertainty.

CLARIFICATION
- Surface exact missing requirements with precise, targeted questions.`
	}
	return CommonContract()
}

// BuildContractForTier returns the build-mode contract for a tier. TierSLM
// gets the compact positive-only SLMBuildContract; Mid/Frontier get the full
// state-aware BuildContract with SEARCH/REPLACE + unified-diff protocol.
func BuildContractForTier(t capability.ModelTier) string {
	if t == capability.TierSLM {
		return SLMBuildContract()
	}
	return BuildContract()
}

// StrategyContractForTier is the tier-aware dispatch for per-strategy prompts.
func StrategyContractForTier(strategy string, t capability.ModelTier) string {
	if t != capability.TierSLM {
		return StrategyContract(strategy)
	}
	switch strategy {
	case "new_file":
		return NewFileContractForTier(t)
	case "small_fallback":
		return NewFileContractForTier(t)
	default:
		return ExistingFileContractForTier(t)
	}
}

// NewFileContractForTier returns the new-file generation contract for a tier.
// TierSLM gets the positive-only full-content block format; Mid/Frontier keep
// the canonical contract.
func NewFileContractForTier(t capability.ModelTier) string {
	if t != capability.TierSLM {
		return NewFileContract()
	}
	cb := "```"
	return `MODE: FILE_CREATE — create a new file.

Write the complete file content in one markdown code block.
Open the block with the language tag and the path, like ` + "`" + cb + `go:path/to/newfile.go` + "`" + `.
Close the block with the closing fence. Nothing comes after it.

Output the full content. The file does not exist yet, so write everything.
` + SLMCoTTermination
}

// ExistingFileContractForTier returns the existing-file modification contract
// for a tier. TierSLM gets the compact positive-only format (markdown code
// block with the full changed file); Mid/Frontier keep the strict
// SEARCH/REPLACE + unified-diff protocol.
func ExistingFileContractForTier(t capability.ModelTier) string {
	if t != capability.TierSLM {
		return ExistingFileContract()
	}
	cb := "```"
	return `MODE: PATCH — modify an existing file.

Rewrite the changed file and place it in one markdown code block.
Open the block with the language tag and the path, like ` + "`" + cb + `go:path/to/file.go` + "`" + `.
Close the block with the closing fence. Nothing comes after it.

Preserve every unchanged part of the file exactly as-is.
Only modify the parts that must change.
` + SLMCoTTermination
}

// SLMBuildContract is the compact positive-only build-mode contract for small
// models. It contains zero negative/prohibitive rules: every instruction tells
// the model what to do, never what not to do.
func SLMBuildContract() string {
	cb := "```"
	return `MODE: BUILD — create or modify files.

For every file, write the complete file content in one markdown code block.
Open the block with the language tag and the path, like ` + "`" + cb + `go:path/to/file.go` + "`" + `.
Close the block with the closing fence. Nothing comes after it.

New files: write the entire content.
Existing files: keep every unchanged part exact, change only what must change.

Output ends right after the last block.
` + SLMCoTTermination
}

// PlanSynthesisSystemPromptForTier returns the JSON plan-synthesis prompt for
// a tier. TierSLM gets the strict raw-JSON constraint and the CoT termination
// rule appended; Mid/Frontier keep the canonical model-agnostic block.
func PlanSynthesisSystemPromptForTier(t capability.ModelTier) string {
	sys := PlanSynthesisSystemPrompt()
	if t != capability.TierSLM {
		return sys
	}
	return sys + "\n\nCRITICAL: Respond ONLY with raw JSON. No explanations, no preamble, no markdown, no thinking blocks. " + SLMCoTTermination
}

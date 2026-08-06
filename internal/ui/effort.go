package ui

import (
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/domain/intent"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/pkg/engine/decision"
)

// promptTier classifies the currently routed model into a capability tier. It
// is the single seam the UI uses to select tier-adapted prompts.
func (m *model) promptTier() capability.ModelTier {
	return capability.ResolveModelCapability(m.activeModelName(), m.activeProviderName()).Tier
}

// reasoningForRequest resolves the dynamic effort directive for the current
// model and effort setting, then translates it into the provider-agnostic
// reasoning payload. rawPrompt is the task text the complexity heuristic reads
// (the user objective / staged task descriptions). When no reasoning control
// is warranted, a nil payload is returned so providers behave exactly as
// before.
func (m *model) reasoningForRequest(rawPrompt string) *ai.ReasoningConfig {
	cap := capability.ResolveModelCapability(m.activeModelName(), m.activeProviderName())
	in := intent.New(rawPrompt)
	effort := decision.ResolveEffortConfig(in, cap, m.currentEffort.String())
	if effort.Level == "" && effort.BudgetTokens == 0 && effort.CoTLimit == 0 {
		return nil
	}
	return &ai.ReasoningConfig{
		Level:        effort.Level,
		BudgetTokens: effort.BudgetTokens,
		CoTLimit:     effort.CoTLimit,
	}
}

// effortFromTasks resolves the effort directive from the staged task list,
// falling back to the raw input when no tasks are staged. The staged task
// descriptions carry the complexity signal for multi-file / architectural
// builds.
func (m *model) effortFromTasks() *ai.ReasoningConfig {
	var sb strings.Builder
	if m.sess != nil {
		for _, t := range m.sess.CurrentTasks {
			if t.Description != "" {
				sb.WriteString(t.Description)
				sb.WriteString("\n")
			}
		}
	}
	if sb.Len() == 0 {
		sb.WriteString(m.input.String())
	}
	return m.reasoningForRequest(sb.String())
}

// tieredModePrompt returns the tier-adapted composed system prompt for the
// given mode (ask/build/plan/investigate/review).
func (m *model) tieredModePrompt(mode string) string {
	return prompt.NewTierAdapter(m.promptTier()).SystemPromptForModeWithUser(mode, m.userName)
}

// tieredStrategyContract returns the tier-adapted strategy prompt.
func (m *model) tieredStrategyContract(strategy string) string {
	return prompt.StrategyContractForTier(strategy, m.promptTier())
}

// fileMutationTools returns the native write_file / apply_patch tool schemas
// for build requests, or nil for TierSLM. An SLM's fast-track prompt strips
// every JSON tool definition and enforces plain markdown code blocks; shipping
// the native tool schemas alongside would push the small model straight back
// into tool-call JSON syntax paralysis instead of raw code fences.
func (m *model) fileMutationTools() []ai.ToolDefinition {
	if !prompt.NativeToolsForTier(m.promptTier()) {
		return nil
	}
	return ai.FileMutationTools()
}

func (m *model) activeModelName() string {
	if name := m.activeRouteModel(); name != "" {
		return name
	}
	if m.cfg != nil {
		return m.cfg.ActiveModelName()
	}
	return ""
}

func (m *model) activeProviderName() string {
	if m.provider != nil {
		return m.provider.Name()
	}
	if m.cfg != nil {
		return m.cfg.ActiveProviderName()
	}
	return ""
}

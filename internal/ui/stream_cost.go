package ui

import (
	"strings"

	"github.com/PizenLabs/izen/internal/llm"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

// ── Real-time streaming cost telemetry ─────────────────────────────────────
// Baseline (t=0) + live burn loop for the EXECUTING footer bar:
//
//	C_in  = T_in * P_input / 1_000_000
//	C_est = (T_in * P_input + T_out * P_output) / 1_000_000
//
// T_in is the prompt-token baseline estimated at stream start (chars/4 over
// the assembled request) and replaced by the provider's authoritative input
// count when a streamUsageMsg arrives. T_out is the live output count:
// max(authoritative stage tokens, per-chunk live estimate) so the meter
// advances on every chunk even before provider usage lands. Pricing comes
// from the provider model registry with llm catalog fallback; zero means
// free and renders as $free.

// estimatePromptTokens converts prompt characters into a token baseline
// (~4 chars per token, minimum 1 per non-empty prompt) mirroring the local
// estimate fallback and estimateStreamTokens.
func estimatePromptTokens(s string) int {
	if s == "" {
		return 0
	}
	n := len(s) / 4
	if n < 1 {
		n = 1
	}
	return n
}

// matchRegistryModel finds a descriptor by exact ID, case-insensitive ID,
// or bare suffix after the vendor slash (openrouter "vendor/model" slugs).
func matchRegistryModel(snap []registry.ModelDescriptor, modelID string) (registry.ModelDescriptor, bool) {
	trimmed := strings.TrimSpace(modelID)
	for _, d := range snap {
		if d.ID == trimmed {
			return d, true
		}
	}
	for _, d := range snap {
		if strings.EqualFold(d.ID, trimmed) {
			return d, true
		}
	}
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		bare := trimmed[idx+1:]
		for _, d := range snap {
			if strings.EqualFold(d.ID, bare) {
				return d, true
			}
		}
	}
	return registry.ModelDescriptor{}, false
}

// lookupStreamPricing resolves (P_input, P_output) per 1M tokens for the
// active model. It prefers the provider registry RAM snapshot (live catalog
// pricing) and falls back to the curated llm catalog. Free-suffixed models
// and ollama always resolve to zero.
func (m *model) lookupStreamPricing(modelID string) (float64, float64) {
	if modelID == "" {
		return 0, 0
	}
	lower := strings.ToLower(strings.TrimSpace(modelID))
	if strings.HasSuffix(lower, ":free") {
		return 0, 0
	}
	if m.cfg != nil {
		if prov := m.cfg.ActiveProviderName(); prov == "ollama" {
			return 0, 0
		}
	}
	// Live registry first (authoritative per-model pricing when synced).
	if m.modelRegistry != nil {
		snap := m.modelRegistry.Snapshot()
		if desc, ok := matchRegistryModel(snap, modelID); ok {
			if desc.InputCostPerM != 0 || desc.OutputCostPerM != 0 {
				return desc.InputCostPerM, desc.OutputCostPerM
			}
		}
	}
	if meta := llm.GetModelMetadata(modelID); meta != nil {
		return meta.InputCostPerM, meta.OutputCostPerM
	}
	if idx := strings.LastIndex(modelID, "/"); idx >= 0 {
		if meta := llm.GetModelMetadata(modelID[idx+1:]); meta != nil {
			return meta.InputCostPerM, meta.OutputCostPerM
		}
	}
	return 0, 0
}

// initStreamCostTelemetry captures the t=0 baseline: pricing rates for the
// active model plus the prompt-token estimate. It resets the live output
// estimate contract (caller resets streamLiveTokens alongside) so the footer
// renders "Generating... 0 tok ($C_in) 0.0 tok/s" before the first chunk.
func (m *model) initStreamCostTelemetry(promptChars int) {
	modelID := m.getActiveModelName()
	inPerM, outPerM := m.lookupStreamPricing(modelID)
	m.streamInputPricePerM = inPerM
	m.streamOutputPricePerM = outPerM
	if promptChars <= 0 {
		m.streamBaseInputTokens = 0
		return
	}
	n := promptChars / 4
	if n < 1 {
		n = 1
	}
	m.streamBaseInputTokens = n
}

// streamLiveOutputTokens returns the live billed-output count T_out: the max
// of the authoritative provider-reported stage count and the per-chunk live
// estimate (which includes reasoning/thinking that streams before usage).
func (m *model) streamLiveOutputTokens() int {
	out := m.streamLiveTokens
	if st := m.stageSnapshot(); st.Tokens > out {
		out = st.Tokens
	}
	if out < 0 {
		out = 0
	}
	return out
}

// streamEstimatedCost returns C_est for the active turn. When no pricing is
// known the result is 0 (renders as $free).
func (m *model) streamEstimatedCost() float64 {
	inT := m.streamBaseInputTokens
	if inT < 0 {
		inT = 0
	}
	outT := m.streamLiveOutputTokens()
	cost := (float64(inT)*m.streamInputPricePerM + float64(outT)*m.streamOutputPricePerM) / 1_000_000
	if m.cfg != nil {
		cost = llm.EnforceFreeModelOverride(m.cfg.ActiveModelName(), cost)
	} else {
		cost = llm.EnforceFreeModelOverride(m.getActiveModelName(), cost)
	}
	if cost < 0 {
		return 0
	}
	return cost
}

// streamCostLabel renders C_est via the canonical cost formatter ($free at 0).
func (m *model) streamCostLabel() string {
	return llm.FormatCost(m.streamEstimatedCost())
}

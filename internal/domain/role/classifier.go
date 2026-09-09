// Package role implements the Phase 2 capability classifier and role
// resolution engine.
//
// The classifier is a heuristic pattern matcher over model IDs: it detects
// thinking/reasoning models, vision-capable models, and tool-capable modern
// LLMs. The manager resolves Izen's 5 core operational roles (default, plan,
// smol, vision, adviser) against explicit CascadeConfig bindings first, then
// falls back to capability heuristics over the in-memory registry.
package role

import (
	"strings"

	"github.com/PizenLabs/izen/internal/provider/registry"
)

// thinkingPatterns match reasoning/thinking model IDs (case-insensitive
// substring). "r1" covers deepseek-r1 et al; "o1"/"o3" cover OpenAI reasoning
// series; "thinking" and "reasoner" cover explicit naming.
var thinkingPatterns = []string{"r1", "thinking", "o1", "o3", "reasoner"}

// visionPatterns match vision-capable model IDs (case-insensitive substring).
// The spec patterns (vision, claude-3, flash-image, gpt-4o, pixtral) are all
// covered; "flash" generalizes "flash-image" so gemini-flash variants classify
// as vision, and "gemini" reflects the vision-native Gemini family (required
// by the google/gemini-2.5-flash verification case).
var visionPatterns = []string{"vision", "claude-3", "flash", "gpt-4o", "pixtral", "gemini"}

// legacyTextOnlyPatterns match explicitly flagged legacy text-only endpoints
// that do NOT support tool calling. Everything else defaults to CapTools.
var legacyTextOnlyPatterns = []string{
	"text-davinci", "text-ada", "text-babbage", "text-curie",
	"ada:", "babbage:", "curie:", "davinci:",
	"gpt-3.5-turbo-instruct",
}

// ClassifyModel heuristically classifies a model ID into its thinking flag
// and capability set.
//
//   - isThinking / CapThinking: ID contains r1, thinking, o1, o3, reasoner.
//   - CapVision: ID contains vision, claude-3, flash-image, gpt-4o, pixtral.
//   - CapTools: default for modern LLMs except legacy text-only endpoints.
//
// Matching is case-insensitive. Capabilities are returned in stable order
// (thinking, vision, tools) with no duplicates.
func ClassifyModel(id string) (isThinking bool, caps []registry.ModelCapability) {
	lower := strings.ToLower(strings.TrimSpace(id))
	if lower == "" {
		return false, []registry.ModelCapability{registry.CapTools}
	}
	var out []registry.ModelCapability
	if containsAny(lower, thinkingPatterns) {
		isThinking = true
		out = append(out, registry.CapThinking)
	}
	if containsAny(lower, visionPatterns) {
		out = append(out, registry.CapVision)
	}
	if !containsAny(lower, legacyTextOnlyPatterns) {
		out = append(out, registry.CapTools)
	}
	if out == nil {
		out = []registry.ModelCapability{}
	}
	return isThinking, out
}

// HasCapability reports whether caps contains cap.
func HasCapability(caps []registry.ModelCapability, cap registry.ModelCapability) bool {
	for _, c := range caps {
		if c == cap {
			return true
		}
	}
	return false
}

// EnrichDescriptor returns a copy of d with classifier-derived IsThinking and
// Capabilities filled in when the descriptor does not already carry them.
// Explicit registry data always wins; classification only fills gaps.
func EnrichDescriptor(d registry.ModelDescriptor) registry.ModelDescriptor {
	isThinking, caps := ClassifyModel(d.ID)
	if !d.IsThinking {
		d.IsThinking = isThinking
	}
	if len(d.Capabilities) == 0 && len(caps) > 0 {
		d.Capabilities = caps
	}
	return d
}

// EffectiveCapabilities returns d.Capabilities when present, otherwise the
// classifier-derived set for d.ID. It never returns nil.
func EffectiveCapabilities(d registry.ModelDescriptor) []registry.ModelCapability {
	if len(d.Capabilities) > 0 {
		return d.Capabilities
	}
	_, caps := ClassifyModel(d.ID)
	return caps
}

// EffectiveIsThinking returns d.IsThinking when true, otherwise the
// classifier-derived flag for d.ID.
func EffectiveIsThinking(d registry.ModelDescriptor) bool {
	if d.IsThinking {
		return true
	}
	isThinking, _ := ClassifyModel(d.ID)
	return isThinking
}

func containsAny(lower string, patterns []string) bool {
	for _, p := range patterns {
		if p != "" && strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

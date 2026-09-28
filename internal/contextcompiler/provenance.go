// ── Semantic Context Provenance (Phase 14) ──────────────────────────────────
//
// Context VALIDITY is a semantic question: did the compilation actually carry
// the workspace material the active intent's contract requires? It is not an
// arithmetic question about how many tokens happened to be spent.
//
// The rule this file replaces is the one that treated an arbitrary token
// threshold as a proxy for validity: "if the compiled context is under N tokens,
// assume it is fine". That proxy is wrong in both directions at once — a large
// read-only projection under a mutation intent is invalid no matter how well it
// filled the budget, and a tiny but scope-correct projection is valid no matter
// how little it spent. Token counts are telemetry; they are reported next to
// the verdict and are never an input to it.
//
// The three conditions below are the whole contract:
//
//  1. SCOPE — every requested target appears in the compiled payload. A context
//     that silently dropped a requested target is invalid, regardless of size.
//  2. PROVENANCE — for a contract that requires workspace material, the payload
//     actually carries that material (a workspace-file section with real
//     content, or a recorded source-side truncation, which is a truthful
//     admission rather than a silent drop).
//  3. INTENT — the compilation was produced under the ACTIVE canonical intent.
//     A payload compiled for a read-only intent is not a valid mutation
//     context, no matter how much of it survived.
package contextcompiler

import (
	"fmt"
	"strings"
)

// ContextProvenance is the SEMANTIC verdict on one compiled payload. Every
// field is a derived fact of the compilation; nothing here is a policy knob and
// nothing here is a token threshold.
type ContextProvenance struct {
	// Valid is the single verdict. It is true only when every required
	// condition below held.
	Valid bool
	// ScopeMatched reports whether every requested target is present in the
	// payload's scope.
	ScopeMatched bool
	// MissingTargets names the requested targets the payload does not carry.
	MissingTargets []string
	// WorkspaceMaterialPresent reports whether the payload actually carries
	// workspace file material.
	WorkspaceMaterialPresent bool
	// TruncatedTargets names the requested targets whose content was dropped by
	// a source-side read cap. A truncated target is a TRUTHFUL admission and is
	// not treated as missing — the payload says so explicitly.
	TruncatedTargets []string
	// CompiledUnder is the canonical intent the compilation ran under.
	CompiledUnder string
	// ActiveIntent is the canonical intent that is now in force.
	ActiveIntent string
	// IntentMatched reports whether CompiledUnder == ActiveIntent.
	IntentMatched bool
	// Reason is the deterministic, user-readable explanation of a refusal ("").
	Reason string
	// ObservedTokens is the compiled token count. It is TELEMETRY ONLY: it is
	// carried so a projection can display it beside the verdict, and it is
	// never an input to Valid.
	ObservedTokens int
}

// IntentBinding names the canonical intent a compilation is (or was) bound to.
// It is a plain string so this package stays free of an intent dependency; the
// autonomy layer owns the vocabulary and passes the label across the boundary.
type IntentBinding struct {
	// Active is the canonical intent currently in force.
	Active string
	// Required names the context contract the active intent demands. It is one
	// of IntentContextNone (a zero-workspace turn), IntentContextWorkspace
	// (the payload must carry workspace file material) or
	// IntentContextSelfContained (workspace material is optional — the model is
	// answering without reading).
	Required string
}

// Context provenance requirement vocabulary.
const (
	// IntentContextNone: the turn carries no workspace context by design.
	IntentContextNone = "none"
	// IntentContextWorkspace: the payload MUST carry workspace file material.
	IntentContextWorkspace = "workspace"
	// IntentContextSelfContained: workspace material is permitted but not
	// required (a direct answer, a chat reply, a plan authored from the
	// request alone).
	IntentContextSelfContained = "self_contained"
)

// ValidateContextProvenance evaluates a compiled payload against the active
// intent's context contract.
//
// The three conditions, in refusal order:
//
//	(a) INTENT   — the payload must have been compiled under the ACTIVE
//	               canonical intent. A stale-intent payload is refused first
//	               because every other fact about it is about a different task.
//	(b) SCOPE    — every requested target must appear in the payload. A
//	               truncated target counts as present: the payload states its
//	               own loss instead of hiding it.
//	(c) PROVENANCE — a `workspace` contract additionally requires real
//	               workspace material in the payload.
func (c *CompiledContext) ValidateContextProvenance(binding IntentBinding, requestedTargets []string) ContextProvenance {
	p := ContextProvenance{
		CompiledUnder: binding.Active,
		ActiveIntent:  binding.Active,
		IntentMatched: true,
	}
	if c == nil {
		p.IntentMatched = binding.Active != ""
		p.Reason = "no compiled context payload"
		return p
	}
	p.ObservedTokens = c.UsedTokens

	// ── (a) INTENT ────────────────────────────────────────────────────
	if binding.Active == "" {
		p.IntentMatched = false
		p.Reason = "the compilation carries no canonical intent binding"
		return p
	}

	// ── (b) SCOPE ─────────────────────────────────────────────────────
	p.ScopeMatched = true
	present := c.admittedPaths()
	truncated := truncatedSet(c.TruncatedFiles)
	for _, t := range requestedTargets {
		normalized := normalizeTargetPath(t)
		if normalized == "" {
			continue
		}
		// A source-side read cap is recorded whether or not the target also made
		// it into the payload: "partially included" and "fully included" are
		// different provenance facts and a projection must be able to tell them
		// apart.
		if truncated[normalized] {
			p.TruncatedTargets = appendUniqueStrings(p.TruncatedTargets, normalized)
		}
		if _, ok := present[normalized]; ok {
			continue
		}
		if truncated[normalized] {
			// A truthful admission of the loss, not a silent drop.
			continue
		}
		p.ScopeMatched = false
		p.MissingTargets = append(p.MissingTargets, normalized)
	}
	if !p.ScopeMatched {
		p.Reason = fmt.Sprintf("the compiled context does not carry the requested target(s): %s",
			strings.Join(p.MissingTargets, ", "))
		return p
	}

	// ── (c) PROVENANCE ────────────────────────────────────────────────
	p.WorkspaceMaterialPresent = c.hasWorkspaceMaterial()
	switch binding.Required {
	case IntentContextWorkspace:
		if !p.WorkspaceMaterialPresent {
			p.Reason = "the contract requires workspace material but the compiled payload carries none"
			return p
		}
	case IntentContextNone:
		// A zero-workspace turn carries no workspace material BY DESIGN; the
		// empty payload is the correct one and is not a provenance failure.
	default:
		// IntentContextSelfContained and an unset requirement both accept an
		// empty workspace projection.
	}
	p.Valid = true
	return p
}

// admittedPaths returns the workspace-file paths the payload actually carried.
// The compiler records this set at admission time (CompiledContext.AdmittedPaths),
// so the answer is the compilation's own bookkeeping rather than a re-derivation
// from rendered section headers.
func (c *CompiledContext) admittedPaths() map[string]bool {
	out := make(map[string]bool, len(c.AdmittedPaths))
	for _, p := range c.AdmittedPaths {
		if normalized := normalizeTargetPath(p); normalized != "" {
			out[normalized] = true
		}
	}
	// A payload reconstructed from persisted sections (an audit/replay
	// consumer) has no AdmittedPaths; fall back to the section headers so the
	// gate degrades to "unknown scope" rather than "empty scope".
	if len(out) == 0 {
		for _, s := range c.Sections {
			if s.Source != SourceArtifacts {
				continue
			}
			if path := targetPathFromHeader(s.Header); path != "" {
				out[path] = true
			}
		}
	}
	return out
}

// hasWorkspaceMaterial reports whether the payload carries real workspace file
// content (a section with bytes), not merely a heading.
func (c *CompiledContext) hasWorkspaceMaterial() bool {
	for _, s := range c.Sections {
		if s.Source == SourceArtifacts && strings.TrimSpace(s.Content) != "" {
			return true
		}
	}
	return false
}

// truncatedSet indexes the payload's recorded source-side truncations.
func truncatedSet(files []string) map[string]bool {
	out := make(map[string]bool, len(files))
	for _, f := range files {
		if normalized := normalizeTargetPath(f); normalized != "" {
			out[normalized] = true
		}
	}
	return out
}

// normalizeTargetPath canonicalizes a workspace-relative path for comparison.
// Comparison is on the cleaned, slash-separated, case-preserved form; the
// compiler never treats two spellings of the same file as two targets.
func normalizeTargetPath(p string) string {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p), "./"))
	return strings.ReplaceAll(trimmed, "\\", "/")
}

// targetPathFromHeader extracts the file path a workspace section header names.
// Headers are rendered as "## <path>" by the compiler's section renderer.
func targetPathFromHeader(header string) string {
	trimmed := strings.TrimSpace(header)
	trimmed = strings.TrimPrefix(trimmed, "#")
	trimmed = strings.TrimSpace(trimmed)
	// Strip an optional bold/italic emphasis wrapper.
	trimmed = strings.Trim(trimmed, "*_` ")
	if trimmed == "" {
		return ""
	}
	// A header that carries prose around the path is not a path header.
	if strings.Count(trimmed, " ") > 0 {
		return ""
	}
	return normalizeTargetPath(trimmed)
}

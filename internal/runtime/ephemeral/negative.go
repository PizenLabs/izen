package ephemeral

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// ImmutableConstraintsHeader is the system-prompt section hosting Phase 3
// verified negative knowledge. Every replacement worker receives the
// ACTIVE entries matching its target scope under this header.
//
// Canonical block rendering lives in durable.RenderImmutableConstraintsBlock;
// this alias is kept so existing callers keep compiling.
const ImmutableConstraintsHeader = durable.ImmutableConstraintsHeader

// RenderResumePrompt serializes a ResumeContract into the replacement
// worker's system prompt. ACTIVE negative constraints are rendered under
// [IMMUTABLE NEGATIVE CONSTRAINTS] as hard constraints; STALE entries are
// never attached to the contract so they cannot block valid execution.
func RenderResumePrompt(c ResumeContract) string {
	var b strings.Builder
	b.WriteString("Resume task " + c.Capsule.TaskID)
	b.WriteString(" at checkpoint " + c.CheckpointID)
	b.WriteString(" (reason: " + string(c.ResumeReason) + ").\n")
	if strings.TrimSpace(c.Capsule.Objective) != "" {
		b.WriteString("Objective: " + strings.TrimSpace(c.Capsule.Objective) + "\n")
	}
	if strings.TrimSpace(c.Capsule.CurrentStep.ID) != "" {
		b.WriteString("Current step: " + c.Capsule.CurrentStep.ID)
		if strings.TrimSpace(c.Capsule.CurrentStep.Goal) != "" {
			b.WriteString(" — " + strings.TrimSpace(c.Capsule.CurrentStep.Goal))
		}
		b.WriteString("\n")
	}
	if len(c.Capsule.ActiveScope) > 0 {
		b.WriteString("Active scope: " + strings.Join(c.Capsule.ActiveScope, ", ") + "\n")
	}
	if len(c.NegativeConstraints) == 0 {
		return b.String()
	}
	// Constraint rendering is delegated to the canonical durable renderer
	// so the block format has a single source of truth.
	b.WriteString(durable.RenderImmutableConstraintsBlock(toDurableConstraints(c.NegativeConstraints)))
	return b.String()
}

// toDurableConstraints projects ephemeral constraints onto the durable
// rendering shape.
func toDurableConstraints(in []NegativeConstraint) []durable.ImmutableConstraint {
	out := make([]durable.ImmutableConstraint, 0, len(in))
	for _, n := range in {
		out = append(out, durable.ImmutableConstraint{
			Hypothesis:   n.Hypothesis,
			WhyRejected:  n.WhyRejected,
			EvidenceRefs: append([]string(nil), n.EvidenceRefs...),
		})
	}
	return out
}

// ValidateProposal rejects a worker proposal that attempts a known failed
// approach (Negative Knowledge Inviolability). Matching is a normalized
// case-insensitive substring comparison in both directions so trivial
// rewordings do not evade the constraint. STALE entries are never attached
// to the contract, so only ACTIVE constraints can reject.
func ValidateProposal(proposal string, constraints []NegativeConstraint) error {
	norm := normalizeProposal(proposal)
	if norm == "" {
		return nil
	}
	for _, n := range constraints {
		nh := normalizeProposal(n.Hypothesis)
		if nh == "" {
			continue
		}
		if strings.Contains(norm, nh) || strings.Contains(nh, norm) {
			return fmt.Errorf("ephemeral: proposal attempts rejected approach %q (reason: %s)",
				n.Hypothesis, n.WhyRejected)
		}
	}
	return nil
}

func normalizeProposal(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(s)), " "))
}

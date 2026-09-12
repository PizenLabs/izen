package durable

import "strings"

// ImmutableConstraintsHeader is the system-prompt section hosting verified
// negative knowledge. Every replacement worker receives the ACTIVE entries
// matching its target scope under this header.
//
// CANONICAL SOURCE: this file is the single implementation of the
// [IMMUTABLE NEGATIVE CONSTRAINTS] block rendering. ephemeral
// (RenderResumePrompt) and adaptive (RenderImmutableConstraints) both
// delegate here so the block format cannot drift between packages. It lives
// in durable (the bottom of the dependency chain) because ephemeral cannot
// import adaptive without creating an import cycle.
const ImmutableConstraintsHeader = "[IMMUTABLE NEGATIVE CONSTRAINTS]"

// ImmutableConstraint is the durable projection of one ACTIVE negative
// knowledge entry for system-prompt rendering.
type ImmutableConstraint struct {
	Hypothesis   string
	WhyRejected  string
	EvidenceRefs []string
}

// RenderImmutableConstraintsBlock serializes constraints into the
// system-prompt block:
//
//	[IMMUTABLE NEGATIVE CONSTRAINTS]
//	DO NOT ATTEMPT THE FOLLOWING REJECTED APPROACHES:
//	- Approach: "..." / Reason: "..." / Evidence: [...]
//
// It returns "" when no constraints are given. It is pure and
// goroutine-safe.
func RenderImmutableConstraintsBlock(constraints []ImmutableConstraint) string {
	if len(constraints) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(ImmutableConstraintsHeader + "\n")
	b.WriteString("DO NOT ATTEMPT THE FOLLOWING REJECTED APPROACHES:\n")
	for _, n := range constraints {
		b.WriteString("- Approach: \"" + strings.TrimSpace(n.Hypothesis) + "\"\n")
		if strings.TrimSpace(n.WhyRejected) != "" {
			b.WriteString("  Reason: \"" + strings.TrimSpace(n.WhyRejected) + "\"\n")
		}
		if len(n.EvidenceRefs) > 0 {
			b.WriteString("  Evidence: [" + strings.Join(n.EvidenceRefs, ", ") + "]\n")
		}
	}
	return b.String()
}

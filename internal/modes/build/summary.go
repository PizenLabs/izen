package build

import (
	"fmt"
	"strings"
)

// MutationRecord describes a single file mutation committed by /build.
type MutationRecord struct {
	File     string
	Strategy string
}

// ExecutionSummary is the structured payload rendered by RenderExecutionSummary
// after a patch transaction completes successfully.
type ExecutionSummary struct {
	Success        bool
	ErrorLink      string
	Mutations      []MutationRecord
	ContextID      string
	GuardrailPass  bool
	GuardrailCount int
	GuardrailLimit int
}

// RenderExecutionSummary returns a strictly concise Markdown summary of a build
// mutation. It performs no I/O and emits nothing — the caller decides whether
// and when to display it, keeping /build quiet and lightning-fast mid-run.
func RenderExecutionSummary(s ExecutionSummary) string {
	var b strings.Builder
	b.WriteString("**⛟  BUILD MUTATION SUMMARY**\n")

	status := "SUCCESS"
	if !s.Success {
		status = "FAILED"
		if s.ErrorLink != "" {
			status = fmt.Sprintf("FAILED (%s)", s.ErrorLink)
		}
	}
	fmt.Fprintf(&b, "- **Status:** %s\n", status)

	if len(s.Mutations) == 0 {
		b.WriteString("- **Files Mutated:** none\n")
	} else {
		for _, m := range s.Mutations {
			label := m.Strategy
			if label == "" {
				label = "ATOMIC_REPLACE"
			}
			fmt.Fprintf(&b, "- **Files Mutated:** `%s` (strategy: %s)\n", m.File, label)
		}
	}

	ctxScope := s.ContextID
	if ctxScope == "" {
		ctxScope = "n/a"
	}
	fmt.Fprintf(&b, "- **Context Scope:** [%s]\n", ctxScope)

	guardrail := "PASS"
	if !s.GuardrailPass {
		guardrail = "TRIGGERED"
	}
	count := s.GuardrailCount
	limit := s.GuardrailLimit
	if limit == 0 {
		limit = 3
	}
	fmt.Fprintf(&b, "- **Guardrail Status:** %s (%d/%d mutations)\n", guardrail, count, limit)

	return b.String()
}

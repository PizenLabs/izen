package build

import (
	"fmt"
	"strings"

	izenctx "github.com/PizenLabs/izen/internal/context"
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
	b.WriteString("**🚀 BUILD MUTATION SUMMARY**\n")

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

// SetLedger attaches the shared plan ledger used to flag tasks Completed after
// a successful commit.
func (e *Engine) SetLedger(l *izenctx.TaskLedger) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ledger = l
}

// SetContextID scopes build mutations for the guardrail audit log.
func (e *Engine) SetContextID(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.contextID = id
}

// RenderExecutionSummary returns the Markdown summary of the most recent
// successful patch commit. It is the post-execution hook the /build loop
// invokes once a patch transaction completes — kept non-blocking and silent so
// active execution stays fast.
func (e *Engine) RenderExecutionSummary() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastSummary
}

// RecordPatch is the post-commit hook: it marks the associated plan task as
// Completed in the shared ledger (when a task id is supplied) and renders the
// execution summary, storing it for RenderExecutionSummary.
func (e *Engine) RecordPatch(taskID int, file, strategy string) ExecutionSummary {
	e.mu.Lock()
	e.applied++
	count := e.applied
	e.mu.Unlock()

	if taskID > 0 {
		if l := e.ledgerForRead(); l != nil {
			l.MarkCompleted(taskID)
		}
	}

	summary := ExecutionSummary{
		Success:        true,
		Mutations:      []MutationRecord{{File: file, Strategy: strategy}},
		ContextID:      e.contextIDForRead(),
		GuardrailPass:  true,
		GuardrailCount: count,
		GuardrailLimit: buildGuardrailLimit,
	}
	e.mu.Lock()
	e.lastSummary = RenderExecutionSummary(summary)
	e.mu.Unlock()
	return summary
}

const buildGuardrailLimit = 3

func (e *Engine) ledgerForRead() *izenctx.TaskLedger {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ledger
}

func (e *Engine) contextIDForRead() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.contextID
}

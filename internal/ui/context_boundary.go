package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/contextspec"
)

// ── CONTEXT BOUNDARY (Phase 11.x) ───────────────────────────────────────────
//
// The Context Boundary sits between the unbounded, human-oriented conversation
// (session history) and the frozen Execution Domain. It is the ONLY place the
// UI crosses that boundary:
//
//	ConversationState → (lazy) ContextSpec → ExecutionSpec → canonical executor
//
// The UI never feeds raw conversation history into execution: it freezes a
// bounded ExecutionSpec whose active semantic state is all that crosses. The
// ContextSpec/ExecutionSpec are untrusted artifacts — they carry no authority
// and cannot write, patch, shell or reach the RuntimeExecutor.

// handoffExecutionContext ensures the ContextSpec is fresh (lazily compiling
// one only when required), freezes a bounded ExecutionSpec against the CURRENT
// workspace snapshot and validates that snapshot. It is a no-op when the
// Context Pipeline is not wired (harness/test paths). A failure is typed and
// must block execution — the caller decides how to surface it.
func (m *model) handoffExecutionContext(objective string) error {
	if m == nil || m.contextSpec == nil || m.sess == nil {
		return nil
	}
	cs := contextspec.ConversationStateFromSession(m.sess, objective)
	declared := m.autonomyHotfix || hasExplicitExecutionAuthority(objective)
	scopeProvenance := "dynamic"
	if declared {
		scopeProvenance = "declared"
	}
	spec, err := m.contextSpec.FreezeExecution(context.Background(), cs, contextspec.FreezeOptions{
		Intent:          objective,
		Targets:         contextTargetsFromInput(objective),
		Declared:        declared,
		ScopeProvenance: scopeProvenance,
	})
	if err != nil {
		return err
	}
	// Bind to the workspace state the contract was frozen against. A divergence
	// here means the hand-off itself is stale: refuse rather than silently
	// refresh (refreshing would change the execution contract).
	if err := m.contextSpec.ValidateFrozenSnapshot(spec); err != nil {
		return err
	}
	m.lastExecutionSpec = spec
	return nil
}

// contextTargetsFromInput extracts explicit @path references from an objective.
// It is the conservative, deterministic target set for the frozen contract; the
// execution strategy remains the authority that resolves the real geometry.
func contextTargetsFromInput(objective string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, field := range strings.Fields(objective) {
		if !strings.HasPrefix(field, "@") || len(field) < 2 {
			continue
		}
		path := strings.Trim(field[1:], "`'\".,;:!?)(")
		if path == "" || path == "." {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

// runSpecCmd renders the ContextSpec read-only: status, revisions, goal,
// targets, active constraints, active decisions and open questions. It never
// mutates, authorizes or executes anything.
func (m *model) runSpecCmd() tea.Cmd {
	if m.contextSpec == nil {
		m.push(roleSystem, "context pipeline not wired — no compiled spec available")
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
		return nil
	}
	var revision uint64
	if m.sess != nil {
		cs := contextspec.ConversationStateFromSession(m.sess, m.sess.ObjectiveIntent())
		revision = cs.Revision
		// /spec is an explicit inspection boundary: it may lazily compile when
		// the committed spec is stale. Compilation is cheap and never executes.
		if _, _, err := m.contextSpec.EnsureFresh(context.Background(), cs); err != nil {
			m.push(roleError, "/spec: "+err.Error())
			m.refreshViewportContent()
			m.gotoBottomIfAllowed()
			return nil
		}
	}
	m.push(roleSystem, m.renderContextSpec(revision))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return nil
}

// renderContextSpec formats the committed spec. Read-only projection.
func (m *model) renderContextSpec(revision uint64) string {
	spec := m.contextSpec.Current()
	var b strings.Builder
	b.WriteString("ContextSpec\n")
	b.WriteString("───────────\n")
	if spec == nil {
		b.WriteString("Status: EMPTY\n")
		fmt.Fprintf(&b, "Conversation Revision: %d\n", revision)
		return strings.TrimRight(b.String(), "\n")
	}
	status := "STALE"
	if spec.IsFresh(revision) {
		status = "FRESH"
	}
	b.WriteString("Status: " + status + "\n")
	fmt.Fprintf(&b, "Conversation Revision: %d\n", revision)
	fmt.Fprintf(&b, "Spec Revision: %d\n", spec.SpecRevision)
	if spec.Goal != "" {
		b.WriteString("\nGoal:\n  " + spec.Goal + "\n")
	}
	targets := spec.ActiveTargets()
	if len(targets) > 0 {
		b.WriteString("\nTargets:\n")
		for _, t := range targets {
			b.WriteString("  " + t + "\n")
		}
	}
	active := spec.ActiveConstraints()
	if len(active) > 0 {
		b.WriteString("\nConstraints:\n")
		for _, c := range active {
			b.WriteString("  - " + c.Text + "\n")
		}
	}
	decisions := spec.ActiveDecisions()
	if len(decisions) > 0 {
		b.WriteString("\nDecisions:\n")
		for _, d := range decisions {
			b.WriteString("  - " + d.Text + "\n")
		}
	}
	if len(spec.OpenQuestions) > 0 {
		b.WriteString("\nOpen Questions:\n")
		for _, q := range spec.OpenQuestions {
			b.WriteString("  - " + q.Text + "\n")
		}
	}
	if m.lastExecutionSpec != nil {
		fmt.Fprintf(&b, "\nFrozen execution contract: %s\n", m.lastExecutionSpec.SpecID)
	}
	return strings.TrimRight(b.String(), "\n")
}

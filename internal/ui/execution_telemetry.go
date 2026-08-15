package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
)

// ── Execution Telemetry: debug / inspect view (Phase 3) ─────────────────────
//
// The normal UI stays compact: the authoritative stage line renders a single
// truthful status (e.g. "Model ● streaming · 921 tok"). The DETAILED execution
// timeline — per-stage started/completed/elapsed, provider
// request→waiting→first-token→streaming→terminal attribution, invocation and
// retry counters, live-worker tracking — lives behind the $inspect interaction
// so diagnostics never pollute the primary view.
//
// This is execution telemetry ONLY. It never exposes model reasoning or
// chain-of-thought.

// runInspectCmd renders the execution timeline of the most recently finalized
// foreground operation. An optional target filter (e.g. `$inspect model`)
// narrows the rows to stages whose name contains the filter. When no operation
// has completed yet, a truthful notice is shown instead of a fabricated
// timeline.
func (m *model) runInspectCmd(filter string) tea.Cmd {
	return func() tea.Msg {
		snap := m.lastExecutionSnapshot
		// When the most recent operation is still in flight, prefer its live
		// snapshot so the inspector shows real in-progress latency.
		if m.activeOp != nil && m.activeOp.Telemetry != nil {
			snap = m.activeOp.Telemetry.Snapshot()
		}
		if snap.OpID == "" {
			m.push(roleSystem, mutedStyle.Render("$inspect: no completed execution yet — run a build, $hot, /plan or /investigate first."))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		rendered := renderInspectTimeline(snap, strings.TrimSpace(filter))
		m.push(roleSystem, infoStyle.Render(" execution telemetry"))
		for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
			m.push(roleSystem, mutedStyle.Render(line))
		}
		// ── CONTEXT OWNERSHIP + EXECUTION PROOF (Phase 8) ──────────────
		// The detailed view also exposes what context crossed to the provider
		// and the execution-evidence account — both derived from real runtime
		// records, never reconstructed from UI state.
		if m.lastPromptEnvelope.OperationID != "" || m.lastPromptEnvelope.Target != "" {
			m.push(roleSystem, mutedStyle.Render(""))
			for _, line := range strings.Split(strings.TrimRight(renderPromptEnvelope(m.lastPromptEnvelope), "\n"), "\n") {
				m.push(roleSystem, mutedStyle.Render(line))
			}
		}
		if m.lastExecutionProof.OperationID != "" || m.lastExecutionProof.Target != "" {
			m.push(roleSystem, mutedStyle.Render(""))
			for _, line := range strings.Split(strings.TrimRight(renderExecutionProof(m.lastExecutionProof), "\n"), "\n") {
				m.push(roleSystem, mutedStyle.Render(line))
			}
		}
		// ── MULTI-FILE EXECUTION GRAPH (Phase 9B) ────────────────────
		// The aggregate graph — one user intent → one graph → one MutationSet
		// → one terminal outcome — with per-node evidence. Only rendered when a
		// multi-file graph was executed; the single-file path stays unchanged.
		if m.lastExecutionGraph != nil {
			m.push(roleSystem, mutedStyle.Render(""))
			for _, line := range strings.Split(strings.TrimRight(renderExecutionGraph(m.lastExecutionGraph), "\n"), "\n") {
				m.push(roleSystem, mutedStyle.Render(line))
			}
		}
		m.push(roleSystem, mutedStyle.Render("  — execution metadata only; model reasoning is never exposed."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
}

// renderInspectTimeline renders the timeline, optionally filtered to stages
// whose name contains filter (case-insensitive). Filtering also keeps the
// corresponding provider rows so a `$inspect model` shows provider latency
// attribution.
func renderInspectTimeline(snap execution.TelemetrySnapshot, filter string) string {
	filter = strings.ToLower(strings.TrimSpace(filter))
	keepStage := func(name string) bool {
		if filter == "" {
			return true
		}
		return strings.Contains(strings.ToLower(name), filter)
	}
	keepProvider := keepStage("model")

	filtered := snap
	filtered.Stages = nil
	for _, sp := range snap.Stages {
		if keepStage(sp.Stage) {
			filtered.Stages = append(filtered.Stages, sp)
		}
	}
	filtered.Providers = nil
	if keepProvider {
		filtered.Providers = snap.Providers
	}
	return filtered.RenderTimeline()
}

// renderExecutionGraph renders the aggregate multi-file execution graph for
// $inspect: the graph state, the single MutationSet terminal state, the
// invocation count, and every node's semantic evidence. It carries no model
// reasoning.
func renderExecutionGraph(g *execution.ExecutionGraph) string {
	if g == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("graph:")
	if g.ID != "" {
		b.WriteString(" op=" + g.ID)
	}
	b.WriteString(" state=" + string(g.State) + " nodes=" + itoa(len(g.Nodes)))
	if g.MutationSet != nil {
		b.WriteString(" mutation-set=" + string(g.MutationSet.State))
		if g.MutationSet.Transaction != nil {
			b.WriteString(" tx=" + string(g.MutationSet.Transaction.State))
		}
	}
	if len(g.Edges) > 0 {
		b.WriteString("\n  edges=" + itoa(len(g.Edges)))
		for _, e := range g.Edges {
			b.WriteString(" " + e.From + "->" + e.To)
		}
	}
	for _, n := range g.Nodes {
		b.WriteString("\n  " + n.Target + ": state=" + string(n.State))
		b.WriteString(" artifact=" + boolWord(n.Evidence.ArtifactPresent))
		b.WriteString(" apply=" + boolWord(n.Evidence.ApplyExecuted))
		b.WriteString(" changed=" + boolWord(n.Evidence.FilesystemChanged))
		b.WriteString(" verify=" + boolWord(n.Evidence.VerificationPassed))
		if n.Evidence.Outcome != "" {
			b.WriteString(" outcome=" + string(n.Evidence.Outcome))
		}
	}
	return b.String()
}

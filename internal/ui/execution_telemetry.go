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

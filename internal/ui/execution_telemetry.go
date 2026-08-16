package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
	runtimegraph "github.com/PizenLabs/izen/internal/execution/graph"
	"github.com/PizenLabs/izen/internal/execution/strategy"
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
		// ── ENGINE-FIRST STRATEGY DECISION (Phase 10) ─────────────────
		// The deterministic decision record of the most recent $prompt: the
		// selected strategy, the target-resolution outcomes, the
		// execution-factor complexity, the context channels, the artifact
		// contract and the budgets the engine chose BEFORE any model call.
		// It answers "why did Izen call the model / read this file / need
		// /plan?" with execution facts — never model reasoning.
		if m.lastExecutionStrategy.Strategy != "" {
			m.push(roleSystem, mutedStyle.Render(""))
			for _, line := range strings.Split(strings.TrimRight(renderExecutionStrategy(m.lastExecutionStrategy), "\n"), "\n") {
				m.push(roleSystem, mutedStyle.Render(line))
			}
		}
		// ── CONTEXT ENVELOPE (Phase 10) ──────────────────────────────
		// The compiled minimum-sufficient context account: every item with its
		// owner (engine/model), source, and reason for inclusion.
		if m.lastContextEnvelope.ItemCount() > 0 {
			m.push(roleSystem, mutedStyle.Render(""))
			for _, line := range strings.Split(strings.TrimRight(renderContextEnvelope(m.lastContextEnvelope), "\n"), "\n") {
				m.push(roleSystem, mutedStyle.Render(line))
			}
		}
		// ── EXECUTION GRAPH (Phase 11) ───────────────────────────────
		// The compiled explicit execution graph of the most recent engine-first
		// $prompt: the typed node sequence (resolve_target → … → verify), the
		// node states recorded at real runtime boundaries, the expected model
		// invocations and the escalation history. It answers "what did Izen
		// intend to execute, what has it actually executed so far" with engine
		// facts — never model reasoning.
		if m.lastStrategyGraph != nil {
			m.push(roleSystem, mutedStyle.Render(""))
			for _, line := range strings.Split(strings.TrimRight(renderStrategyGraph(m.lastStrategyGraph), "\n"), "\n") {
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
		// ── RUNTIME GRAPH (Phase 12 / P1 #6) ────────────────────────
		// The runtime-owned execution graph evidence of the most recent gated
		// RuntimeExecutor execution: every canonical stage with its real
		// state/evidence/timestamp. It is the authoritative execution timeline
		// the runtime produced — never reconstructed by the UI.
		if len(m.lastRuntimeGraph) > 0 {
			m.push(roleSystem, mutedStyle.Render(""))
			for _, line := range strings.Split(strings.TrimRight(renderRuntimeGraph(m.lastRuntimeGraph), "\n"), "\n") {
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

// renderRuntimeGraph renders the runtime-owned execution graph evidence of a
// gated RuntimeExecutor execution for $inspect: every canonical stage with its
// real state, evidence and start timestamp. It renders ONLY what the runtime
// recorded — a pending stage is a stage that was never reached. It never
// exposes model reasoning.
func renderRuntimeGraph(stages []runtimegraph.StageSnapshot) string {
	if len(stages) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "runtime-graph: stages=%d", len(stages))
	for _, s := range stages {
		b.WriteString("\n  " + s.Kind + ": " + string(s.State))
		if s.Evidence != "" {
			b.WriteString(" — " + truncateInline(s.Evidence, 80))
		}
		if !s.StartedAt.IsZero() {
			b.WriteString(" at=" + s.StartedAt.Format("15:04:05.000"))
		}
	}
	return b.String()
}

// renderExecutionStrategy renders the engine-first strategy decision record of
// the most recent $prompt for $inspect: the strategy, its deterministic
// rationale, the target-resolution outcomes, the execution-factor complexity,
// the minimum-sufficient context channels, the artifact contract and the
// budgets the engine selected BEFORE any model invocation. It exposes
// execution facts only — never model reasoning.
func renderExecutionStrategy(p strategy.ExecutionStrategyProfile) string {
	var b strings.Builder
	b.WriteString("strategy:")
	if p.Intent != "" {
		b.WriteString(" intent=" + truncateInline(p.Intent, 80))
	}
	fmt.Fprintf(&b, "\n  strategy=%s complexity=%s", p.Strategy, p.Complexity.Level)
	if p.Complexity.Score > 0 {
		fmt.Fprintf(&b, " (score=%d)", p.Complexity.Score)
	}
	for _, f := range p.Complexity.Factors {
		b.WriteString("\n    factor " + f.Name + "=" + itoa(f.Score) + " — " + f.Reason)
	}
	if p.StrategyReason != "" {
		b.WriteString("\n  reason=" + p.StrategyReason)
	}
	if p.Deterministic {
		b.WriteString("\n  deterministic=yes")
	}
	b.WriteString("\n  model=" + boolWord(p.ModelRequired))
	if p.ModelRequired {
		b.WriteString(" decision=" + p.ModelDecision)
	}
	for _, t := range p.Targets {
		b.WriteString("\n  target " + t.Resolved)
		if t.Resolved == "" || t.Resolved == t.Raw {
			b.WriteString(" (raw=" + t.Raw + ")")
		}
		b.WriteString(" status=" + string(t.Status))
		if t.Source != "" {
			b.WriteString(" source=" + t.Source)
		}
		b.WriteString(" exists=" + boolWord(t.Exists))
		if t.Reason != "" {
			b.WriteString(" — " + t.Reason)
		}
	}
	if len(p.ContextKinds) > 0 {
		b.WriteString("\n  context=")
		first := true
		for _, k := range p.ContextKinds {
			if !first {
				b.WriteString(",")
			}
			first = false
			b.WriteString(k.Label())
		}
	}
	if p.Artifact.Kind != "" {
		b.WriteString("\n  artifact=" + p.Artifact.Kind)
		if p.Artifact.Bounded {
			b.WriteString(" (bounded)")
		}
	}
	if p.ReasoningBudget > 0 {
		fmt.Fprintf(&b, "\n  reasoning-budget=%d", p.ReasoningBudget)
	}
	if p.MaxOutputTokens > 0 {
		fmt.Fprintf(&b, "\n  output-budget=%d", p.MaxOutputTokens)
	}
	if p.Escalation {
		b.WriteString("\n  escalated=" + p.EscalationReason)
	}
	if p.ModelRequired {
		ic := strategy.For(p, 1)
		b.WriteString("\n  " + ic.String())
	}
	return b.String()
}

func truncateInline(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// renderStrategyGraph renders the compiled explicit execution graph of the most
// recent engine-first $prompt for $inspect: the strategy, the graph lifecycle
// state, the expected model invocations, every typed node with its recorded
// state, and the escalation history. It exposes execution facts only — the
// graph carries no model reasoning.
func renderStrategyGraph(g *strategy.ExecutionGraph) string {
	if g == nil {
		return "execution-graph: <nil>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "execution-graph: strategy=%s state=%s nodes=%d expected-invocations=%d",
		g.Strategy, g.State, g.NodeCount(), g.ExpectedInvocations)
	if len(g.MutationTargets) > 0 {
		fmt.Fprintf(&b, " mutation-targets=%s", strings.Join(g.MutationTargets, ","))
	}
	fmt.Fprintf(&b, "\n  metrics: %s", g.Metrics().String())
	for _, n := range g.Nodes {
		fmt.Fprintf(&b, "\n  %s %s: %s", n.ID, n.Kind.Label(), n.State)
		if n.Target != "" {
			b.WriteString(" " + n.Target)
		}
		if n.RequiresModel {
			fmt.Fprintf(&b, " model=yes invocation#%d", n.Invocation)
		}
		if n.Kind.HumanBoundary() {
			b.WriteString(" human")
		}
		if n.Evidence != "" {
			b.WriteString(" — " + truncateInline(n.Evidence, 80))
		}
	}
	if g.EscalationCount() > 0 {
		fmt.Fprintf(&b, "\n  escalations=%d", g.EscalationCount())
		for _, e := range g.Escalations {
			b.WriteString("\n    " + e.String())
		}
	}
	return b.String()
}

// renderContextEnvelope renders the compiled minimum-sufficient context
// account of the most recent $prompt for $inspect. Every item names its owner
// ("engine" / "model"), its source, and the concrete reason it was included —
// the context ownership model made observable. It never exposes model
// reasoning, only what context the engine supplied and why.
func renderContextEnvelope(env strategy.ContextEnvelope) string {
	var b strings.Builder
	b.WriteString("context-envelope:")
	if env.Expanded {
		b.WriteString(" expanded=" + env.ExpansionReason)
	}
	for _, it := range env.Items {
		b.WriteString("\n  " + it.Kind.Label())
		b.WriteString(" owner=" + it.Owner)
		b.WriteString(" source=" + it.Source.Label())
		if it.Content != "" {
			b.WriteString(" " + truncateInline(it.Content, 80))
		}
		if it.ReasonForInclusion != "" {
			b.WriteString(" — " + it.ReasonForInclusion)
		}
	}
	return b.String()
}

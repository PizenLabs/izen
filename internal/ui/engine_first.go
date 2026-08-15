package ui

import (
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/command"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/modes"
)

// ── PHASE 10 — ENGINE-FIRST EXECUTION STRATEGY LAYER ────────────────────────
//
// The strategy layer (internal/execution/strategy) classifies a $prompt into a
// bounded execution strategy BEFORE any model invocation. This file wires that
// layer into the UI dispatcher:
//
//   - selectExecutionStrategy runs the deterministic selector over the
//     workspace evidence (target existence, resolution, operation class).
//   - routeEngineFirstPrompt dispatches the strategy onto the existing
//     execution paths — hotfix for targeted mutations, deterministic task
//     staging for direct tasks, /ask chat for targeted reasoning, and the
//     existing ask-handoff/investigate/plan path for repository work.
//
// The engine decides WHAT execution requires, HOW MUCH context is sufficient,
// WHEN a model is necessary, and WHICH artifact the model must produce. The
// model is only ever a reasoning component inside that decision.
//
// OBSERVABILITY: every dispatch records the ExecutionStrategyProfile in
// m.lastExecutionStrategy, rendered by $inspect (execution_telemetry.go).

// engineFirstWorkspace adapts the real workspace to the strategy.Workspace
// contract. It performs existence checks and bounded fuzzy lookups only — no
// repository-wide scan and no model call.
type engineFirstWorkspace struct {
	root string
}

func (w engineFirstWorkspace) Root() string { return w.root }

func (w engineFirstWorkspace) Exists(path string) bool {
	if w.root == "" {
		_, err := os.Stat(path)
		return err == nil
	}
	info, err := os.Stat(filepath.Join(w.root, path))
	return err == nil && !info.IsDir()
}

func (w engineFirstWorkspace) ResolveFuzzy(name string, max int) []string {
	root := w.root
	if root == "" {
		root = "."
	}
	var out []string
	visited := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == ".izen" || base == "node_modules" ||
				base == "vendor" || base == ".venv" || base == "venv" {
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if visited > 2000 {
			return filepath.SkipAll
		}
		if len(out) >= max {
			return filepath.SkipAll
		}
		if strings.EqualFold(d.Name(), name) {
			rel, rerr := filepath.Rel(root, path)
			if rerr == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	return out
}

// strategyDeps returns the deterministic inputs of the strategy selector.
func (m *model) strategyDeps() strategy.Deps {
	return strategy.Deps{
		Root:      m.workspaceRoot,
		Workspace: engineFirstWorkspace{root: m.workspaceRoot},
	}
}

// selectExecutionStrategy runs the deterministic engine-first selector for a
// $prompt tail. It never invokes a model and never reads file content into a
// prompt — it only resolves targets and classifies the operation.
func (m *model) selectExecutionStrategy(rawInput string) strategy.ExecutionStrategyProfile {
	return strategy.Select(rawInput, m.strategyDeps())
}

// routeEngineFirstPrompt dispatches a $prompt through the engine-first
// strategy layer. It returns (cmd, true) when the strategy fully owns the
// request; (nil, false) when the caller must fall through to the existing
// fast-track / ask-handoff path.
func (m *model) routeEngineFirstPrompt(rawInput string) (tea.Cmd, bool) {
	profile := m.selectExecutionStrategy(rawInput)
	m.lastExecutionStrategy = profile
	// Compile the minimum-sufficient context envelope the strategy requires.
	// It records every context channel with its owner, source and inclusion
	// reason; $inspect renders it so "why did Izen read this file / use this
	// much context" is answerable without exposing model reasoning.
	m.lastContextEnvelope = strategy.NewCompiler(m.strategyDeps()).Compile(profile)
	// Compile the explicit execution graph the strategy dictates and drive its
	// initial deterministic nodes (Phase 11). The graph exists before any
	// model invocation and records real runtime boundaries as they happen.
	m.recordStrategyGraph(profile)

	switch profile.Strategy {
	case strategy.HumanClarification:
		return m.clarifyEngineFirst(profile), true

	case strategy.TargetedMutation:
		return m.routeEngineFirstTargeted(profile, rawInput), true

	case strategy.DirectDeterministic:
		return m.routeEngineFirstDeterministic(profile), true

	case strategy.TargetedReasoning:
		return m.routeEngineFirstReasoning(rawInput), true

	default:
		// RepositoryInvestigation / MultiFilePlanning: repository evidence
		// proved that investigate/plan is justified — fall through to the
		// existing ask-handoff path, which owns those engines.
		return nil, false
	}
}

// clarifyEngineFirst surfaces a target-resolution ambiguity before any model
// call. No invocation, no mutation, no context expansion — the human is the
// authority.
func (m *model) clarifyEngineFirst(p strategy.ExecutionStrategyProfile) tea.Cmd {
	m.push(roleError, "[PROMPT] "+p.StrategyReason)
	m.push(roleSystem, infoStyle.Render(
		"  No model call was made and no files were modified. Name the exact target, e.g. \"$prompt fix X in @index.html\"."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
}

// routeEngineFirstTargeted routes a targeted mutation directly to the bounded
// single/multi-file mutation pipeline (the same executor $hot uses). It never
// enters /investigate or /plan: one appropriately bounded model call, then
// approval, mutation and verification.
func (m *model) routeEngineFirstTargeted(p strategy.ExecutionStrategyProfile, rawInput string) tea.Cmd {
	// The mutation executor requires /build. The $prompt directive explicitly
	// authorizes the operation, so the transition gate is opened.
	if m.resolver.Current() != modes.ModeBuild {
		m.modeChangeAuthorized = true
		m.setMode(modes.ModeBuild)
	}
	// Adaptive output budget: the strategy selected a budget proportional to
	// the artifact contract and complexity. The hotfix executor consumes it
	// and clears it on the terminal message.
	m.activeStrategyBudget = p.MaxOutputTokens
	m.hotfixBranding = "PROMPT"
	m.push(roleSystem, infoStyle.Render(
		" engine-first: targeted_mutation — routing directly to bounded mutation (no investigate/plan)."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return m.handleHotfixCmd(rawInput)
}

// routeEngineFirstDeterministic stages a zero-model deterministic task set.
// The engine resolves the operation without any LLM reasoning.
func (m *model) routeEngineFirstDeterministic(p strategy.ExecutionStrategyProfile) tea.Cmd {
	target := ""
	if len(p.Targets) > 0 {
		target = p.Targets[0].Resolved
	}
	if target == "" {
		target = "LICENSE"
	}
	m.push(roleSystem, infoStyle.Render(" engine-first: direct_deterministic — zero model invocations."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	tasks := command.GenerateFallbackPlan(command.FallbackPlanTarget{
		File:        target,
		Description: p.Intent,
		TaskType:    "FILE_MUTATE",
	})
	return func() tea.Msg {
		return planResultMsg{Tasks: tasks, IsFastTrack: true, EngineFirst: true}
	}
}

// routeEngineFirstReasoning routes a read-only understanding request with an
// explicit target into the normal /ask chat path, which gathers the explicit
// file context through the governed Context Planner — never a repository scan.
func (m *model) routeEngineFirstReasoning(rawInput string) tea.Cmd {
	if m.resolver.Current() != modes.ModeAsk {
		m.modeChangeAuthorized = true
		m.setMode(modes.ModeAsk)
	}
	m.push(roleSystem, infoStyle.Render(
		" engine-first: targeted_reasoning — read-only /ask chat with explicit target context."))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return m.handleMessageContent(rawInput)
}

// hotfixOutputBudget returns the adaptive output budget selected by the
// engine-first router for the current targeted mutation, falling back to the
// legacy fixed bound when no strategy budget is active. It is a strict no-op
// when the strategy did not select a budget.
func (m *model) hotfixOutputBudget() int {
	if m.activeStrategyBudget > 0 {
		return m.activeStrategyBudget
	}
	return 2048
}

// hotfixBrandingLabel returns the status-line label of the bounded mutation
// executor: "HOTFIX" for $hot, "PROMPT" when a $prompt routed through the
// engine-first strategy layer. It is reset to the $hot default by the
// terminal messages so a stale label never leaks across operations.
func (m *model) hotfixBrandingLabel() string {
	if m.hotfixBranding == "PROMPT" {
		return "PROMPT"
	}
	return "HOTFIX"
}

// clearEngineFirstMutationState releases the transient strategy state of a
// targeted mutation at its terminal message: the adaptive output budget and
// the operation branding. The retained ExecutionStrategyProfile in
// m.lastExecutionStrategy survives for $inspect.
func (m *model) clearEngineFirstMutationState() {
	m.activeStrategyBudget = 0
	m.hotfixBranding = ""
}

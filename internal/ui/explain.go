package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/pkg/engine/inference"
	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
	"github.com/PizenLabs/izen/pkg/engine/lowerer"
)

// runExplainDecisionCmd is the /explain-decision evidence inspector. It
// answers WHY the engine chose a specific tech stack: it collects workspace
// facts, runs the multi-hypothesis InferenceEngine, evaluates the policy, and
// renders the ranked hypotheses with every traceable EvidenceTrace and its
// weight. It is a read-only projection — no files are touched and no model is
// called.
func (m *model) runExplainDecisionCmd() tea.Cmd {
	root := m.workspaceRoot
	if root == "" {
		m.push(roleSystem, warningStyle.Render("no workspace root — cannot inspect decisions"))
		m.refreshViewportContent()
		return nil
	}

	facts := inference.AnalyzeWorkspace(root)
	slots := inference.PromptSlots{Raw: m.currentPrompt}
	set := inference.NewInferenceEngine().Infer(facts, slots)
	policy := inference.NewPolicyEngine()

	m.push(roleSystem, labelBoldStyle.Render(" decision inspector — "+root))
	m.push(roleSystem, mutedStyle.Render("  hypothesis · confidence · [policy verdict] — ranked best first"))
	m.push(roleSystem, "")

	for _, dim := range set.Types() {
		m.push(roleSystem, labelBoldStyle.Render(" "+dim.String()))
		inf := set.Inference(dim)
		if inf.Label == "" {
			m.push(roleSystem, mutedStyle.Render("  no confident hypothesis (evidence too thin → fallback)"))
			m.push(roleSystem, "")
			continue
		}
		verdict := policy.Evaluate(set, dim)
		m.push(roleSystem, infoStyle.Render(fmt.Sprintf(
			"  → %-16s confidence %.2f   [%s]", inf.Label, inf.Confidence, verdict.Decision)))
		for _, tr := range inf.Evidence {
			m.push(roleSystem, mutedStyle.Render(fmt.Sprintf(
				"      +%.2f  %s   %s", tr.Weight, tr.Key(), tr.Reason)))
		}
		for i, alt := range inf.Alternatives {
			if i == 0 {
				continue
			}
			m.push(roleSystem, mutedStyle.Render(fmt.Sprintf(
				"      alt        %-16s confidence %.2f", alt.Label, alt.Confidence())))
		}
		if verdict.RunnerUp != nil && verdict.Decision == inference.DecisionEscalateToHuman {
			m.push(roleSystem, warningStyle.Render(fmt.Sprintf(
				"      ⚠ %s", verdict.Reason)))
		}
		m.push(roleSystem, "")
	}

	// Resolved framework → capability graph projection.
	if fw, ok := lowerer.ResolveFramework(set.ResolvedFramework()); ok {
		plan, err := ir.NewLogicalPlan([]ir.IRNode{&ir.CreatePageNode{Name: "About"}}, nil)
		if err == nil {
			lower := lowerer.NewPlanLowerer(lowerer.DefaultRegistry())
			caps := lower.ResolveCapabilities(fw, plan)
			var capLabels []string
			for _, c := range caps {
				capLabels = append(capLabels, c.String())
			}
			m.push(roleSystem, labelBoldStyle.Render(" capability graph — "+fw.String()))
			m.push(roleSystem, infoStyle.Render("  resolved: "+strings.Join(capLabels, " · ")))
			m.push(roleSystem, "")
		}
	}

	m.push(roleSystem, mutedStyle.Render("  Weights: config +0.30 · dependency +0.60 · prompt +0.40 · workspace +0.20"))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
}

package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/review"
)

func (m *model) runInvestigateCmd(content string) tea.Cmd {
	m.agentRunning = true
	m.agentDone = false
	m.agentLabel = "investigating"
	m.spinnerFrame = 0

	return func() tea.Msg {
		eng := investigate.NewEngine(".", content, nil, nil)
		result, err := eng.Run()
		if err != nil {
			return investigateResultMsg{err: err}
		}

		var recs []record
		push := func(r role, text string) {
			for _, l := range strings.Split(text, "\n") {
				recs = append(recs, record{role: r, text: l})
			}
		}

		push(roleAI, investigationStyle.Render(fmt.Sprintf("problem: %s", result.Problem)))
		push(roleAI, investigationStyle.Render(fmt.Sprintf(
			"duration: %s · loops: %d · hypotheses: %d · evidence: %d",
			result.Duration, result.Loops, len(result.Hypotheses), len(result.Evidence))))

		if result.Resolved {
			push(roleStatus, hypothesisStyle.Render("✓ "+result.Conclusion))
		} else {
			push(roleSystem, infoStyle.Render("investigation inconclusive"))
		}

		for _, h := range result.Hypotheses {
			sym := "○"
			switch h.Status {
			case investigate.HypothesisConfirmed:
				sym = "✓"
			case investigate.HypothesisRejected:
				sym = "✗"
			}
			push(roleAI, hypothesisStyle.Render(
				fmt.Sprintf("  %s %s [%s] (%.0f%%)", sym, h.Theory, h.Status, h.Confidence*100)))
		}

		for _, ev := range result.Evidence {
			c := ev.Content
			if len(c) > 60 {
				c = c[:60] + "…"
			}
			push(roleAI, evidenceStyle.Render(fmt.Sprintf("  [%s] %s", ev.Source, c)))
		}

		if !result.Resolved && result.Error != "" {
			push(roleError, "error: "+result.Error)
		}

		return investigateResultMsg{records: recs, sessionKey: result.Problem}
	}
}

func (m *model) runReviewCmd() tea.Cmd {
	m.agentRunning = true
	m.agentDone = false
	m.agentLabel = "reviewing"
	m.spinnerFrame = 0

	return func() tea.Msg {
		eng := review.NewEngine(".", nil, nil)
		result, err := eng.Run()
		if err != nil {
			return reviewResultMsg{err: err}
		}

		var recs []record
		push := func(r role, text string) {
			for _, l := range strings.Split(text, "\n") {
				recs = append(recs, record{role: r, text: l})
			}
		}

		if result.Error != "" {
			push(roleSystem, infoStyle.Render(result.Error))
			return reviewResultMsg{records: recs}
		}

		push(roleAI, reviewStyle.Render(fmt.Sprintf("review: %s → %s", result.BaseBranch, result.Branch)))
		push(roleAI, reviewStyle.Render(fmt.Sprintf(
			"commit: %s · files: %d · duration: %s",
			result.CommitHash, len(result.FilesChanged), result.Duration)))

		scoreColor := scoreStyle
		if result.Score < 50 {
			scoreColor = errorStyle
		} else if result.Score < 75 {
			scoreColor = riskHighStyle
		}
		push(roleAI, scoreColor.Render(fmt.Sprintf("score: %d/100  risk: %d/100", result.Score, result.ImpactRadius.RiskScore)))

		if len(result.FilesChanged) > 0 {
			push(roleAI, infoStyle.Render("changed files:"))
			for _, f := range result.FilesChanged {
				sym := "~"
				switch f.Status {
				case "added":
					sym = "+"
				case "deleted":
					sym = "-"
				case "renamed":
					sym = "→"
				}
				push(roleAI, infoStyle.Render(fmt.Sprintf("  %s %s (+%d/-%d)", sym, f.Path, f.Additions, f.Deletions)))
			}
		}

		if len(result.ImpactRadius.IndirectFiles) > 0 {
			push(roleAI, riskMediumStyle.Render(fmt.Sprintf(
				"impact: %d direct · %d indirect · %d packages",
				len(result.ImpactRadius.DirectFiles),
				len(result.ImpactRadius.IndirectFiles),
				len(result.ImpactRadius.AffectedPkgs))))
		}

		sevOrder := []review.RiskSeverity{
			review.RiskCritical, review.RiskHigh, review.RiskMedium, review.RiskLow, review.RiskInfo,
		}
		sevStyles := map[review.RiskSeverity]lipgloss.Style{
			review.RiskCritical: riskCriticalStyle,
			review.RiskHigh:     riskHighStyle,
			review.RiskMedium:   riskMediumStyle,
			review.RiskLow:      riskLowStyle,
			review.RiskInfo:     riskInfoStyle,
		}

		for _, sev := range sevOrder {
			var findings []review.RiskFinding
			for _, f := range result.RiskFindings {
				if f.Severity == sev {
					findings = append(findings, f)
				}
			}
			if len(findings) == 0 {
				continue
			}
			style := sevStyles[sev]
			push(roleAI, style.Render(fmt.Sprintf("  [%s] %d findings", strings.ToUpper(string(sev)), len(findings))))
			for _, f := range findings {
				push(roleAI, style.Render(fmt.Sprintf("    %s:%d — %s", f.File, f.Line, f.Description)))
			}
		}

		if len(result.Recommendations) > 0 {
			push(roleAI, reviewStyle.Render("recommendations:"))
			for i, rec := range result.Recommendations {
				push(roleAI, infoStyle.Render(fmt.Sprintf("  %d. %s", i+1, rec)))
			}
		}

		sessionKey := result.Branch + "@" + result.CommitHash
		savedResult := result
		return reviewResultMsg{
			records:      recs,
			sessionKey:   sessionKey,
			saveReportFn: func() { review.SaveReport(savedResult, ".") },
		}
	}
}

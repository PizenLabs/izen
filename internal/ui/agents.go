package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/modes/commit"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/review"
	"github.com/PizenLabs/izen/internal/prompt"
)

func (m *model) runInvestigateCmd(content string) tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			return agentStartMsg{label: "investigating"}
		},
		m.runInvestigateAsyncCmd(content),
	)
}

func (m *model) runInvestigateAsyncCmd(content string) tea.Cmd {
	currentMode := m.resolver.Current()

	return func() tea.Msg {
		if !currentMode.CanShell() {
			return investigateResultMsg{err: fmt.Errorf("investigate mode: shell execution denied by %s capabilities", currentMode)}
		}
		if currentMode.CanWrite() {
			return investigateResultMsg{err: fmt.Errorf("investigate mode: write capability detected — violating capability contract")}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		type outcome struct {
			result *investigate.InvestigationResult
			err    error
		}
		outCh := make(chan outcome, 1)

		go func() {
			eng := investigate.NewEngine(".", content, nil, nil)
			result, err := eng.RunContext(ctx)
			outCh <- outcome{result: result, err: err}
		}()

		var result *investigate.InvestigationResult
		var engErr error

		select {
		case o := <-outCh:
			result = o.result
			engErr = o.err
		case <-ctx.Done():
			engErr = fmt.Errorf("investigation timed out after 60s: %w", ctx.Err())
		}

		var recs []record

		if engErr != nil {
			recs = append(recs, record{role: roleError, text: "investigation error: " + engErr.Error()})
		} else if result != nil {
			var b strings.Builder
			fmt.Fprintf(&b, "Problem:    %s\n", result.Problem)
			fmt.Fprintf(&b, "Duration:   %s\n", result.Duration)
			fmt.Fprintf(&b, "Loops:      %d\n", result.Loops)
			if result.Resolved {
				fmt.Fprintf(&b, "Conclusion: %s\n", result.Conclusion)
			} else {
				b.WriteString("Status: Inconclusive\n")
			}

			if len(result.Hypotheses) > 0 {
				b.WriteString("\nHypotheses:\n")
				for _, h := range result.Hypotheses {
					sym := "○"
					switch h.Status {
					case investigate.HypothesisConfirmed:
						sym = "✓"
					case investigate.HypothesisRejected:
						sym = "✗"
					}
					fmt.Fprintf(&b, "  %s %s [%s] (%.0f%%)\n", sym, h.Theory, h.Status, h.Confidence*100)
				}
			}

			if len(result.Evidence) > 0 {
				b.WriteString("\nEvidence:\n")
				for _, ev := range result.Evidence {
					c := ev.Content
					if len(c) > 60 {
						c = c[:60] + "…"
					}
					fmt.Fprintf(&b, "  [%s] %s\n", ev.Source, c)
				}
			}

			if !result.Resolved && result.Error != "" {
				fmt.Fprintf(&b, "\nError: %s\n", result.Error)
			}

			recs = append(recs, record{role: roleAI, text: b.String()})
		}

		esc := buildInvestigationEscalation(content, result, engErr)

		return investigateResultMsg{
			records:           recs,
			sessionKey:        content,
			err:               engErr,
			escalationContent: esc,
		}
	}
}

func buildInvestigationEscalation(content string, result *investigate.InvestigationResult, engErr error) string {
	var escBuilder strings.Builder
	escBuilder.WriteString("## LOCAL TELEMETRY DIAGNOSTICS\n\n")
	fmt.Fprintf(&escBuilder, "**Original User Query:** %s\n\n", content)

	if result != nil {
		fmt.Fprintf(&escBuilder, "**Problem:** %s\n", result.Problem)
		fmt.Fprintf(&escBuilder, "**Duration:** %s\n", result.Duration)
		fmt.Fprintf(&escBuilder, "**Loops:** %d\n", result.Loops)
		fmt.Fprintf(&escBuilder, "**Resolved by engine:** %v\n\n", result.Resolved)

		if len(result.Hypotheses) > 0 {
			escBuilder.WriteString("### Hypotheses Tested\n\n")
			for _, h := range result.Hypotheses {
				statusSym := "✗"
				if h.Status == investigate.HypothesisConfirmed {
					statusSym = "✓"
				}
				fmt.Fprintf(&escBuilder, "- **%s** — %s (%.0f%% confidence) %s\n", h.Theory, h.Status, h.Confidence*100, statusSym)
			}
			escBuilder.WriteString("\n")
		}

		if len(result.Evidence) > 0 {
			escBuilder.WriteString("### Evidence Collected\n\n")
			for _, ev := range result.Evidence {
				fmt.Fprintf(&escBuilder, "- `[%s]` %s\n", ev.Source, ev.Content)
			}
			escBuilder.WriteString("\n")
		}

		if result.Conclusion != "" {
			fmt.Fprintf(&escBuilder, "**Conclusion:** %s\n\n", result.Conclusion)
		}

		if result.Error != "" {
			fmt.Fprintf(&escBuilder, "**Engine Error:** %s\n\n", result.Error)
		}
	} else {
		escBuilder.WriteString("**Engine returned nil result**\n\n")
	}

	if engErr != nil {
		fmt.Fprintf(&escBuilder, "**Execution Error:** %s\n\n", engErr)
	}

	escBuilder.WriteString("---\n")
	escBuilder.WriteString("Analyze the diagnostic telemetry above in context of the original user query. ")
	escBuilder.WriteString("Provide a definitive resolution streamed back to the terminal.\n")
	return escBuilder.String()
}

func (m *model) runReviewCmd(target string) tea.Cmd {
	return tea.Sequence(
		func() tea.Msg {
			return agentStartMsg{label: "reviewing"}
		},
		func() tea.Msg {
			currentMode := m.resolver.Current()
			if currentMode.CanWrite() {
				return reviewResultMsg{err: fmt.Errorf("review mode: write capability detected — review must be 100%% read-only")}
			}
			if currentMode.CanShell() {
				return reviewResultMsg{err: fmt.Errorf("review mode: shell capability detected — review must lock out shell execution")}
			}
			if currentMode.CanPatch() {
				return reviewResultMsg{err: fmt.Errorf("review mode: patch capability detected — review must lock out patch generation")}
			}

			eng := review.NewEngine(".", nil, nil)
			var result *review.ReviewResult
			var err error
			if target != "" {
				result, err = eng.RunTarget(target)
			} else {
				result, err = eng.Run()
			}
			if err != nil {
				return reviewResultMsg{err: err}
			}

			var recs []record
			if result.Error != "" {
				recs = append(recs, record{role: roleSystem, text: result.Error})
				return reviewResultMsg{records: recs}
			}

			var b strings.Builder
			fmt.Fprintf(&b, "Review: %s → %s\n", result.BaseBranch, result.Branch)
			fmt.Fprintf(&b, "Commit: %s · Files Changed: %d · Duration: %s\n", result.CommitHash, len(result.FilesChanged), result.Duration)
			fmt.Fprintf(&b, "Score: %d/100 · Risk Score: %d/100\n", result.Score, result.ImpactRadius.RiskScore)

			if len(result.FilesChanged) > 0 {
				b.WriteString("\nFiles Changed:\n")
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
					fmt.Fprintf(&b, "  %s %s (+%d/-%d)\n", sym, f.Path, f.Additions, f.Deletions)
				}
			}

			if len(result.ImpactRadius.IndirectFiles) > 0 {
				fmt.Fprintf(&b, "\nImpact Radius:\n  Direct: %d · Indirect: %d · Affected Packages: %d\n",
					len(result.ImpactRadius.DirectFiles), len(result.ImpactRadius.IndirectFiles), len(result.ImpactRadius.AffectedPkgs))
			}

			if len(result.RiskFindings) > 0 {
				b.WriteString("\nRisk Findings:\n")
				sevOrder := []review.RiskSeverity{
					review.RiskCritical, review.RiskHigh, review.RiskMedium, review.RiskLow, review.RiskInfo,
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
					fmt.Fprintf(&b, "  [%s] %d findings:\n", strings.ToUpper(string(sev)), len(findings))
					for _, f := range findings {
						fmt.Fprintf(&b, "    • %s:%d — %s\n", f.File, f.Line, f.Description)
					}
				}
			}

			if len(result.Recommendations) > 0 {
				b.WriteString("\nRecommendations:\n")
				for i, rec := range result.Recommendations {
					fmt.Fprintf(&b, "  %d. %s\n", i+1, rec)
				}
			}

			recs = append(recs, record{role: roleAI, text: b.String()})

			sessionKey := result.Branch + "@" + result.CommitHash
			savedResult := result
			return reviewResultMsg{
				records:      recs,
				sessionKey:   sessionKey,
				saveReportFn: func() { _ = review.SaveReport(savedResult, ".") },
			}
		},
	)
}

func (m *model) runUndoCmd() tea.Cmd {
	checkpoints := m.sess.Checkpoints
	if len(checkpoints) == 0 {
		m.push(roleError, "no checkpoints to undo")
		return nil
	}
	lastID := checkpoints[len(checkpoints)-1]
	if err := m.execEng.Checkpoints.Restore(lastID); err != nil {
		m.push(roleError, "undo failed: "+err.Error())
		return nil
	}
	m.sess.Checkpoints = checkpoints[:len(checkpoints)-1]
	_ = m.sess.Save()
	m.push(roleStatus, fmt.Sprintf("undone: restored to checkpoint %s", lastID))
	return nil
}

func (m *model) runCommitCmdAgent() tea.Cmd {
	return tea.Sequence(
		func() tea.Msg {
			return agentStartMsg{label: "generating commit message"}
		},
		func() tea.Msg {
			diff, err := m.gitEng.LastCommitDiff()
			if err != nil {
				return commitGeneratedMsg{err: fmt.Errorf("failed to get diff: %w", err)}
			}
			if strings.TrimSpace(diff) == "" {
				return commitGeneratedMsg{err: fmt.Errorf("no changes in last commit — nothing to amend")}
			}

			payload := fmt.Sprintf("Generate a conventional commit message for these staged changes:\n\n%s", diff)
			sys := prompt.CommitSystemPrompt()
			msgs := []ai.Message{
				{Role: "system", Content: sys},
				{Role: "user", Content: payload},
			}
			req := ai.Request{
				Model:    m.cfg.ActiveModelName(),
				Messages: msgs,
				Stream:   false,
			}
			resp, err := m.provider.Execute(context.Background(), req)
			if err != nil {
				return commitGeneratedMsg{err: fmt.Errorf("LLM call failed: %w", err)}
			}

			raw := resp.Content
			lines := commit.CleanRawLLMOutput(raw)
			var subject, body string
			for i, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if i == 0 {
					subject = commit.SanitizeSubject(line)
				} else {
					body += line + "\n"
				}
			}
			if subject == "" {
				subject = "chore(repo): update repository state"
			}
			bodyLines := strings.Split(strings.TrimSpace(body), "\n")
			body = commit.SanitizeBody(bodyLines)
			msg := commit.CommitMessage{Subject: subject, Body: body}
			finalMessage := fmt.Sprintf("%s\n\n%s\n", msg.Subject, msg.Body)

			if err := m.gitEng.AmendCommit(finalMessage); err != nil {
				return commitGeneratedMsg{err: fmt.Errorf("amend failed: %w", err)}
			}
			hash, _ := m.gitEng.CurrentHash()
			checkpoints := m.sess.Checkpoints
			if len(checkpoints) > 0 {
				m.sess.Checkpoints = checkpoints[:len(checkpoints)-1]
				_ = m.sess.Save()
			}
			return commitGeneratedMsg{subject: msg.Subject, body: msg.Body, hash: hash}
		},
	)
}

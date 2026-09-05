package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/command"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/commit"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/review"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/retrieval"
	riview "github.com/PizenLabs/izen/internal/review"
	"github.com/PizenLabs/izen/internal/session"
)

func (m *model) runInvestigateCmd(content string) tea.Cmd {
	// Set investigateRunning synchronously (event-loop thread) so the view
	// renders the spinner immediately and Esc/Ctrl+C can cancel the in-flight
	// run through the central Emergency Interrupt Registry before the async
	// agentStartMsg is even processed (mirrors runReviewCmd).
	m.investigateRunning = true
	m.lastActionTime = time.Now()

	return tea.Batch(
		func() tea.Msg {
			return agentStartMsg{label: "investigating"}
		},
		m.smoothStreamTickCmd(),
		m.runInvestigateAsyncCmd(content),
	)
}

func (m *model) runInvestigateAsyncCmd(content string) tea.Cmd {
	currentMode := m.resolver.Current()

	// Construct the Context Planner on the UI goroutine so the closure never
	// races lazy construction with the main loop. Nil when no graph is ready.
	planner := m.contextPlanner()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	// Register cancel so it can be invoked on mode transition/Ctrl+C
	m.registerBackgroundCancel(cancel)

	return func() (msg tea.Msg) {
		// ── GUARANTEED LIFECYCLE PATTERN ────────────────────────────────
		// The terminal investigateResultMsg MUST reach the TUI event loop on
		// ANY exit path — success, error, or panic. If the closure itself
		// panics (e.g. a nil result during escalation formatting), the recover
		// below converts it into an error-carrying investigateResultMsg so the
		// spinner can never be orphaned. The named return + defer order
		// guarantees msg is set before cancel() runs.
		defer func() {
			if r := recover(); r != nil {
				msg = investigateResultMsg{
					records:    []record{{role: roleError, text: fmt.Sprintf("investigation failed: %v", r)}},
					err:        fmt.Errorf("investigate pipeline panic: %v", r),
					sessionKey: content,
				}
			}
		}()
		// Release the 60s watchdog once the run completes (a no-op on the
		// registered background cancel if it was already fired by Esc/Ctrl+C).
		defer cancel()

		if !currentMode.CanShell() {
			return investigateResultMsg{err: fmt.Errorf("investigate mode: shell execution denied by %s capabilities", currentMode)}
		}
		if currentMode.CanWrite() {
			return investigateResultMsg{err: fmt.Errorf("investigate mode: write capability detected — violating capability contract")}
		}

		type outcome struct {
			result        *investigate.InvestigationResult
			err           error
			ledgerForPlan string
			engLedger     *investigate.ContextLedger
		}
		outCh := make(chan outcome, 1)

		go func() {
			// ── WORKER LIFETIME (Phase 3) ────────────────────────────────
			// The investigate engine runs on its own goroutine; register it as
			// a worker of the active operation so the terminal-lifecycle tests
			// can prove it is released. A no-op when no operation is attached.
			m.spawnOpWorker("investigate")
			defer m.releaseOpWorker("investigate")

			// Panic guard: a panic inside the engine must still deliver an
			// error outcome so the select below resolves immediately instead of
			// freezing the spinner for the full 60s deadline waiting on outCh.
			defer func() {
				if r := recover(); r != nil {
					outCh <- outcome{err: fmt.Errorf("investigate engine panic: %v", r)}
				}
			}()
			// The investigate retriever's graph tier is served from the Phase 3
			// Lea structural engine when one is attached, degrading to a
			// no-op graph source otherwise.
			retriever := investigate.NewRetrieverAdapter(retrieval.NewRetriever(".", m.leaEng))
			executor := investigate.NewShellTestExecutor(".")
			eng := investigate.NewEngineWithAI(".", content, retriever, executor, m.provider, m.cfg.ActiveModelName())
			eng.WithEventBus(m.bus)
			// Inject the Layer 0-5 pipeline Facade so the investigation can run
			// Layer 4 RAM validation over candidate targets. Nil-safe.
			eng.WithPipelineFacade(m.pipelineFacade())
			// Inject the Context Planner so the forensic diagnostics are
			// enriched with intent-aware, budget-fitted structural context
			// (tool logs, graph symbols) before the orchestrator dispatches.
			if planner != nil {
				eng.WithContextPlanner(planner)
			}
			// Classify intent from the investigation content to enforce ENV_DEPS guard.
			// Feature/UnitTest/Refactor intents skip external dependency search and
			// Docker checks — only Bug/Regression intents get full forensic treatment.
			eng.Intent = investigate.ClassifyIntent(content)
			// Inject workspace snapshot cache and capability registry for
			// archetype-aware diagnostic gating.
			if m.runtimeCtx != nil {
				if m.runtimeCtx.SnapCache != nil {
					eng.WithSnapshotCache(m.runtimeCtx.SnapCache)
				}
				if m.runtimeCtx.CapRegistry != nil {
					eng.WithCapabilityRegistry(m.runtimeCtx.CapRegistry)
				}
			}
			result, err := eng.RunContext(ctx)
			ledgerContent := eng.FormatLedgerForPlan()
			outCh <- outcome{result: result, err: err, ledgerForPlan: ledgerContent, engLedger: eng.Ledger}
		}()

		var result *investigate.InvestigationResult
		var engErr error
		var ledgerForPlan string
		var engLedger *investigate.ContextLedger

		select {
		case o := <-outCh:
			result = o.result
			engErr = o.err
			ledgerForPlan = o.ledgerForPlan
			engLedger = o.engLedger
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
					sym := Icon.Pending
					switch h.Status {
					case investigate.HypothesisConfirmed:
						sym = Icon.Success
					case investigate.HypothesisRejected:
						sym = Icon.Error
					}
					fmt.Fprintf(&b, "  %s %s [%s] (%.0f%%)\n", sym, h.Theory, h.Status, h.Confidence*100)
				}
			}

			if len(result.Evidence) > 0 {
				b.WriteString("\nEvidence:\n")
				for _, ev := range result.Evidence {
					// ANSI-safe truncation: cell-aware so style sequences and
					// wide glyphs are never split mid-way.
					fmt.Fprintf(&b, "  [%s] %s\n", ev.Source, truncateANSI(ev.Content, 60))
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
			ledgerContent:     ledgerForPlan,
			investigateLedger: engLedger,
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
				statusSym := Icon.Error
				if h.Status == investigate.HypothesisConfirmed {
					statusSym = Icon.Success
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

// runReviewTestComposite implements the /review $test composite fast-query:
// it runs the dynamic test suite, injects the telemetry into the forensic
// ledger context, then triggers the risk analysis engine with both the git
// diff AND the test reports. Returns a tea.Cmd so the synchronous pipeline
// never blocks the Bubble Tea event loop.
func (m *model) runReviewTestComposite() tea.Cmd {
	// Set reviewRunning synchronously so Esc/Ctrl+C can abort the composite
	// (test suite + risk engine) before the async agentStartMsg is processed.
	m.reviewRunning = true
	m.lastActionTime = time.Now()
	return tea.Sequence(
		func() tea.Msg {
			return agentStartMsg{label: "review+test"}
		},
		func() tea.Msg {
			res := command.HandleReviewTestComposite(
				&reviewTestExecutor{m: m},
				&reviewLedgerInjector{m: m},
				&reviewRunner{m: m},
			)

			recs := []record{}

			statusLine := Icon.Success + " all tests passed"
			if !res.TestPassed {
				statusLine = Icon.Error + " tests failed — see telemetry below"
			}
			recs = append(recs, record{role: roleSystem, text: statusLine})
			if res.TestReport != "" {
				for _, line := range strings.Split(res.TestReport, "\n") {
					if line == "" {
						continue
					}
					role := roleSystem
					if strings.Contains(line, "FAIL") || strings.Contains(line, "error") {
						role = roleError
					} else if strings.Contains(line, "PASS") || strings.Contains(line, "ok") {
						role = roleStatus
					}
					recs = append(recs, record{role: role, text: line})
				}
			}

			if res.Err != nil {
				return reviewResultMsg{err: res.Err}
			}

			// Telemetry has been injected into the forensic ledger; surface a
			// minimal confirmation line so the pipeline trace is visible.
			recs = append(recs, record{role: roleSystem, text: "[IZEN] Test telemetry injected into forensic ledger."})

			if res.Review != "" {
				recs = append(recs, record{role: roleAI, text: res.Review})
			}

			if res.Ledger != nil {
				testSummary := "Manual $test execution"
				if res.TestPassed {
					testName := extractTestName(res.TestReport)
					if testName != "" {
						testSummary += " passed: " + testName
					} else {
						testSummary += " passed"
					}
				} else {
					testSummary += " completed (see results below)"
				}
				evStatus := riview.EvStatusPassed
				if !res.TestPassed {
					evStatus = riview.EvStatusFailed
				}
				ev := res.Ledger.AddEvidence("", riview.EvTypeExistingTest, evStatus, riview.ConfVerified, "", testSummary)
				recs = append(recs, record{role: roleSystem, text: fmt.Sprintf("[+] Appended Evidence %s [Existing Test]: %s", ev.ID, string(evStatus))})
			}

			return reviewResultMsg{records: recs, ledger: res.Ledger}
		},
	)
}

// extractTestName extracts the first test function name from go test -v output.
func extractTestName(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "=== RUN") {
			parts := strings.SplitN(line, " ", 4)
			if len(parts) >= 3 {
				return strings.TrimSpace(parts[2])
			}
		}
		if strings.Contains(line, "PASS:") {
			parts := strings.Split(line, "PASS:")
			if len(parts) > 1 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// reviewTestExecutor runs the dynamic test suite for the composite pipeline.
type reviewTestExecutor struct {
	m *model
}

func (e *reviewTestExecutor) RunDynamicTests() (bool, string, error) {
	runner := execExecutionRunner(".")
	result, err := runner.RunContext(e.m.operationContext(), "go test -v ./...")
	if err != nil && result == nil {
		return false, err.Error(), err
	}
	output := ""
	passed := true
	if result != nil {
		output = result.Stdout
		if result.Stderr != "" {
			if output != "" {
				output += "\n"
			}
			output += result.Stderr
		}
		if result.ExitCode != 0 {
			passed = false
		}
	}
	e.m.lastTestOutput = output
	e.m.lastTestFailed = !passed
	return passed, output, nil
}

// reviewLedgerInjector feeds test telemetry into the forensic ledger context.
type reviewLedgerInjector struct {
	m *model
}

func (i *reviewLedgerInjector) InjectTestTelemetry(passed bool, telemetry string) error {
	ledger := i.m.sess.ContextLedger
	if ledger == nil {
		ledger = session.NewContextLedger(modes.ModeReview)
	}
	status := "passed"
	if !passed {
		status = "failed"
	}
	ledger.InjectPacket(session.LedgerPacket{
		Kind:    "test_telemetry",
		Title:   "dynamic test suite report",
		Payload: fmt.Sprintf("test suite: %s\n\n%s", status, telemetry),
	})
	i.m.sess.SetContextLedger(ledger)
	return nil
}

// reviewRunner triggers the comprehensive review engine (git diff + ledger).
type reviewRunner struct {
	m *model
}

func (r *reviewRunner) RunComprehensiveReview() (string, *riview.ReviewLedger, error) {
	if cur := r.m.resolver.Current(); cur.CanWrite() || cur.CanShell() || cur.CanPatch() {
		return "", nil, fmt.Errorf("review mode: write/shell/patch capability detected — review must be 100%% read-only")
	}
	eng := review.NewEngine(".", nil, nil).WithEventBus(r.m.bus)
	// Inject the Layer 0-5 pipeline Facade so the review verify step runs the
	// Layer 4 RAM validation DAG over the changed files. Nil-safe.
	eng.WithPipelineFacade(r.m.pipelineFacade())
	result, err := eng.Run()
	if err != nil {
		return "", nil, err
	}
	if result.Error != "" {
		return result.Error, nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "│ Review: %s → %s\n", result.BaseBranch, result.Branch)
	fmt.Fprintf(&b, "│ Commit: %s · Files Changed: %d · Duration: %s\n", result.CommitHash, len(result.FilesChanged), result.Duration)
	fmt.Fprintf(&b, "│ Score: %d/100 · Risk Score: %d/100\n", result.Score, result.ImpactRadius.RiskScore)
	if len(result.RiskFindings) > 0 {
		b.WriteString("│\n│ Risk Findings:\n")
		for _, f := range result.RiskFindings {
			fmt.Fprintf(&b, "│   [%s] %s:%d — %s\n", strings.ToUpper(string(f.Severity)), f.File, f.Line, f.Description)
		}
	}
	if len(result.Recommendations) > 0 {
		b.WriteString("│\n│ Recommendations:\n")
		for i, rec := range result.Recommendations {
			fmt.Fprintf(&b, "│   %d. %s\n", i+1, rec)
		}
	}
	_ = review.SaveReport(result, ".")
	return b.String(), result.Ledger, nil
}

// reviewPipelineTimeout is the hard fallback deadline for a /review pipeline
// run. The review engine is read-only, so a run that exceeds this bound is
// almost certainly wedged (e.g. a sandboxed `go test` stalled on a broken
// module graph) and must be aborted rather than spinning forever.
const reviewPipelineTimeout = 30 * time.Second

func (m *model) runReviewCmd(target string) tea.Cmd {
	// Set reviewRunning synchronously (event-loop thread) so the view renders
	// the spinner immediately and Esc/Ctrl+C can cancel the in-flight run
	// before the async agentStartMsg is even processed.
	m.reviewRunning = true
	m.lastActionTime = time.Now()

	// Strict fallback timeout for the whole /review pipeline. Registered as a
	// background cancel so a mode transition, Esc, or Ctrl+C aborts a stuck
	// run instead of leaving the spinner up forever.
	ctx, cancel := context.WithTimeout(context.Background(), reviewPipelineTimeout)
	m.registerBackgroundCancel(cancel)

	return tea.Sequence(
		func() tea.Msg {
			return agentStartMsg{label: "reviewing"}
		},
		m.smoothStreamTickCmd(),
		func() (msg tea.Msg) {
			// DEFENSIVE CLEANUP: guarantee the terminal reviewResultMsg is
			// delivered on every exit path (including a panic inside the
			// engine), so the spinner can never be left orphaned.
			defer func() {
				if r := recover(); r != nil {
					msg = reviewResultMsg{err: fmt.Errorf("review pipeline panic: %v", r)}
				}
			}()
			// Release the 30s watchdog once the run completes (a no-op on the
			// registered background cancel if it was already fired by Esc).
			defer cancel()

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

			eng := review.NewEngine(".", nil, nil).WithContext(ctx).WithEventBus(m.bus)
			// Inject the Layer 0-5 pipeline Facade for the Layer 4 RAM
			// validation of the changed files during the verify step.
			eng.WithPipelineFacade(m.pipelineFacade())
			var result *review.ReviewResult
			var err error
			if target != "" {
				//nolint:contextcheck // engine consumes the injected ctx internally
				result, err = eng.RunTarget(target)
			} else {
				//nolint:contextcheck // engine consumes the injected ctx internally
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
						fmt.Fprintf(&b, "    "+Icon.Bullet+" %s:%d — %s\n", f.File, f.Line, f.Description)
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

			if result.Ledger != nil {
				pr := riview.NewProvenanceRenderer(result.Ledger, 80)
				recs = append(recs, record{role: roleSystem, text: pr.Render()})
			}

			sessionKey := result.Branch + "@" + result.CommitHash
			savedResult := result
			return reviewResultMsg{
				records:      recs,
				sessionKey:   sessionKey,
				ledger:       result.Ledger,
				saveReportFn: func() { _ = review.SaveReport(savedResult, ".") }, //nolint:contextcheck // SaveReport is a substrate wrapper managing its own context
			}
		},
	)
}

// initSessionStartCheckpoint creates the session-start shadow checkpoint
// to enable /undo --session even across CLI restarts. If a session-start
// checkpoint already exists, it is overwritten with the current state.
func (m *model) initSessionStartCheckpoint() tea.Msg {
	if m.execEng == nil || m.execEng.ShadowCP == nil {
		return nil
	}
	// FIRST-RUN GATE: never create checkpoints (or the .izen/ directory) when
	// .izen/ does not yet exist on disk. This prevents the session-start
	// snapshot from spuriously creating .izen/checkpoints/ which would cause
	// HasLocalState to return true and bypass the TUI onboarding flow.
	if m.workspaceRoot != "" {
		if _, err := os.Stat(filepath.Join(m.workspaceRoot, ".izen")); os.IsNotExist(err) {
			return nil
		}
	}
	_, err := m.execEng.ShadowCP.CreateSessionStartSnapshot()
	if err != nil {
		return nil
	}
	return nil
}

func (m *model) runUndoCmd(raw string) tea.Cmd {
	parts := strings.Fields(raw)
	hasAll := false
	hasSession := false
	for _, p := range parts[1:] {
		switch strings.ToLower(p) {
		case "--all", "all":
			hasAll = true
		case "--session", "session":
			hasSession = true
		}
	}

	if hasAll || hasSession {
		if hasAll {
			if m.gitEng == nil {
				m.push(roleError, "git engine not available")
				return nil
			}
			if err := m.gitEng.CheckoutFile("."); err != nil {
				m.push(roleError, "undo --all failed: "+err.Error())
				return nil
			}
			m.push(roleStatus, Icon.Success+" Reverted all working directory changes")
			return nil
		}
		// --session: restore session-start checkpoint
		if m.execEng == nil || m.execEng.ShadowCP == nil {
			m.push(roleError, "session engine not available")
			return nil
		}
		if err := m.execEng.ShadowCP.RestoreSessionStart(); err != nil {
			m.push(roleError, "undo --session failed: "+err.Error())
			return nil
		}
		m.sess.Checkpoints = nil
		_ = m.sess.Save()
		m.push(roleStatus, Icon.Success+" Reverted all working directory changes")
		return nil
	}

	// Default: single-step undo
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

func (m *model) runCommitCmdAgent(userMsg string) tea.Cmd {
	return tea.Sequence(
		func() tea.Msg {
			label := "generating commit message"
			if userMsg != "" {
				label = "committing"
			}
			return agentStartMsg{label: label}
		},
		m.smoothStreamTickCmd(),
		func() tea.Msg {
			// ── CONSECUTIVE BUILD CHECKPOINT DETECTION ─────────
			// Scan git log for consecutive "izen build:" commits at HEAD.
			// These are temporary checkpoints created during /build and should
			// be squashed into a single semantic commit.
			// CRITICAL: Clamp HEAD~N so it never exceeds the repo's total
			// commit count. When all commits are build checkpoints, diff
			// against the empty tree (no parent commit exists).
			const emptyTreeHash = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

			buildCount := m.gitEng.CountConsecutiveBuildCheckpoints()
			var squashRef string
			useEmptyTree := false
			totalCommits := 0

			if buildCount > 0 {
				totalCommits, _ = m.gitEng.TotalCommits()
				if totalCommits > 0 && buildCount >= totalCommits {
					useEmptyTree = true
					if totalCommits > 1 {
						squashRef = fmt.Sprintf("HEAD~%d", totalCommits-1)
					}
				} else {
					squashRef = fmt.Sprintf("HEAD~%d", buildCount)
				}
			}

			// ── STAGE ALL CHANGES ──────────────────────────────
			if err := m.gitEng.StageAll(); err != nil {
				return commitGeneratedMsg{err: fmt.Errorf("failed to stage changes: %w", err)}
			}

			// ── GET DIFF ───────────────────────────────────────
			var diff string
			var err error
			switch {
			case useEmptyTree:
				diff, err = m.gitEng.DiffRange(emptyTreeHash, "HEAD")
			case squashRef != "":
				diff, err = m.gitEng.DiffRange(squashRef, "HEAD")
			default:
				diff, err = m.gitEng.DiffCached()
			}
			if err != nil {
				return commitGeneratedMsg{err: fmt.Errorf("failed to get diff: %w", err)}
			}
			if strings.TrimSpace(diff) == "" {
				return commitGeneratedMsg{err: fmt.Errorf("no changes to commit")}
			}

			// ── SQUASH BUILD CHECKPOINTS ──────────────────────
			if squashRef != "" {
				if err := m.gitEng.ResetSoft(squashRef); err != nil {
					return commitGeneratedMsg{err: fmt.Errorf("squash failed: %w", err)}
				}
				if err := m.gitEng.StageAll(); err != nil {
					return commitGeneratedMsg{err: fmt.Errorf("re-stage after squash failed: %w", err)}
				}
			}

			// ── GENERATE COMMIT MESSAGE ───────────────────────
			var subject, body string
			if userMsg != "" {
				subject = userMsg
			} else {
				payload := commit.BuildPrompt(diff)
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
				ctx, cancel := context.WithTimeout(m.operationContext(), buildGenerationTimeout)
				resp, err := m.provider.Execute(ctx, req)
				cancel()
				if err != nil {
					return commitGeneratedMsg{err: fmt.Errorf("LLM call failed: %w", err)}
				}
				parsed := commit.ParseGeneratedMessage(resp.Content)
				subject = parsed.Subject
				body = parsed.Body
			}

			// When the sole commit is a build checkpoint (no parent to
			// squash against), amend it instead of creating a new commit.
			if useEmptyTree && totalCommits == 1 {
				msg := subject
				if body != "" {
					msg = subject + "\n\n" + body
				}
				if err := m.gitEng.AmendCommit(msg); err != nil {
					return commitGeneratedMsg{err: fmt.Errorf("amend failed: %w", err)}
				}
			} else {
				if err := m.gitEng.Commit(subject, body); err != nil {
					return commitGeneratedMsg{err: fmt.Errorf("commit failed: %w", err)}
				}
			}

			hash, _ := m.gitEng.CurrentHash()
			return commitGeneratedMsg{subject: subject, body: body, hash: hash}
		},
	)
}

package ui

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	ctxpkg "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/domain"
	objengine "github.com/PizenLabs/izen/internal/engine"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/retrieval"
)

var validSystemCommands = map[string]struct{}{
	"/help":       {},
	"/?":          {},
	"/quit":       {},
	"/mode":       {},
	"/provider":   {},
	"/objective":  {},
	"/clear":      {},
	"/drop":       {},
	"/undo":       {},
	"/commit":     {},
	"/checkpoint": {},
	"/arch":       {},
}

func (m *model) handleInput(line string) tea.Cmd {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	// Clear any stale error bar on new user input
	m.lastApplyError = ""

	// Rigid active guards to block spamming inputs during background processes
	if m.streaming || m.agentRunning {
		m.push(roleSystem, "[System] Input blocked: task execution active.")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// Safety gate confirmation: pending test/run confirmation for large repos
	if m.pendingTestConfirm {
		return m.handleReviewTestConfirm(line)
	}

	if strings.HasPrefix(line, "!") {
		shellCmd := strings.TrimSpace(line[1:])
		if shellCmd == "" {
			m.push(roleSystem, "usage: !<shell command>")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		currentMode := m.resolver.Current()
		if !currentMode.CanShell() {
			m.push(roleError, fmt.Sprintf("shell execution blocked in /%s mode (no CapShell)", currentMode))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}

		// ── Shell Guard Rail: Security-aware command firewall ──
		if blocked, _ := m.shellFirewall(shellCmd); blocked {
			m.reviewRunning = false
			m.agentRunning = false
			m.agentLabel = ""
			m.push(roleError, fmt.Sprintf("[SECURITY ALERT] Dangerous shell mutation blocked: Executing '%s' is strictly forbidden in this mode.", shellCmd))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}

		m.push(roleSystem, "$ "+shellCmd)
		out, err := execShell(shellCmd)
		if err != nil {
			m.push(roleError, err.Error())
		}
		scanner := bufio.NewScanner(strings.NewReader(strings.TrimRight(out, "\r\n")))
		for scanner.Scan() {
			m.push(roleSystem, scanner.Text())
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	// $ sub-command prefix — delegates to handleReviewDollar for routing.
	if strings.HasPrefix(line, "$") {
		// ANTI-DEADLOCK: unconditionally sanitize stale execution flags
		// before spawning any background task. Prevents ghost spinner lock
		// when sequential $ commands are issued without a clean reset.
		cmd := m.handleReviewDollar(line)
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return cmd
	}

	if mode, content, ok := parseModeShorthand(line); ok {
		m.setMode(mode)
		if content == "" {
			return nil
		}
		return m.handleMessageContent(content)
	}

	if strings.HasPrefix(line, "/") {
		return m.handleCommand(line)
	}

	if m.resolver.Current() == modes.ModeBuild {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "run" {
			var stepNum int
			if len(fields) >= 2 {
				stepNum, _ = strconv.Atoi(fields[1])
			}
			return m.handleBuildRun(stepNum)
		}
	}

	return m.handleMessageContent(line)
}

func (m *model) handleMessageContent(line string) tea.Cmd {
	var fileCtx strings.Builder
	var refFiles []string
	var trace *ctxpkg.CodebaseTrace
	for _, field := range strings.Fields(line) {
		if !strings.HasPrefix(field, "@") {
			continue
		}
		ref := filepath.Clean(field[1:])
		if ref == "" || ref == "." {
			continue
		}
		refFiles = append(refFiles, ref)
	}
	refFiles = append(refFiles, m.pendingFileRefs...)
	m.pendingFileRefs = nil

	if m.graph != nil && len(refFiles) > 0 {
		cb := ctxpkg.NewBuilder(".", m.graph, m.gitEng, m.sess)
		renderer := ctxpkg.DefaultRenderer()
		seen := make(map[string]bool)
		for _, ref := range refFiles {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			symName := filepath.Base(ref)
			symExt := filepath.Ext(symName)
			if symExt != "" {
				symName = strings.TrimSuffix(symName, symExt)
			}
			depCtx := cb.BuildDependencySlice(symName)
			if len(depCtx.Files) == 0 {
				fn := m.graph.LookupFile(ref)
				if fn != nil {
					fs := ctxpkg.CompressFile(fn, 30)
					depCtx.Files = append(depCtx.Files, fs)
				}
			}
			if len(depCtx.Files) > 0 {
				if fileCtx.Len() > 0 {
					fileCtx.WriteString("\n")
				}
				fileCtx.WriteString(renderer.Render(depCtx))
				if depCtx.Trace != nil {
					trace = depCtx.Trace
				}
			}
			// Force sync: always include CURRENT file content read fresh from disk
			// so the AI sees the exact byte content rather than relying on cached
			// graph metadata alone.
			data, err := os.ReadFile(ref)
			if err == nil {
				ext := filepath.Ext(ref)
				lang := strings.TrimPrefix(ext, ".")
				lines := strings.Split(string(data), "\n")
				if len(lines) > 50 {
					lines = lines[:50]
				}
				if fileCtx.Len() > 0 {
					fileCtx.WriteString("\n\n")
				}
				fmt.Fprintf(&fileCtx, "## Current Content of: %s\n```%s\n%s\n```",
					ref, lang, strings.Join(lines, "\n"))
			}
		}
	} else if len(refFiles) > 0 {
		for _, ref := range refFiles {
			data, err := os.ReadFile(ref)
			if err != nil {
				continue
			}
			if fileCtx.Len() > 0 {
				fileCtx.WriteString("\n\n")
			}
			ext := filepath.Ext(ref)
			lang := strings.TrimPrefix(ext, ".")
			fmt.Fprintf(&fileCtx, "File: %s\n```%s\n%s\n```", ref, lang, string(data))
		}
	}

	// Inject semantic mapping rules for legal/text files to guide local SLMs
	// that struggle with author/copyright targeting in LICENSE documents.
	if fileCtx.Len() > 0 {
		ctxStr := fileCtx.String()
		lowerCtx := strings.ToLower(ctxStr)
		if strings.Contains(lowerCtx, "license") || strings.Contains(lowerCtx, "readme") {
			semanticRule := `[SEMANTIC MAPPING RULE]: In legal text/LICENSE documents, the "Author", "Holder", or "Organization" corresponds specifically to the string immediately following the "Copyright (c) <Year>" marker. You must strictly target your line mutation to THAT specific line. Do not alter any other paragraph.`
			fileCtx.WriteString("\n\n" + semanticRule)
		}
	}

	line = m.expandFileRefs(line)

	content := strings.TrimSpace(line)
	if fileCtx.Len() > 0 {
		content = fileCtx.String() + "\n\n" + content
	}

	if m.resolver.Current() == modes.ModeBuild && m.graph != nil {
		compressor := retrieval.NewContextCompressorFromGraph(m.graph, m.sess.ObjectiveIntent())
		compressed := compressor.CompressLines(content)
		if compressed != "" && compressed != content {
			content = retrieval.FormatCompressedFrame(compressed) + "\n\n" + content
		}
		// Capture snapshot for background goroutine to avoid data race
		// on m.graph when the main loop assigns a new graph.
		g := m.graph
		go retrieval.BuildGlobalCompressor(g, m.sess.ObjectiveIntent())
	}

	switch m.resolver.Current() {
	case modes.ModeInvestigate:
		if m.investigateInvocationCount >= maxInvestigateInvocations {
			m.push(roleError, fmt.Sprintf("max investigate invocations (%d) reached", maxInvestigateInvocations))
			m.push(roleSystem, infoStyle.Render("start a new session with /objective <desc> or restart"))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		m.investigateInvocationCount++
		return m.runInvestigateCmd(content)
	case modes.ModeReview:
		trimmed := strings.TrimSpace(content)

		target := ""
		if strings.HasPrefix(strings.ToLower(trimmed), "check ") {
			target = strings.TrimSpace(trimmed[6:])
		}
		return m.runReviewCmd(target)
	case modes.ModePlan:
		m.responseBuffer.Reset()
		m.execEng.SetStreamContextFiles(m.attachedFiles)

		cb := ctxpkg.NewBuilder(".", m.graph, m.gitEng, m.sess)
		assembly := cb.BuildPlanAssembly(content, m.attachedFiles)

		modelName := m.cfg.ActiveModelName()
		if budgetErr := plan.CheckTokenBudget(modelName, assembly.EstimateTokens); budgetErr != nil {
			m.push(roleError, budgetErr.Error())
			m.push(roleSystem, infoStyle.Render(budgetErr.BudgetActionHint()))
			return nil
		}

		if m.graph != nil && assembly.EstimateTokens < plan.TokenBudgetForModel(modelName)-1000 {
			query := content
			if m.sess.ObjectiveIntent() != "" {
				query = m.sess.ObjectiveIntent() + " " + query
			}
			lc := retrieval.GetLynxController()
			if lc != nil {
				compressor := retrieval.NewContextCompressorFromGraph(m.graph, m.sess.ObjectiveIntent())
				g := m.graph
				go retrieval.BuildGlobalCompressor(g, m.sess.ObjectiveIntent())
				results, err := lc.SearchRaw(query)
				if err == nil && len(results) > 0 {
					compressed := compressor.CompressResults(results)
					skeleton := retrieval.FormatResultsAsSkeleton(compressed)
					if skeleton != "" {
						augmented := assembly.RawContext + "\n\n" + retrieval.FormatPlanFrame(skeleton)
						augmentedTokens := plan.EstimateTokens(augmented)
						if plan.CheckTokenBudget(modelName, augmentedTokens) == nil {
							assembly.RawContext = augmented
							assembly.EstimateTokens = augmentedTokens
						}
					}
				}
			}
		}

		planTrace := &ctxpkg.CodebaseTrace{}
		for _, sf := range assembly.SymbolFiles {
			planTrace.MatchedFiles = append(planTrace.MatchedFiles, sf.Path)
			for _, sym := range sf.Symbols {
				planTrace.ResolvedSymbols = append(planTrace.ResolvedSymbols, sym.Name)
			}
		}
		return tea.Batch(
			func() tea.Msg { return traceUpdateMsg{trace: planTrace} },
			m.streamCmd(assembly.RawContext),
		)
	default:
		m.responseBuffer.Reset()
		m.execEng.SetStreamContextFiles(m.attachedFiles)

		if m.resolver.Current() == modes.ModeAsk && len(refFiles) == 0 {
			result := retrieval.RouteAsk(line, m.gitEng)
			if len(result.Targets) > 0 && m.graph != nil {
				cb := ctxpkg.NewBuilder(".", m.graph, m.gitEng, m.sess)
				ctx := cb.Build(ctxpkg.BuildRequest{
					Files:      result.Targets,
					MaxFiles:   len(result.Targets),
					MaxSymbols: 20,
				})
				if ctx != nil && len(ctx.Files) > 0 {
					header := fmt.Sprintf("### LOCALIZED CONTEXT (%s)\n\n", result.Label)
					content = header + ctxpkg.DefaultRenderer().Render(ctx) + "\n" + content
					if ctx.Trace != nil {
						trace = ctx.Trace
					}
				}
			}
		}

		if trace != nil {
			return tea.Batch(
				func() tea.Msg { return traceUpdateMsg{trace: trace} },
				m.streamCmd(content),
			)
		}
		return m.streamCmd(content)
	}
}

func parseModeShorthand(line string) (modes.Mode, string, bool) {
	lower := strings.ToLower(strings.TrimSpace(line))
	for _, mode := range []modes.Mode{
		modes.ModeAsk,
		modes.ModePlan,
		modes.ModeBuild,
		modes.ModeInvestigate,
		modes.ModeReview,
	} {
		prefix := "/" + mode.String()
		if lower == prefix {
			return mode, "", true
		}
		if strings.HasPrefix(lower, prefix+" ") {
			return mode, strings.TrimSpace(line[len(prefix):]), true
		}
	}
	return modes.ModeAsk, "", false
}

func (m *model) setMode(mode modes.Mode) {
	m.investigateInvocationCount = 0 // Unconditional state clearance to avoid hard lockout bugs during testing

	// ── ABSOLUTE STALE GOROUTINE RELEASE ON MODE ENTRY ────────────────
	// Before any mode transition, cancel all in-flight background contexts,
	// drain stream buffers, and reset spinner state. This prevents stale
	// tickMsg loops and structural goroutines from a previous mode (e.g.,
	// $test from /review) from corrupting the single-source model state
	// of the new mode — the root cause of spinner frame mutation bugs.
	m.cancelStaleAgentOps()
	m.buildVerifyPending = false

	if mode == m.resolver.Current() {
		return
	}
	oldMode := m.resolver.Current()
	m.startModeTransition(mode)
	m.sess.SetMode(mode)
	_ = m.sess.Save()
	modeColor := modeAccentColor(mode)
	modeLabel := lipgloss.NewStyle().Foreground(modeColor).Render(
		fmt.Sprintf("→ /%s — %s", mode, mode.Description()))
	m.push(roleSystem, modeLabel)
	m.push(roleSystem, fmt.Sprintf("[System] Runtime boundary adjusted: /%s ──> /%s.", oldMode, mode))

	// Handoff context injection primes the target mode with state from the
	// previous mode's terminal event.
	m.injectHandoffContext(mode)
	m.updateActionChips()

	m.refreshViewportContent()
	m.Viewport.GotoBottom()
}

func (m *model) handleCommand(cmd string) tea.Cmd {
	name := strings.Fields(cmd)
	if len(name) == 0 {
		return nil
	}
	if _, ok := validSystemCommands[name[0]]; !ok {
		m.push(roleError, "unknown command: "+cmd)
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	switch {
	case cmd == "/help" || cmd == "/?":
		m.push(roleSystem, labelBoldStyle.Render("modes"))
		m.push(roleSystem, infoStyle.Render("  /ask         explain, inspect, understand (read-only)"))
		m.push(roleSystem, infoStyle.Render("  /plan        architecture, migrations, refactors"))
		m.push(roleSystem, infoStyle.Render("  /build       implement, refactor, write tests"))
		m.push(roleSystem, infoStyle.Render("  /investigate debug bugs, failures, regressions"))
		m.push(roleSystem, infoStyle.Render("  /review      audit changes, detect risks"))
		m.push(roleSystem, "")
		m.push(roleSystem, labelBoldStyle.Render("commands"))
		m.push(roleSystem, infoStyle.Render("  /help  /mode  /provider  /objective  /drop  /clear  /quit"))
		m.push(roleSystem, infoStyle.Render("  /undo  /commit  /checkpoint  /arch"))
		m.push(roleSystem, infoStyle.Render("  /objective approve  approve budget-guarded objective"))
		m.push(roleSystem, infoStyle.Render("  /provider <name>  switch AI provider (ollama|anthropic|openai|gemini)"))
		m.push(roleSystem, infoStyle.Render("  !<cmd>  run a shell command"))
		m.push(roleSystem, "")
		m.push(roleSystem, labelBoldStyle.Render("review sub-commands ($)"))
		m.push(roleSystem, infoStyle.Render("  $test [path]  run tests (safety-gated for large repos)"))
		m.push(roleSystem, infoStyle.Render("  $run  [path]  run go build (safety-gated for large repos)"))
		m.push(roleSystem, infoStyle.Render("  $fix          auto-fix from last test/run failure output"))
		m.push(roleSystem, infoStyle.Render("  $log          evaluate shell trace & run implicit pipeline"))
		m.push(roleSystem, infoStyle.Render(""))
		m.push(roleSystem, labelBoldStyle.Render("investigate sub-commands ($)"))
		m.push(roleSystem, infoStyle.Render("  $env            capture environment diagnostics"))
		m.push(roleSystem, infoStyle.Render("  $trace <fn>     live execution trace with -race"))
		m.push(roleSystem, infoStyle.Render("  $diagnose       root cause analysis from forensic data"))
		m.push(roleSystem, infoStyle.Render("  $log            evaluate shell trace & run implicit pipeline"))
		m.push(roleSystem, "")
		m.push(roleSystem, infoStyle.Render("  @<path>  reference a file in your message"))
		return nil

	case cmd == "/quit":
		m.sess.SetMode(m.resolver.Current())
		_ = m.sess.Save()
		m.push(roleSystem, "goodbye.")
		return tea.Quit

	case strings.HasPrefix(cmd, "/mode"):
		parts := strings.Fields(cmd)
		if len(parts) == 2 {
			mode, ok := modes.Parse(parts[1])
			if ok {
				m.setMode(mode)
				return nil
			}
		}
		m.push(roleSystem, infoStyle.Render("usage: /mode <ask|plan|build|investigate|review>"))
		return nil

	case strings.HasPrefix(cmd, "/provider"):
		parts := strings.Fields(cmd)
		if len(parts) == 2 {
			return m.switchProvider(parts[1])
		}
		m.listProviders()
		return nil

	case strings.HasPrefix(cmd, "/objective"):
		objArg := strings.TrimSpace(strings.TrimPrefix(cmd, "/objective"))
		if strings.EqualFold(objArg, "approve") {
			if m.sess.ObjectiveState == nil {
				m.uiNotice = "No active objective to approve."
				return nil
			}
			m.sess.ObjectiveState.HumanConfirmed = true
			if m.sess.ObjectiveState.CurrentStatus == domain.ObjectiveAnalyzing || m.sess.ObjectiveState.CurrentStatus == domain.ObjectiveIdle {
				m.sess.ObjectiveState.CurrentStatus = domain.ObjectivePlanned
			}
			m.sess.SetObjectiveState(m.sess.ObjectiveState)
			_ = m.sess.Save()
			m.uiNotice = "Objective approved for outbound pipelines."
			return nil
		}
		if objArg != "" {
			m.resetObjectiveContextStacks()
			obj := domain.NewObjective(objArg)
			obj.CurrentStatus = domain.ObjectiveAnalyzing
			m.sess.SetObjectiveState(obj)
			_ = m.sess.Save()
			m.uiNotice = "Objective analysis started."
			return m.analyzeObjectiveCmd(obj)
		} else {
			m.uiNotice = "Usage: /objective <description>"
		}
		return nil

	case cmd == "/clear":
		m.records = nil
		m.PreRenderedHistory = ""
		m.showBanner = true
		m.refreshViewportContent()
		return tea.Sequence(tea.ClearScreen, tea.Println("IZEN cleared."))

	case cmd == "/drop":
		m.attachedFiles = nil
		m.push(roleSystem, infoStyle.Render("context cleared"))
		return nil

	case strings.HasPrefix(cmd, "/drop "):
		target := filepath.Clean(strings.TrimSpace(strings.TrimPrefix(cmd, "/drop")))
		if target == "" || target == "." {
			m.push(roleSystem, infoStyle.Render("usage: /drop <path>"))
			return nil
		}
		filtered := make([]string, 0, len(m.attachedFiles))
		for _, f := range m.attachedFiles {
			if filepath.Clean(f) != target {
				filtered = append(filtered, f)
			}
		}
		if len(filtered) == len(m.attachedFiles) {
			m.push(roleSystem, infoStyle.Render("not attached: "+target))
			return nil
		}
		m.attachedFiles = filtered
		if len(m.attachedFiles) == 0 {
			m.push(roleSystem, infoStyle.Render("context cleared"))
		} else {
			m.push(roleSystem, infoStyle.Render("dropped: "+target))
		}
		return nil

	case cmd == "/undo":
		return m.runUndoCmd()

	case cmd == "/commit":
		return m.runCommitCmdAgent()

	case cmd == "/checkpoint":
		m.push(roleSystem, infoStyle.Render("/checkpoint not yet implemented"))
		return nil

	case cmd == "/arch":
		m.showBanner = false
		m.push(roleSystem, "[System] Reading Graph AST and mapping local repository...")
		m.refreshViewportContent()
		return func() tea.Msg {
			graphText := m.renderArch()
			return archDoneMsg{Content: graphText}
		}

	}

	m.push(roleError, "unknown command: "+cmd)
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
}

func (m *model) startModeTransition(target modes.Mode) {
	m.lineAnimTargetMode = target
	m.lineAnimProgress = 0.0
	m.lineAnimating = true
	m.resolver.Set(target)
}

func (m *model) handleBuildRun(stepNum int) tea.Cmd {
	tasks := m.sess.CurrentTasks
	if len(tasks) == 0 {
		m.push(roleStatus, "no tasks staged — use /plan first")
		return nil
	}
	var targetTask *plan.Task
	if stepNum > 0 {
		for i, t := range tasks {
			if t.StepNum == stepNum {
				targetTask = &tasks[i]
				break
			}
		}
		if targetTask == nil {
			m.push(roleStatus, fmt.Sprintf("task %d not found", stepNum))
			return nil
		}
	} else {
		for i, t := range tasks {
			if t.Status == "idle" {
				targetTask = &tasks[i]
				break
			}
		}
	}
	if targetTask == nil {
		m.push(roleStatus, "all tasks already completed")
		return nil
	}
	targetTask.Status = "processing"
	m.sess.StageTaskList(&tasks)
	_ = m.sess.Save()
	m.push(roleStatus, fmt.Sprintf("executing step %d: %s — %s", targetTask.StepNum, targetTask.Type, targetTask.Target))

	content := fmt.Sprintf("Execute step %d: %s\nTarget: %s\nDescription: %s",
		targetTask.StepNum, targetTask.Type, targetTask.Target, targetTask.Description)

	if m.graph != nil {
		compressor := retrieval.NewContextCompressorFromGraph(m.graph, m.sess.ObjectiveIntent())
		compressed := compressor.CompressLines(content)
		if compressed != "" && compressed != content {
			content = retrieval.FormatCompressedFrame(compressed) + "\n\n" + content
		}
		g := m.graph
		go retrieval.BuildGlobalCompressor(g, m.sess.ObjectiveIntent())
	}

	m.responseBuffer.Reset()
	m.execEng.SetStreamContextFiles(m.attachedFiles)

	buildTrace := &ctxpkg.CodebaseTrace{
		MatchedFiles:    []string{targetTask.Target},
		ResolvedSymbols: []string{targetTask.Target},
	}
	return tea.Batch(
		func() tea.Msg { return traceUpdateMsg{trace: buildTrace} },
		m.streamCmd(content),
	)
}

func (m *model) handleReviewTestConfirm(line string) tea.Cmd {
	m.pendingTestConfirm = false
	target := strings.TrimSpace(line)
	if target == "" || target == "y" || target == "yes" {
		return m.runTestEngine("./...")
	}
	return m.runTestEngine(target)
}

// countGoFiles walks the repository root and counts .go source files,
// excluding vendor/, .izen/, node_modules/, and other generated directories.
func countGoFiles(root string) int {
	count := 0
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == "vendor" || base == ".izen" || base == "node_modules" ||
				base == ".git" || strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(info.Name(), ".go") {
			count++
		}
		return nil
	})
	return count
}

func (m *model) runTestCmd(target string) tea.Cmd {
	if target == "" {
		goFileCount := countGoFiles(".")
		if goFileCount >= 50 {
			warning := fmt.Sprintf(
				"[!] WARNING: Repository contains %d Go source files.\n"+
					"    Running global ./... will scan the entire project.\n"+
					"    Estimated token weight: ~%dk tokens.\n\n"+
					"    Press Enter to confirm global execution, or type a specific\n"+
					"    target path (e.g. ./pkg/foo, ./internal/bar/...).",
				goFileCount, goFileCount*8,
			)
			m.push(roleSystem, warningStyle.Render(warning))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			m.pendingTestConfirm = true
			m.pendingTestTarget = "./..."
			return nil
		}
		return tea.Batch(
			func() tea.Msg { return agentStartMsg{label: "testing"} },
			m.runTestEngine("./..."),
		)
	}
	return tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "testing"} },
		m.runTestEngine(target),
	)
}

func (m *model) runRunCmd(target string) tea.Cmd {
	if target == "" {
		goFileCount := countGoFiles(".")
		if goFileCount >= 50 {
			warning := fmt.Sprintf(
				"[!] WARNING: Repository contains %d Go source files.\n"+
					"    Running global ./... will scan the entire project.\n"+
					"    Estimated token weight: ~%dk tokens.\n\n"+
					"    Press Enter to confirm global execution, or type a specific\n"+
					"    target path (e.g. ./pkg/foo, ./internal/bar/...).",
				goFileCount, goFileCount*8,
			)
			m.push(roleSystem, warningStyle.Render(warning))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			m.pendingTestConfirm = true
			m.pendingTestTarget = "./..."
			return nil
		}
		return tea.Batch(
			func() tea.Msg { return agentStartMsg{label: "building"} },
			m.runBuildEngine("./..."),
		)
	}
	return tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "building"} },
		m.runBuildEngine(target),
	)
}

func (m *model) runTestEngine(target string) tea.Cmd {
	return func() tea.Msg {
		runner := execExecutionRunner(".")
		cmd := "go test -v " + target
		result, err := runner.Run(cmd)
		output := ""
		passed := true
		failedCount := 0
		totalCount := 0

		if result != nil {
			output = result.Stdout
			if result.Stderr != "" {
				if output != "" {
					output += "\n"
				}
				output += result.Stderr
			}
			// Count pass/fail lines
			for _, line := range strings.Split(output, "\n") {
				if strings.Contains(line, "--- FAIL:") {
					failedCount++
				}
				if strings.Contains(line, "--- PASS:") {
					totalCount++
				}
			}
			totalCount += failedCount
			if result.ExitCode != 0 || failedCount > 0 {
				passed = false
			}
		}
		if err != nil && output == "" {
			output = err.Error()
			passed = false
		}

		return testResultMsg{
			output: output,
			passed: passed,
			failed: failedCount,
			total:  totalCount,
			err:    err,
		}
	}
}

func (m *model) runBuildEngine(target string) tea.Cmd {
	return func() tea.Msg {
		runner := execExecutionRunner(".")
		cmd := "go build " + target
		result, err := runner.Run(cmd)
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
		if err != nil && output == "" {
			output = err.Error()
			passed = false
		}

		// Count errors in output
		failedCount := 0
		for _, line := range strings.Split(output, "\n") {
			if strings.Contains(line, ".go:") && (strings.Contains(line, "error") || strings.Contains(line, "cannot")) {
				failedCount++
			}
		}

		return testResultMsg{
			output: output,
			passed: passed,
			failed: failedCount,
			total:  0,
			err:    err,
		}
	}
}

func execExecutionRunner(root string) *executionRunner {
	return &executionRunner{root: root}
}

type executionRunner struct {
	root string
}

func (r *executionRunner) Run(command string) (*executionRunResult, error) {
	c := exec.CommandContext(context.Background(), "bash", "-c", command)
	c.Dir = r.root
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	result := &executionRunResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
	}
	return result, err
}

type executionRunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func (m *model) runFixCmd(target string) tea.Cmd {
	// ── FAIL-SAFE: Belt-and-suspenders write-capability guard ────────────
	if !m.resolver.Current().CanWrite() && !m.resolver.Current().CanPatch() {
		m.cancelStaleAgentOps()
		m.push(roleSystem, mutedStyle.Render("[System] Action rejected: Write access required. Please switch to '/build' mode to execute patches."))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	if m.lastTestOutput == "" {
		m.push(roleError, "no previous test/run output available — run $test or $run first")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	return tea.Batch(
		func() tea.Msg {
			return agentStartMsg{label: "fixing"}
		},
		func() tea.Msg {
			output := m.lastTestOutput
			frames := investigate.ParseStackFrames(output)

			var fixCtx strings.Builder
			fixCtx.WriteString("## FAILURE LOG\n\n```\n")
			fixCtx.WriteString(output)
			fixCtx.WriteString("\n```\n\n")

			if len(frames) > 0 {
				fixCtx.WriteString("## STACK TRACE → SOURCE PROXIMITY\n\n")
				slicer := investigate.NewProximitySlicer(".", 10)
				seen := make(map[string]bool)
				for _, frame := range frames {
					key := fmt.Sprintf("%s:%d", frame.File, frame.Line)
					if seen[key] {
						continue
					}
					seen[key] = true
					slice := slicer.Extract(frame)
					if slice != nil {
						fmt.Fprintf(&fixCtx, "### %s:%d\n\n", slice.File, slice.Line)
						fixCtx.WriteString("```go\n")
						for _, cline := range slice.Context {
							fixCtx.WriteString(cline)
							fixCtx.WriteString("\n")
						}
						fixCtx.WriteString("```\n\n")
					}
				}
			}

			if m.lastTestTarget != "" {
				fmt.Fprintf(&fixCtx, "**Target:** `%s`\n\n", m.lastTestTarget)
			}

			fixCtx.WriteString("## INSTRUCTION\n")
			fixCtx.WriteString("Analyze the test failure(s) above. Identify the root cause in the source code ")
			fixCtx.WriteString("and provide the corrected implementation. Output the minimal fix as a unified diff ")
			fixCtx.WriteString("or complete file replacement.\n")

			return fixResultMsg{content: fixCtx.String()}
		},
	)
}

// ── $log (view mode) — Filtered mutation log display ──────────────────────────
// runLogViewCmd reads .izen/audit/mutations.log and renders entries filtered
// by the active session ContextID. Pass showAll=true to bypass filtering.
func (m *model) runLogViewCmd(showAll bool) tea.Cmd {
	ctxID := ""
	if !showAll && m.sess != nil {
		ctxID = m.sess.ContextID
	}
	return func() tea.Msg {
		logPath := filepath.Join(".izen", "audit", "mutations.log")
		data, err := os.ReadFile(logPath)
		if err != nil {
			m.push(roleStatus, "[System] No mutation log found.")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return agentDoneMsg{}
		}

		lines := strings.Split(string(data), "\n")
		var filtered []string
		for _, line := range lines {
			if line == "" {
				continue
			}
			if ctxID != "" && !strings.Contains(line, "context="+ctxID) {
				continue
			}
			filtered = append(filtered, line)
		}

		if len(filtered) == 0 {
			msg := "[System] $log: No entries"
			if ctxID != "" {
				msg += " for context " + ctxID
			}
			m.push(roleStatus, msg)
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return agentDoneMsg{}
		}

		var b strings.Builder
		b.WriteString("[System] $log: Mutation history")
		if ctxID != "" {
			b.WriteString(" (filtered: " + ctxID + ")")
		}
		b.WriteString("\n")
		for _, line := range filtered {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
		}

		m.push(roleStatus, b.String())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return agentDoneMsg{}
	}
}

// ── $log — Under-the-hood pipeline trigger ─────────────────────────────────────
// runLogCmd receives a shell execution trace, evaluates crash signatures via the
// ContextLedger, and triggers the implicit silent analysis pipeline
// (investigate → plan → build) without visible mode bouncing.
func (m *model) runLogCmd(traceData string) tea.Cmd {
	m.cancelStaleAgentOps()
	m.pipelineRunning = true
	m.pipelineStep = "analyzing trace"

	// Capture raw shell output from the execution runner
	return tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "$log trace analysis"} },
		func() tea.Msg {
			runner := execExecutionRunner(".")
			var output string
			if traceData != "" {
				out, err := runner.Run(traceData)
				if err != nil {
					return logInputMsg{err: err}
				}
				if out != nil {
					output = out.Stdout
					if out.Stderr != "" {
						if output != "" {
							output += "\n"
						}
						output += out.Stderr
					}
				}
			}

			m.push(roleSystem, "[System] $log: Executing under-the-hood trace capture...")

			// Extract stack frames for ledger registration
			frames := investigate.ParseStackFrames(output)
			var files []string
			for _, f := range frames {
				files = append(files, f.File)
			}
			if len(files) > 50 {
				files = files[:50]
			}

			// Register with ContextLedger
			if m.ledger == nil {
				m.ledger = NewContextLedger()
			}
			ledgerID := m.ledger.Record(files, output)

			// Build analysis payload for Step 1 (silent investigation)
			var analysis strings.Builder
			analysis.WriteString("## [$log] UNDER-THE-HOOD TRACE ANALYSIS\n\n")
			fmt.Fprintf(&analysis, "**Ledger ID:** `%s`\n\n", ledgerID)
			analysis.WriteString("## RAW TRACE OUTPUT\n\n```\n")
			analysis.WriteString(output)
			analysis.WriteString("\n```\n\n")

			if len(frames) > 0 {
				analysis.WriteString("## STACK TRACE → SOURCE PROXIMITY\n\n")
				slicer := investigate.NewProximitySlicer(".", 10)
				seen := make(map[string]bool)
				for _, frame := range frames {
					key := fmt.Sprintf("%s:%d", frame.File, frame.Line)
					if seen[key] {
						continue
					}
					seen[key] = true
					slice := slicer.Extract(frame)
					if slice != nil {
						fmt.Fprintf(&analysis, "### %s:%d\n\n```go\n", slice.File, slice.Line)
						for _, cline := range slice.Context {
							analysis.WriteString(cline)
							analysis.WriteString("\n")
						}
						analysis.WriteString("```\n\n")
					}
				}
			}

			analysis.WriteString("## INSTRUCTION\n")
			analysis.WriteString("Analyze the trace above. Identify the root cause. ")
			analysis.WriteString("Output a structured diagnosis with the root cause, evidence, and proposed resolution.\n")

			m.reviewRunning = true
			m.lastActionTime = time.Now()
			return logInputMsg{output: analysis.String()}
		},
	)
}

// ── $log → silent investigate step ──────────────────────────────────────────────
// handleLogInput processes the capture trace and fires the silent investigation
// step through streamCmd (read-only LLM analysis). No mode transition occurs.
func (m *model) handleLogInput(msg logInputMsg) tea.Cmd {
	m.pipelineStep = "analyzing failure"
	if msg.err != nil {
		m.pipelineRunning = false
		m.reviewRunning = false
		m.agentRunning = false
		m.push(roleError, "$log: execution error: "+msg.err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m.flushPendingRecords()
	}

	m.push(roleSystem, "[System] $log: Pipeline Step 1/3 — Silent failure analysis...")
	m.streamCh = nil
	m.streaming = false
	m.streamParser = nil
	flush := m.flushPendingRecords()
	return tea.Batch(flush, m.streamCmd(msg.output))
}

// ── $fix implicit pipeline: investigate → plan → build (no UI bouncing) ──────
// runImplicitFixPipeline executes the three-step silent analysis flow when $fix
// is invoked from /review or globally. The UI does NOT flash between modes.
func (m *model) runImplicitFixPipeline() tea.Cmd {
	m.cancelStaleAgentOps()
	m.pipelineRunning = true
	m.pipelineStep = "analyzing failure"

	if m.lastTestOutput == "" {
		m.pipelineRunning = false
		m.push(roleError, "no previous test/run output available — run $test or $run first")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	return tea.Batch(
		func() tea.Msg { return agentStartMsg{label: "fix pipeline: analyze"} },
		func() tea.Msg {
			output := m.lastTestOutput
			frames := investigate.ParseStackFrames(output)

			// Register with ContextLedger
			if m.ledger == nil {
				m.ledger = NewContextLedger()
			}
			var files []string
			for _, f := range frames {
				files = append(files, f.File)
			}
			if len(files) > 50 {
				files = files[:50]
			}
			ledgerID := m.ledger.Record(files, output)
			m.push(roleSystem, infoStyle.Render(fmt.Sprintf("[System] Pipeline registered as %s — running silent analysis.", ledgerID)))

			var fixCtx strings.Builder
			fmt.Fprintf(&fixCtx, "## FAILURE LOG [%s]\n\n```\n", ledgerID)
			fixCtx.WriteString(output)
			fixCtx.WriteString("\n```\n\n")

			if len(frames) > 0 {
				fixCtx.WriteString("## STACK TRACE → SOURCE PROXIMITY\n\n")
				slicer := investigate.NewProximitySlicer(".", 10)
				seen := make(map[string]bool)
				for _, frame := range frames {
					key := fmt.Sprintf("%s:%d", frame.File, frame.Line)
					if seen[key] {
						continue
					}
					seen[key] = true
					slice := slicer.Extract(frame)
					if slice != nil {
						fmt.Fprintf(&fixCtx, "### %s:%d\n\n```go\n", slice.File, slice.Line)
						for _, cline := range slice.Context {
							fixCtx.WriteString(cline)
							fixCtx.WriteString("\n")
						}
						fixCtx.WriteString("```\n\n")
					}
				}
			}

			if m.lastTestTarget != "" {
				fmt.Fprintf(&fixCtx, "**Target:** `%s`\n\n", m.lastTestTarget)
			}

			fixCtx.WriteString("## PIPELINE INSTRUCTION — STEP 1 (SILENT ANALYSIS)\n")
			fixCtx.WriteString("Analyze the test failure(s) above. Identify the root cause in the source code. ")
			fixCtx.WriteString("Output a structured diagnosis with: root cause, evidence, and proposed resolution.\n")

			m.reviewRunning = true
			m.lastActionTime = time.Now()
			return investigateCompleteMsg{analysis: fixCtx.String(), ledgerID: ledgerID}
		},
	)
}

// handleInvestigateComplete receives the silent analysis and pipes it into plan.
// Step 2: silent blueprinting. No UI mode transition occurs.
func (m *model) handleInvestigateComplete(msg investigateCompleteMsg) tea.Cmd {
	m.pipelineStep = "blueprinting"
	if msg.err != nil {
		m.pipelineRunning = false
		m.reviewRunning = false
		m.agentRunning = false
		m.push(roleError, "fix pipeline: analysis failed: "+msg.err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m.flushPendingRecords()
	}

	m.push(roleSystem, infoStyle.Render("[System] Pipeline Step 2/3 — Formulating fix blueprint..."))
	m.streamCh = nil
	m.streaming = false
	m.streamParser = nil
	m.handoffCtx.ProposedFix = msg.analysis
	flush := m.flushPendingRecords()
	return tea.Batch(flush, m.streamCmd(msg.analysis))
}

// handleBlueprintReady receives the plan output and jumps to /build execution.
// Step 3: Explicit execution jump to /build with the fully realized blueprint.
func (m *model) handleBlueprintReady(msg blueprintReadyMsg) tea.Cmd {
	m.pipelineRunning = false
	m.pipelineStep = ""

	if msg.err != nil {
		m.reviewRunning = false
		m.agentRunning = false
		m.push(roleError, "fix pipeline: blueprint error: "+msg.err.Error())
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return m.flushPendingRecords()
	}

	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("[System] Pipeline Step 3/3 — Blueprint ready [%s]. Jumping to /build for execution...", msg.ledgerID)))

	// ── Explicit UI mode transition to /build ──────────────────────────
	// The exact millisecond the patch blueprint is finalized, we transition.
	m.setMode(modes.ModeBuild)
	m.lastTestOutput = msg.blueprint

	// Reset pipeline flag so the normal boring path finishes cleanly
	m.pipelineRunning = false

	m.streamCh = nil
	m.streaming = false
	m.streamParser = nil
	flush := m.flushPendingRecords()

	// Dispatch the fix command with our blueprint as the failure content
	return tea.Batch(
		flush,
		func() tea.Msg {
			frames := investigate.ParseStackFrames(msg.blueprint)
			var fixCtx strings.Builder
			fixCtx.WriteString("## FIX BLUEPRINT\n\n```\n")
			fixCtx.WriteString(msg.blueprint)
			fixCtx.WriteString("\n```\n\n")

			if len(frames) > 0 {
				fixCtx.WriteString("## STACK TRACE → SOURCE PROXIMITY\n\n")
				slicer := investigate.NewProximitySlicer(".", 10)
				seen := make(map[string]bool)
				for _, frame := range frames {
					key := fmt.Sprintf("%s:%d", frame.File, frame.Line)
					if seen[key] {
						continue
					}
					seen[key] = true
					slice := slicer.Extract(frame)
					if slice != nil {
						fmt.Fprintf(&fixCtx, "### %s:%d\n\n```go\n", slice.File, slice.Line)
						for _, cline := range slice.Context {
							fixCtx.WriteString(cline)
							fixCtx.WriteString("\n")
						}
						fixCtx.WriteString("```\n\n")
					}
				}
			}

			fixCtx.WriteString("## INSTRUCTION\n")
			fixCtx.WriteString("Implement the fix blueprint above. Output the minimal fix as a unified diff ")
			fixCtx.WriteString("or complete file replacement.\n")

			return fixResultMsg{content: fixCtx.String()}
		},
	)
}

// ── Workflow lifecycle: context cancellation for stale goroutine release ──
// cancelStaleAgentOps cancels any in-flight background context and resets
// all agent/spinner state to prevent stale tickMsg loops, goroutine leaks,
// and spinner frame corruption across mode transitions and $sub-command
// re-entry. MUST be called before spawning any new execution loop.
//
// ContextLedger immunity: the ledger data block is preserved during child
// suffix transitions, allowing a sibling sub-scope (#101-sub) to inherit
// the parent state. On new root allocations (completely decoupled crashes),
// the ledger is re-initialized via ResetForNewRoot.
func (m *model) cancelStaleAgentOps() {
	// Stash ContextLedger before clearing everything else
	if m.ledger != nil {
		m.ledgerStash = m.ledger.stashLedgerData()
	}

	m.reviewRunning = false
	m.agentRunning = false
	m.agentDone = false
	m.agentLabel = ""
	m.lastActionTime = time.Time{}
	m.spinnerFrame = 0

	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
	m.streamCh = nil
	m.streaming = false
	m.streamTickActive = false
	m.streamBuffer = ""
	m.currentStreamContent = ""
	m.interruptRequested = false

	// Preserve pipeline state if active (implicit pipeline continues)
	if m.pipelineRunning {
		return
	}

	// Re-hydrate ledger from stash for new root allocations
	if m.ledgerStash != nil {
		if m.ledger == nil {
			m.ledger = NewContextLedger()
		}
		m.ledger.restoreLedgerData(m.ledgerStash)
		m.ledgerStash = nil
	}
}

// handleReviewDollar routes $ sub-commands.
// ModeReview: $test, $run, $fix, $log
// ModeInvestigate: $env, $trace, $diagnose, $log
// Sets reviewRunning synchronously so the view can render an immediate
// spinner before the async agentStartMsg is processed.
func (m *model) handleReviewDollar(line string) tea.Cmd {
	action := strings.TrimSpace(line[1:])
	mode := m.resolver.Current()

	// ── $log — UNDER-THE-HOOD IMPLICIT PIPELINE ──────────────────────────
	// $log evaluates a shell failure trace, fires the silent analysis pipeline
	// (investigate → plan → build) without bouncing the UI between modes.
	// The ContextLedger tracks issues silently via #number scoping.
	//
	// By default $log renders only telemetry and mutation logs matching the
	// active #number context. Pass --all to show the full unfiltered history.
	if action == "log" || strings.HasPrefix(action, "log ") {
		rest := strings.TrimSpace(strings.TrimPrefix(action, "log"))
		if rest == "" || rest == "--all" {
			showAll := rest == "--all"
			return m.runLogViewCmd(showAll)
		}
		return m.runLogCmd(rest)
	}

	// ── INTELLIGENT AUTO-TRANSITION: $fix from /review → /build ─────────
	// When $fix is invoked inside /review, the system detects that Write/Patch
	// capabilities are required and seamlessly transitions to /build mode,
	// carrying the failure context forward for autonomous fix execution.
	if mode == modes.ModeReview && (action == "fix" || strings.HasPrefix(action, "fix ")) {
		if m.lastTestOutput == "" {
			m.cancelStaleAgentOps()
			m.push(roleError, "no previous test/run output available — run $test or $run first")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		if !m.resolver.Current().CanWrite() && !m.resolver.Current().CanPatch() {
			m.cancelStaleAgentOps()
			m.push(roleSystem, mutedStyle.Render("[System] Action rejected: Write access required. Please switch to '/build' mode to execute patches."))
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		m.cancelStaleAgentOps()
		m.push(roleSystem, infoStyle.Render("[System] Running under-the-hood pipeline: silent analysis → blueprint → build..."))
		return m.runImplicitFixPipeline()
	}

	// ── $diagnose in /investigate — runs analysis; auto-transition to /build
	// happens from streamDoneMsg when mutation is detected in the output.
	if mode == modes.ModeInvestigate && (action == "diagnose" || strings.HasPrefix(action, "diagnose ")) {
		m.cancelStaleAgentOps()
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		return m.runDiagnoseCmd()
	}

	// ── $test in /investigate — full Test=Yes privilege. Let run read-only.
	if mode == modes.ModeInvestigate && (action == "test" || strings.HasPrefix(action, "test ")) {
		m.cancelStaleAgentOps()
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		rest := strings.TrimSpace(strings.TrimPrefix(action, "test"))
		return m.runTestCmd(rest)
	}

	// ── ABSOLUTE STALE GOROUTINE RELEASE (ANTI-CORRUPTION) ───────────────
	// Before spawning ANY new execution, kill/drain/cancel all previous
	// background agents. This prevents stale tickMsg loops and structural
	// goroutines from the previous $test/$run from corrupting the single-source
	// model state — which causes the custom star spinner to mutate into defaults.
	m.cancelStaleAgentOps()

	var cmd tea.Cmd

	switch {
	case mode == modes.ModeReview && (action == "test" || strings.HasPrefix(action, "test ")):
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		rest := strings.TrimSpace(strings.TrimPrefix(action, "test"))
		cmd = m.runTestCmd(rest)

	case mode == modes.ModeReview && (action == "run" || strings.HasPrefix(action, "run")):
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		rest := strings.TrimSpace(strings.TrimPrefix(action, "run"))
		cmd = m.runRunCmd(rest)

	case mode == modes.ModeInvestigate && action == "env":
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		cmd = m.runEnvCmd()

	case mode == modes.ModeInvestigate && (strings.HasPrefix(action, "trace ") || action == "trace"):
		m.reviewRunning = true
		m.lastActionTime = time.Now()
		rest := strings.TrimSpace(strings.TrimPrefix(action, "trace"))
		if rest == "" {
			m.push(roleError, "usage: $trace <TestFunctionName>")
			m.refreshViewportContent()
			m.Viewport.GotoBottom()
			return nil
		}
		cmd = m.runTraceCmd(rest)

	default:
		switch mode {
		case modes.ModeReview:
			m.push(roleError, fmt.Sprintf("unknown review action: $%s (use $test, $run, $fix, or $log)", action))
		case modes.ModeInvestigate:
			m.push(roleError, fmt.Sprintf("unknown investigate action: $%s (use $env, $trace, $test, $diagnose, or $log)", action))
		default:
			m.push(roleError, fmt.Sprintf("$ sub-commands not available in /%s mode", mode))
		}
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	if cmd == nil {
		m.reviewRunning = false
		m.lastActionTime = time.Time{}
	}
	return cmd
}

// runEnvCmd captures Go version, git status, and key environment variables
// into a structured [SYSTEM ENVIRONMENT DIAGNOSTICS] block.
func (m *model) runEnvCmd() tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			return agentStartMsg{label: "env diagnostics"}
		},
		func() tea.Msg {
			var b strings.Builder
			b.WriteString("\n═══════════════════════════════════════════\n")
			b.WriteString("  [SYSTEM ENVIRONMENT DIAGNOSTICS]\n")
			b.WriteString("═══════════════════════════════════════════\n")

			goVer, _ := execShell("go version")
			goVer = strings.TrimSpace(goVer)
			fmt.Fprintf(&b, "  Go Version : %s\n", goVer)

			branch, branchErr := m.gitEng.Branch()
			hash, hashErr := m.gitEng.CurrentHash()
			if branchErr == nil {
				fmt.Fprintf(&b, "  Git Branch : %s\n", branch)
			}
			if hashErr == nil {
				fmt.Fprintf(&b, "  Git Commit : %s\n", hash)
			}

			statusOut, _ := execShell("git status --short")
			if strings.TrimSpace(statusOut) != "" {
				b.WriteString("  Git Dirt   :\n")
				for _, line := range strings.Split(strings.TrimRight(statusOut, "\n"), "\n") {
					line = strings.TrimSpace(line)
					if line != "" {
						fmt.Fprintf(&b, "    %s\n", line)
					}
				}
			}

			b.WriteString("  Environment :\n")
			relevantVars := []string{"GOPATH", "GO111MODULE", "GOFLAGS", "GOROOT", "PATH", "SHELL", "TERM", "HOME"}
			for _, name := range relevantVars {
				if val, ok := os.LookupEnv(name); ok {
					fmt.Fprintf(&b, "    %s=%s\n", name, val)
				}
			}

			b.WriteString("═══════════════════════════════════════════\n")

			return envResultMsg{content: b.String()}
		},
	)
}

// runTraceCmd dispatches a live go test -run=[target] -v -race execution
// and captures full stdout/stderr including panic frames and data races.
func (m *model) runTraceCmd(target string) tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			return agentStartMsg{label: "tracing: " + target}
		},
		func() tea.Msg {
			runner := execExecutionRunner(".")
			cmd := "go test -run=" + target + " -v -race 2>&1"
			result, err := runner.Run(cmd)

			output := ""
			passed := true
			failedCount := 0
			totalCount := 0

			if result != nil {
				output = result.Stdout
				if result.Stderr != "" {
					if output != "" {
						output += "\n"
					}
					output += result.Stderr
				}
				for _, line := range strings.Split(output, "\n") {
					if strings.Contains(line, "--- FAIL:") {
						failedCount++
					}
					if strings.Contains(line, "--- PASS:") {
						totalCount++
					}
				}
				totalCount += failedCount
				if result.ExitCode != 0 || failedCount > 0 {
					passed = false
				}
			}
			if err != nil && output == "" {
				output = err.Error()
				passed = false
			}

			return traceResultMsg{
				output: output,
				target: target,
				passed: passed,
				failed: failedCount,
				total:  totalCount,
				err:    err,
			}
		},
	)
}

// runDiagnoseCmd builds the RCA payload from accumulated forensic data
// (LastFailurePayload + $trace + $env) and dispatches it through streamCmd
// with strict output schema constraints.
func (m *model) runDiagnoseCmd() tea.Cmd {
	content := m.buildDiagnoseContent()
	if content == "" {
		return tea.Batch(
			func() tea.Msg {
				m.push(roleError, "$diagnose: no forensic data available — run $env and $trace first")
				m.refreshViewportContent()
				m.Viewport.GotoBottom()
				return agentDoneMsg{}
			},
		)
	}
	return tea.Batch(
		func() tea.Msg {
			return agentStartMsg{label: "rca analysis"}
		},
		func() tea.Msg {
			return diagnoseResultMsg{content: content}
		},
	)
}

// buildDiagnoseContent assembles the LLM prompt from the handoff context.
// Seeds strict RCA output schema and binds all collected forensic data.
func (m *model) buildDiagnoseContent() string {
	if m.handoffCtx.LastFailurePayload == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("You are a Root Cause Analysis engine operating under strict Evidence-First principles.\n")
	b.WriteString("You MUST NOT write casual conversation, ask clarifying questions, or provide generic advice.\n")
	b.WriteString("You MUST adhere strictly to the following normalized Markdown schema in your response:\n\n")
	b.WriteString("### 🚨 RUNTIME ROOT CAUSE ANALYSED\n")
	b.WriteString("- **Issue Detected:** [Concise architectural description of the failure]\n")
	b.WriteString("- **Runtime Evidence:** [Extracted log line, panic frame, or memory state culprit]\n")
	b.WriteString("- **Proposed Resolution:** [Concrete steps to alter code architecture/mutations]\n\n")
	b.WriteString("---\n\n")
	b.WriteString("## FORENSIC DATA\n\n")
	b.WriteString(m.handoffCtx.LastFailurePayload)
	return b.String()
}

// shellFirewall checks a shell command against the security guard rail.
// Returns (blocked, violationMessage).
// Global blacklist applies in all modes; /mode investigate has an additional
// read-only allowlist that rejects anything outside inspection binaries.
func (m *model) shellFirewall(cmd string) (bool, string) {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	if lower == "" {
		return false, ""
	}

	// ── Mode-specific allowlist: /mode investigate — read-only only ──
	if m.resolver.Current() == modes.ModeInvestigate {
		allowed := false
		for _, prefix := range []string{"go test", "go version", "git status", "git diff", "dlv"} {
			if strings.HasPrefix(lower, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			return true, fmt.Sprintf(
				"Dangerous shell mutation blocked: Executing '%s' is strictly forbidden in this mode.",
				cmd)
		}
	}

	// ── Global blacklist ──
	blacklist := []string{"rm ", "sudo", "chmod", "chown", "mkfs", "dd ", "mv /*", "> /dev/gpi"}
	for _, b := range blacklist {
		if strings.Contains(lower, b) {
			return true, fmt.Sprintf(
				"Dangerous shell mutation blocked: Executing '%s' is strictly forbidden in this mode.",
				cmd)
		}
	}

	return false, ""
}

func execShell(cmd string) (string, error) {
	c := exec.CommandContext(context.Background(), "bash", "-c", cmd)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	out := stdout.String()
	if stderr.Len() > 0 {
		if out != "" {
			out += "\n"
		}
		out += stderr.String()
	}
	return out, err
}

func (m *model) analyzeObjectiveCmd(obj *domain.Objective) tea.Cmd {
	return func() tea.Msg {
		resultCh := make(chan objectiveAnalyzedMsg, 1)
		go func() {
			if obj == nil {
				resultCh <- objectiveAnalyzedMsg{err: fmt.Errorf("objective is nil")}
				return
			}
			res := objengine.BuildObjectiveContext(obj.RawIntent, m.cfg.ActiveModelName(), m.graph)
			obj.Scope = res.Scope
			obj.TokenBudget = res.Budget
			obj.Telemetry = append(obj.Telemetry[:0], res.Telemetry...)
			obj.CurrentStatus = domain.ObjectivePlanned
			obj.HumanConfirmed = !res.Budget.RequiresApproval
			resultCh <- objectiveAnalyzedMsg{objective: obj}
		}()
		return <-resultCh
	}
}

func (m *model) resetObjectiveContextStacks() {
	m.pendingFileRefs = nil
	m.attachedFiles = nil
	m.investigateInvocationCount = 0
	m.pendingTestConfirm = false
	m.pendingTestTarget = ""
	m.lastTestOutput = ""
	m.lastTestFailed = false
	m.pendingProposals = nil
	m.awaitingConfirmation = false
	m.acceptAll = false
	m.state = StateChat
	m.recalcViewportHeight()
	m.acceptedProposals = nil
	m.pendingShellExec = nil
	m.shellAwaitingIdx = 0
	m.sess.InvestigationID = ""
	m.sess.ReviewID = ""
	m.sess.ClearHistory()
	m.sess.ClearTasks()
	_ = m.sess.Save()
}

// ── Handoff Pipeline ───────────────────────────────────────────────────────────

// updateActionChips evaluates the current handoff context and mode to
// dynamically populate the active action chips at the UI bottom boundary.
// NOTE: All chip keys use alt+ modifier to avoid key collisions with
// normal text input (see MODIFIER-BASED INPUT SAFETY).
func (m *model) updateActionChips() {
	m.activeChips = nil
	m.showChips = false

	mode := m.resolver.Current()
	switch mode {
	case modes.ModeReview:
		if m.handoffCtx.LastFailurePayload != "" {
			m.activeChips = append(m.activeChips, actionChip{
				key:    "alt+a",
				label:  "Investigate Root Cause",
				action: "/mode investigate",
				query:  "Investigate root cause of the following failure:\n\n" + m.handoffCtx.LastFailurePayload,
			})
			m.showChips = true
		}

	case modes.ModeInvestigate:
		if m.handoffCtx.ProposedFix != "" {
			m.activeChips = append(m.activeChips, actionChip{
				key:    "alt+b",
				label:  "Formulate Execution Plan",
				action: "/mode plan",
				query:  "Formulate an execution plan for the proposed fix:\n\n" + m.handoffCtx.ProposedFix,
			})
			m.showChips = true
		}

	case modes.ModePlan:
		if len(m.handoffCtx.PendingTodos) > 0 {
			var todoBlock strings.Builder
			todoBlock.WriteString("Execute the planned changes with these TODOs:\n")
			for _, t := range m.handoffCtx.PendingTodos {
				fmt.Fprintf(&todoBlock, "  - %s\n", t)
			}
			m.activeChips = append(m.activeChips, actionChip{
				key:    "alt+c",
				label:  "Execute & Verify Patch",
				action: "/mode build",
				query:  todoBlock.String(),
			})
			m.showChips = true
		}

	case modes.ModeBuild:
		// Post-build commit/rollback chips are set dynamically in update.go
		// based on test verification results.
	}
}

// injectHandoffContext primes the target mode with contextual state from the
// previous mode. Called during setMode when a handoff context is available.
func (m *model) injectHandoffContext(mode modes.Mode) {
	switch mode {
	case modes.ModeInvestigate:
		if m.handoffCtx.LastFailurePayload != "" {
			m.push(roleSystem, "[System] Handoff context successfully injected into target mode.")
		}

	case modes.ModePlan:
		if m.handoffCtx.ProposedFix != "" {
			if len(m.handoffCtx.PendingTodos) == 0 {
				m.handoffCtx.PendingTodos = parseProposedFixIntoTodos(m.handoffCtx.ProposedFix)
			}
			m.push(roleSystem, fmt.Sprintf(
				"[System] Handoff context successfully injected into target mode: %d pending TODO(s) from investigation.",
				len(m.handoffCtx.PendingTodos)))
		}

	case modes.ModeBuild:
		if len(m.handoffCtx.PendingTodos) > 0 || m.handoffCtx.ProposedFix != "" {
			m.createBuildCheckpoint(0)
			m.push(roleSystem, "[System] Handoff context successfully injected into target mode. Pre-build checkpoint created.")
		}
	}
}

// handleChipActivation routes a hotkey press to the matching action chip.
// Returns the tea.Cmd to execute, or nil if no chip matched.
func (m *model) handleChipActivation(key string) tea.Cmd {
	for _, chip := range m.activeChips {
		if !strings.EqualFold(chip.key, key) {
			continue
		}
		m.push(roleUser, chip.action)
		m.push(roleSystem, fmt.Sprintf("[System] Action Chip activated: %s.", chip.label))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()

		// Mode transition chips: /mode <name>
		parts := strings.Fields(chip.action)
		if len(parts) >= 2 && parts[0] == "/mode" {
			mode, ok := modes.Parse(parts[1])
			if ok {
				m.setMode(mode)
				if chip.query != "" {
					return m.handleMessageContent(chip.query)
				}
			}
			return nil
		}

		// Direct command chips: /commit, /undo, etc.
		return m.handleCommand(chip.action)
	}
	return nil
}

// parseProposedFixIntoTodos converts a proposed fix (markdown/diff) into a
// checklist of concrete TODO strings for the plan mode dashboard.
func parseProposedFixIntoTodos(fix string) []string {
	lines := strings.Split(fix, "\n")
	var todos []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "- [ ]") || strings.HasPrefix(trimmed, "- [x]") {
			todos = append(todos, strings.TrimSpace(trimmed[5:]))
		} else if strings.HasPrefix(trimmed, "✓ ") || strings.HasPrefix(trimmed, "○ ") || strings.HasPrefix(trimmed, "● ") {
			todos = append(todos, strings.TrimSpace(trimmed[2:]))
		}
	}
	if len(todos) == 0 {
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				todos = append(todos, trimmed)
			}
		}
	}
	if len(todos) > 20 {
		todos = todos[:20]
	}
	return todos
}

// extractTodosFromPlan extracts TODO items from a plan-mode LLM response.
func extractTodosFromPlan(content string) []string {
	tasks := plan.ParseMarkdownToTasks(content)
	if len(tasks) > 0 {
		todos := make([]string, 0, len(tasks))
		for _, t := range tasks {
			label := t.Type + ": " + t.Target
			if t.Description != "" {
				label += " — " + t.Description
			}
			todos = append(todos, label)
		}
		return todos
	}
	return parseProposedFixIntoTodos(content)
}

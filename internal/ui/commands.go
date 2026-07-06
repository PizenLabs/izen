package ui

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	ctxpkg "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/domain"
	objengine "github.com/PizenLabs/izen/internal/engine"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/retrieval"
)

var validSystemCommands = map[string]struct{}{
	"/help":       {}, // true,
	"/?":          {},
	"/quit":       {},
	"/mode":       {},
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
		return nil
	}

	if strings.HasPrefix(line, "!") {
		shellCmd := strings.TrimSpace(line[1:])
		if shellCmd == "" {
			m.push(roleSystem, "usage: !<shell command>")
			return nil
		}
		currentMode := m.resolver.Current()
		if !currentMode.CanShell() {
			m.push(roleError, fmt.Sprintf("shell execution blocked in /%s mode (no CapShell)", currentMode))
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
		return nil
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
		go retrieval.BuildGlobalCompressor(m.graph, m.sess.ObjectiveIntent())
	}

	switch m.resolver.Current() {
	case modes.ModeInvestigate:
		if m.investigateInvocationCount >= maxInvestigateInvocations {
			m.push(roleError, fmt.Sprintf("max investigate invocations (%d) reached", maxInvestigateInvocations))
			m.push(roleSystem, infoStyle.Render("start a new session with /objective <desc> or restart"))
			return nil
		}
		m.investigateInvocationCount++
		return m.runInvestigateCmd(content)
	case modes.ModeReview:
		target := ""
		trimmed := strings.TrimSpace(content)
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
				go retrieval.BuildGlobalCompressor(m.graph, m.sess.ObjectiveIntent())
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
		m.push(roleSystem, infoStyle.Render("  /help  /mode  /objective  /drop  /clear  /quit"))
		m.push(roleSystem, infoStyle.Render("  /undo  /commit  /checkpoint  /arch"))
		m.push(roleSystem, infoStyle.Render("  /objective approve  approve budget-guarded objective"))
		m.push(roleSystem, infoStyle.Render("  !<cmd>  run a shell command"))
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
		go retrieval.BuildGlobalCompressor(m.graph, m.sess.ObjectiveIntent())
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
	m.pendingProposals = nil
	m.awaitingConfirmation = false
	m.acceptAll = false
	m.state = StateChat
	m.acceptedProposals = nil
	m.pendingShellExec = nil
	m.shellAwaitingIdx = 0
	m.sess.InvestigationID = ""
	m.sess.ReviewID = ""
	m.sess.ClearHistory()
	m.sess.ClearTasks()
	_ = m.sess.Save()
}

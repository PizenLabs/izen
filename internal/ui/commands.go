package ui

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	ctxpkg "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/retrieval"
)

var validSystemCommands = map[string]struct{}{
	"/help":       {},
	"/?":          {},
	"/quit":       {},
	"/mode":       {},
	"/objective":  {},
	"/clear":      {},
	"/drop":       {},
	"/undo":       {},
	"/commit":     {},
	"/checkpoint": {},
}

func (m *model) handleInput(line string) tea.Cmd {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	if strings.HasPrefix(line, "!") {
		shellCmd := strings.TrimSpace(line[1:])
		if shellCmd == "" {
			m.push(roleSystem, "usage: !<shell command>")
			return nil
		}
		currentMode := m.resolver.Current()
		if currentMode.ReadOnly() {
			m.push(roleError, fmt.Sprintf("shell execution blocked in /%s mode (read-only)", currentMode))
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

	return m.handleMessageContent(line)
}

func (m *model) handleMessageContent(line string) tea.Cmd {
	var fileCtx strings.Builder
	var refFiles []string
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
			} else {
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
					fileCtx.WriteString(fmt.Sprintf("File: %s\n```%s\n%s\n```",
						ref, lang, strings.Join(lines, "\n")))
				}
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
			fileCtx.WriteString(fmt.Sprintf("File: %s\n```%s\n%s\n```", ref, lang, string(data)))
		}
	}

	line = m.expandFileRefs(line)

	content := strings.TrimSpace(line)
	if fileCtx.Len() > 0 {
		content = fileCtx.String() + "\n\n" + content
	}

	if m.resolver.Current() == modes.ModeBuild && m.graph != nil {
		compressor := retrieval.NewContextCompressorFromGraph(m.graph, m.sess.Objective)
		compressed := compressor.CompressLines(content)
		if compressed != "" && compressed != content {
			content = retrieval.FormatCompressedFrame(compressed) + "\n\n" + content
		}
		go retrieval.BuildGlobalCompressor(m.graph, m.sess.Objective)
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
		return m.runReviewCmd()
	case modes.ModePlan:
		m.responseBuffer.Reset()
		m.execEng.SetStreamContextFiles(m.attachedFiles)
		userContent := prompt.BuildPlanPrompt(m.sess.Objective, content)
		return m.streamCmd(userContent)
	default:
		m.responseBuffer.Reset()
		m.execEng.SetStreamContextFiles(m.attachedFiles)
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
	if mode == m.resolver.Current() {
		return
	}
	m.startModeTransition(mode)
	m.sess.SetMode(mode)
	m.sess.Save()
	modeColor := modeAccentColor(mode)
	modeLabel := lipgloss.NewStyle().Foreground(modeColor).Render(
		fmt.Sprintf("→ /%s — %s", mode, mode.Description()))
	m.push(roleSystem, modeLabel)
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
		m.push(roleSystem, infoStyle.Render("  /undo  /commit  /checkpoint"))
		m.push(roleSystem, infoStyle.Render("  !<cmd>  run a shell command"))
		m.push(roleSystem, "")
		m.push(roleSystem, infoStyle.Render("  @<path>  reference a file in your message"))
		return nil

	case cmd == "/quit":
		m.sess.SetMode(m.resolver.Current())
		m.sess.Save()
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
		obj := strings.TrimSpace(strings.TrimPrefix(cmd, "/objective"))
		if obj != "" {
			m.sess.SetObjective(obj)
			m.sess.Save()
			m.push(roleSystem, infoStyle.Render("objective: "+obj))
		} else {
			m.push(roleSystem, infoStyle.Render("usage: /objective <description>"))
		}
		return nil

	case cmd == "/clear":
		// Reset conversation + restore banner
		m.records = nil
		m.showBanner = true
		m.rebuildViewport()
		return nil

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
	}

	m.push(roleError, "unknown command: "+cmd)
	return nil
}

// startModeTransition kicks off the 150ms color-fade animation.
func (m *model) startModeTransition(target modes.Mode) {
	m.lineAnimTargetMode = target
	m.lineAnimProgress = 0.0
	m.lineAnimating = true
	m.resolver.Set(target)
}

func execShell(cmd string) (string, error) {
	c := exec.Command("bash", "-c", cmd)
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

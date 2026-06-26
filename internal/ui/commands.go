package ui

import (
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
)

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
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			m.push(roleSystem, l)
		}
		return nil
	}

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

	if strings.HasPrefix(line, "/") {
		return m.handleCommand(line)
	}

	m.push(roleUser, line)

	newMode := m.resolver.Resolve(line)
	if newMode != m.resolver.Current() {
		m.resolver.Set(newMode)
		m.sess.SetMode(newMode)
		m.sess.Save()
		modeColor := modeAccentColor(newMode)
		modeLabel := lipgloss.NewStyle().Foreground(modeColor).Render(fmt.Sprintf("→ /%s — %s", newMode, newMode.Description()))
		m.push(roleSystem, modeLabel)
	}

	content := stripModePrefix(line)
	if content == "" {
		return nil
	}

	if fileCtx.Len() > 0 {
		content = fileCtx.String() + "\n\n" + content
	}

	switch m.resolver.Current() {
	case modes.ModeInvestigate:
		if m.investigateInvocationCount >= maxInvestigateInvocations {
			m.push(roleError, fmt.Sprintf("max investigate invocations (%d) reached for this session", maxInvestigateInvocations))
			m.push(roleSystem, infoStyle.Render("start a new session with /objective <desc> or restart"))
			return nil
		}
		m.investigateInvocationCount++
		return m.runInvestigateCmd(content)
	case modes.ModeReview:
		return m.runReviewCmd()
	default:
		m.responseBuffer.Reset()
		return m.streamCmd(content)
	}
}

func (m *model) handleCommand(cmd string) tea.Cmd {
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
		m.push(roleSystem, infoStyle.Render("  /help         show this help"))
		m.push(roleSystem, infoStyle.Render("  /mode <name>  switch mode"))
		m.push(roleSystem, infoStyle.Render("  /objective    set session objective"))
		m.push(roleSystem, infoStyle.Render("  /drop         clear attached context files"))
		m.push(roleSystem, infoStyle.Render("  /drop @<path> remove a specific attached file"))
		m.push(roleSystem, infoStyle.Render("  /quit         exit"))
		m.push(roleSystem, infoStyle.Render("  !<cmd>        run a shell command"))
		m.push(roleSystem, "")
		m.push(roleSystem, labelBoldStyle.Render("file references"))
		m.push(roleSystem, infoStyle.Render("  @<path>  reference a file anywhere in your message"))
		m.push(roleSystem, infoStyle.Render("           supports subdirs, e.g. @internal/ai/client.go"))
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
				m.resolver.Set(mode)
				m.sess.SetMode(mode)
				m.sess.Save()
				modeColor := modeAccentColor(mode)
				modeLabel := lipgloss.NewStyle().Foreground(modeColor).Render(
					fmt.Sprintf("→ /%s — %s", mode, mode.Description()))
				m.push(roleSystem, modeLabel)
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
		m.records = nil
		return nil

	case cmd == "/drop":
		m.attachedFiles = nil
		m.push(roleSystem, infoStyle.Render("context cleared"))
		return nil

	case strings.HasPrefix(cmd, "/drop "):
		target := strings.TrimSpace(strings.TrimPrefix(cmd, "/drop"))
		if target == "" {
			m.attachedFiles = nil
			m.push(roleSystem, infoStyle.Render("context cleared"))
			return nil
		}
		target = filepath.Clean(target)
		filtered := make([]string, 0, len(m.attachedFiles))
		for _, f := range m.attachedFiles {
			if f != target {
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

	case cmd == "/models":
		m.push(roleSystem, infoStyle.Render("active model: "+m.cfg.ActiveModelName()))
		return nil

	case cmd == "/tokens":
		m.push(roleSystem, infoStyle.Render(
			fmt.Sprintf("tokens: %d in / %d out", m.tokenInput, m.tokenOutput)))
		return nil

	case cmd == "/undo":
		m.push(roleSystem, infoStyle.Render("/undo not yet implemented"))
		return nil

	case cmd == "/commit":
		m.push(roleSystem, infoStyle.Render("/commit not yet implemented"))
		return nil

	case cmd == "/checkpoint":
		m.push(roleSystem, infoStyle.Render("/checkpoint not yet implemented"))
		return nil

	case cmd == "/history":
		m.push(roleSystem, infoStyle.Render("/history not yet implemented"))
		return nil

	case cmd == "/resume":
		m.push(roleSystem, infoStyle.Render("/resume not yet implemented"))
		return nil
	}

	for _, mode := range []modes.Mode{
		modes.ModeAsk, modes.ModePlan, modes.ModeBuild,
		modes.ModeInvestigate, modes.ModeReview,
	} {
		prefix := "/" + mode.String()
		if strings.HasPrefix(strings.ToLower(cmd), prefix) {
			m.resolver.Set(mode)
			m.sess.SetMode(mode)
			m.sess.Save()
			modeColor := modeAccentColor(mode)
			modeLabel := lipgloss.NewStyle().Foreground(modeColor).Render(
				fmt.Sprintf("→ /%s — %s", mode, mode.Description()))
			m.push(roleSystem, modeLabel)
			content := strings.TrimSpace(cmd[len(prefix):])
			if content == "" {
				return nil
			}
			m.push(roleUser, content)
			m.responseBuffer.Reset()
			return m.streamCmd(content)
		}
	}

	m.push(roleError, "unknown command: "+cmd)
	return nil
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

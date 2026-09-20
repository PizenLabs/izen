package git

import (
	"strings"

	giteng "github.com/PizenLabs/izen/internal/git"

	"github.com/charmbracelet/lipgloss"
)

var (
	gitPanelBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#74c7ec"))
	gitTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cdd6f4"))
	gitStagedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1"))
	gitUnstagedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
	gitMutedStyle    = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("#6c7086"))
	gitSelStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa"))
)

// Panel is the Git Control Panel modal (Ctrl+G or /git):
//
//	Left pane: staged + unstaged file lists.
//	Right pane: live diff preview of the selected file.
//	Bottom: commit message text input + [ ] Amend previous commit checkbox.
//
// The panel is keyboard-driven and panic-safe: empty repos render an empty
// file list, broken diffs degrade to an error line, and widths < 80 collapse
// to a single column (never panic).
type Panel struct {
	Staged   []string
	Unstaged []string
	Cursor   int
	// DiffPreview is the live `git diff` of the selected file.
	DiffPreview string
	// Form is the bottom commit section.
	Form CommitForm
	// Open reports whether the modal is visible.
	Open bool
	// Dir is the working directory git runs in.
	Dir string
}

// NewPanel builds a closed panel for dir.
func NewPanel(dir string) *Panel {
	if dir == "" {
		dir = "."
	}
	return &Panel{Dir: dir}
}

// Files flattens staged then unstaged into one navigable list.
func (p *Panel) Files() []string {
	out := make([]string, 0, len(p.Staged)+len(p.Unstaged))
	out = append(out, p.Staged...)
	out = append(out, p.Unstaged...)
	return out
}

// SelectedPath returns the file under the cursor ("" when empty).
func (p *Panel) SelectedPath() string {
	files := p.Files()
	if len(files) == 0 {
		return ""
	}
	if p.Cursor < 0 {
		p.Cursor = 0
	}
	if p.Cursor >= len(files) {
		p.Cursor = len(files) - 1
	}
	return files[p.Cursor]
}

// IsStaged reports whether path is in the staged set.
func (p *Panel) IsStaged(path string) bool {
	for _, s := range p.Staged {
		if s == path {
			return true
		}
	}
	return false
}

// MoveUp / MoveDown navigate the left pane (clamped, wrap-safe).
func (p *Panel) MoveUp() {
	if n := len(p.Files()); n > 0 {
		p.Cursor = (p.Cursor - 1 + n) % n
	}
}

// MoveDown navigates down.
func (p *Panel) MoveDown() {
	if n := len(p.Files()); n > 0 {
		p.Cursor = (p.Cursor + 1) % n
	}
}

// Refresh reloads status + diff preview from git. It never errors outward:
// non-repos and empty repos yield empty lists and a muted preview line.
func (p *Panel) Refresh() {
	staged, unstaged := ListStatus(p.Dir)
	p.Staged = staged
	p.Unstaged = unstaged
	if p.Cursor >= len(p.Files()) {
		p.Cursor = 0
	}
	p.Form.RefreshHEAD(p.Dir)
	sel := p.SelectedPath()
	if sel == "" {
		p.DiffPreview = "(no changes)"
		return
	}
	diff, err := GetDiff(p.Dir, sel, p.IsStaged(sel))
	if err != nil || strings.TrimSpace(diff) == "" {
		p.DiffPreview = "(no diff available)"
		return
	}
	p.DiffPreview = diff
}

// AIDraft runs the Ctrl+A AI commit generator over the current diff and
// fills the result directly into the text input.
func (p *Panel) AIDraft() {
	status := strings.Join(append(append([]string{}, p.Staged...), p.Unstaged...), "\n")
	p.Form.SetMessage(GenerateAIDraftString(status, p.DiffPreview))
}

// Render renders the modal at width. Narrow widths (<80) collapse to a single
// column; width < 20 returns "" (never panics).
func (p *Panel) Render(width int) string {
	if width < 20 {
		return ""
	}
	var b strings.Builder
	b.WriteString(gitTitleStyle.Render("Git Control Panel  (Ctrl+G close · Ctrl+A AI message)") + "\n")
	files := p.Files()
	switch {
	case len(files) == 0:
		b.WriteString(gitMutedStyle.Render("(clean tree — nothing staged or unstaged)") + "\n")
	case width < 80:
		// Single-column collapse for narrow terminals.
		for i, f := range files {
			marker := "  "
			if i == p.Cursor {
				marker = gitSelStyle.Render("▸ ")
			}
			style := gitUnstagedStyle
			if p.IsStaged(f) {
				style = gitStagedStyle
			}
			b.WriteString(marker + style.Render(f) + "\n")
		}
		b.WriteString(gitMutedStyle.Render("── diff ──") + "\n")
		b.WriteString(truncateLines(p.DiffPreview, 20) + "\n")
	default:
		// Two-pane: left files (30 cells), right diff preview.
		leftW := 30
		var left strings.Builder
		for i, f := range files {
			marker := "  "
			if i == p.Cursor {
				marker = "▸ "
			}
			name := f
			if len([]rune(name)) > leftW-4 {
				name = string([]rune(name)[:leftW-5]) + "…"
			}
			row := marker + name
			switch {
			case i == p.Cursor:
				row = gitSelStyle.Render(row)
			case p.IsStaged(f):
				row = gitStagedStyle.Render(row)
			default:
				row = gitUnstagedStyle.Render(row)
			}
			left.WriteString(row + "\n")
		}
		right := truncateLines(p.DiffPreview, 30)
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left.String(), " │ ", right) + "\n")
	}
	// Bottom: commit input + amend checkbox.
	amendBox := "[ ]"
	if p.Form.Amend {
		amendBox = "[x]"
	}
	amendLine := amendBox + " Amend previous commit"
	if !p.Form.HasHEAD {
		amendLine = gitMutedStyle.Render(amendBox + " Amend (no HEAD yet)")
	}
	msg := p.Form.Message
	if msg == "" {
		msg = gitMutedStyle.Render("type commit message…")
	}
	b.WriteString(gitMutedStyle.Render("── commit ──") + "\n")
	b.WriteString(msg + "\n")
	b.WriteString(amendLine + "\n")
	return gitPanelBorder.Width(width).Render(strings.TrimSuffix(b.String(), "\n"))
}

// ListStatus splits `git status --porcelain` into staged/unstaged paths via the
// git.Engine seam. Non-repos and empty repos return empty slices (never an
// error outward).
func ListStatus(dir string) (staged, unstaged []string) {
	if dir == "" {
		dir = "."
	}
	return giteng.NewEngine(dir).StatusPaths()
}

// GetDiff returns the live diff preview for path: the staged diff
// (`git diff --cached`) when staged, else the working-tree diff. Broken diffs
// surface as errors (rendered as a muted line, never a panic).
func GetDiff(dir, path string, staged bool) (string, error) {
	if path == "" {
		return "", nil
	}
	if dir == "" {
		dir = "."
	}
	eng := giteng.NewEngine(dir)
	if staged {
		return eng.DiffCachedFile(path)
	}
	return eng.DiffFile(path)
}

func truncateLines(s string, max int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > max {
		lines = append(lines[:max], "… (truncated)")
	}
	return strings.Join(lines, "\n")
}

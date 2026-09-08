package plan

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// SpinnerFrames is the braille spinner cycle for RUNNING steps, driven by
// TUI frame ticks. It mirrors the canonical ProposalSpinnerFrames sequence
// used across the TUI so the plan card animates in sync.
var SpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var (
	planBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#45475a")).
			Padding(0, 1)

	planTitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cdd6f4"))
	planGoalStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	planPendingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	planRunningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#89dceb"))
	planSuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1"))
	planFailedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))
	planMetaStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#585b70"))
)

// SpinnerFrame returns the spinner glyph for a frame tick.
func SpinnerFrame(frame int) string {
	if len(SpinnerFrames) == 0 {
		return "⠋"
	}
	idx := frame % len(SpinnerFrames)
	if idx < 0 {
		idx += len(SpinnerFrames)
	}
	return SpinnerFrames[idx]
}

// Render returns the boxed execution-plan checklist. width caps the box
// width (<=0 means no cap). frame drives the RUNNING spinner animation.
func (p *ExecutionPlan) Render(frame, width int) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	header := planTitleStyle.Render("Execution Plan")
	if strings.TrimSpace(p.Goal) != "" {
		header += " " + planGoalStyle.Render("· "+p.Goal)
	}
	b.WriteString(header)
	if len(p.Steps) == 0 {
		b.WriteString("\n" + planMetaStyle.Render("○ no steps"))
	} else {
		numbering := make([]int, 0, 4)
		renderSteps(&b, p.Steps, numbering, 0, frame)
	}
	completed, running, _, total := p.Counts()
	summary := fmt.Sprintf("%d/%d completed", completed, total)
	if running > 0 {
		summary += fmt.Sprintf(" · %d running", running)
	}
	summary += fmt.Sprintf(" · %s", formatElapsed(p.TotalElapsed()))
	b.WriteString("\n" + planMetaStyle.Render(summary))
	box := planBoxStyle.Render(b.String())
	if width > 0 && lipgloss.Width(box) > width {
		box = planBoxStyle.Width(width).Render(b.String())
	}
	return box
}

func renderSteps(b *strings.Builder, steps []PlanStep, numbering []int, depth, frame int) {
	for i, s := range steps {
		num := append(append([]int(nil), numbering...), i+1)
		label := formatNumber(num)
		prefix := ""
		if depth > 0 {
			prefix = strings.Repeat("  ", depth)
		}
		icon, line := formatStepLine(s, label, frame)
		b.WriteString("\n" + prefix + icon + " " + line)
		if s.Status == StatusFailed && strings.TrimSpace(s.Error) != "" {
			b.WriteString("\n" + prefix + "  " + planFailedStyle.Render("└─ "+s.Error))
		}
		if len(s.SubSteps) > 0 {
			renderSteps(b, s.SubSteps, num, depth+1, frame)
		}
	}
}

func formatNumber(num []int) string {
	parts := make([]string, len(num))
	for i, n := range num {
		parts[i] = fmt.Sprintf("%d", n)
	}
	return strings.Join(parts, ".") + "."
}

func formatStepLine(s PlanStep, label string, frame int) (string, string) {
	title := s.Title
	if strings.TrimSpace(title) == "" {
		title = s.ID
	}
	elapsed := ""
	if s.ElapsedTime > 0 {
		elapsed = " (" + formatElapsed(s.ElapsedTime) + ")"
	}
	switch s.Status {
	case StatusRunning:
		spinner := SpinnerFrame(frame)
		return planRunningStyle.Render(spinner),
			planRunningStyle.Render(label+" "+title) + planMetaStyle.Render(" (running "+formatElapsed(s.ElapsedTime)+")")
	case StatusSuccess:
		return planSuccessStyle.Render("✓"),
			planSuccessStyle.Render(label+" "+title) + planMetaStyle.Render(elapsed)
	case StatusFailed:
		detail := "failed"
		if strings.TrimSpace(s.Error) != "" {
			detail = "failed: " + s.Error
		} else if s.ElapsedTime > 0 {
			detail = "failed " + formatElapsed(s.ElapsedTime)
		}
		return planFailedStyle.Render("✗"),
			planFailedStyle.Render(label+" "+title) + planMetaStyle.Render(" ("+detail+")")
	default:
		return planPendingStyle.Render("○"),
			planPendingStyle.Render(label+" "+title) + planMetaStyle.Render(elapsed)
	}
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	secs := d.Seconds()
	if secs < 60 {
		return fmt.Sprintf("%.1fs", secs)
	}
	m := int(secs) / 60
	rem := secs - float64(m*60)
	return fmt.Sprintf("%dm%.0fs", m, rem)
}

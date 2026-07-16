package context

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ── Sliding-Window Renderer ──────────────────────────────────────────────────
//
// The Renderer implements the Sliding Window rendering strategy:
// WindowSize = 1. Even if the Context carries references to dozens of tasks
// and files, the rendered prompt contains ONLY:
//   1. The objective and current mode
//   2. The active task (first pending, never future tasks)
//   3. The minimal code diff / files for that task
//   4. Errors compressed to root cause positions
//
// Future tasks MUST NOT be rendered until the current active task transitions
// to TaskCompleted.
// ─────────────────────────────────────────────────────────────────────────────

type Renderer struct {
	MaxDiffLines int
	MaxFileLines int
	Ledger       *TaskLedger
}

func DefaultRenderer() *Renderer {
	return &Renderer{
		MaxDiffLines: 100,
		MaxFileLines: 50,
	}
}

// SetLedger attaches the task ledger for sliding-window rendering.
func (r *Renderer) SetLedger(l *TaskLedger) {
	r.Ledger = l
}

func (r *Renderer) Render(ctx *Context) string {
	var b strings.Builder

	r.renderObjective(&b, ctx)
	r.renderMode(&b, ctx)
	r.renderActiveWindow(&b, ctx)
	r.renderDiff(&b, ctx)
	r.renderFiles(&b, ctx)
	r.renderErrors(&b, ctx)

	return b.String()
}

// renderActiveWindow implements the sliding-window constraint: it renders only
// the first pending task (never future tasks), producing an O(1) runtime
// task metadata payload for the LLM.
func (r *Renderer) renderActiveWindow(b *strings.Builder, ctx *Context) {
	// Determine which tasks fall within the active window.
	var activeIDs []int
	allTasks := ctx.TaskStatusSnapshot

	if allTasks != nil && r.Ledger != nil {
		fp := r.Ledger.FirstPending()
		if fp > 0 {
			activeIDs = append(activeIDs, fp)
		}
	}

	// Always render working tree status regardless of task window.
	r.renderStatus(b, ctx)

	// Render compressed task window header.
	if r.Ledger != nil {
		total := r.Ledger.TotalPending()
		if total > 0 {
			fmt.Fprintf(b, "# Active Tasks (Windowed — %d pending)\n", total)
			fmt.Fprintf(b, "Showing 1 of %d pending tasks. Future tasks are excluded\n", total)
			fmt.Fprintf(b, "until the current task completes.\n\n")

			if len(activeIDs) > 0 {
				for _, id := range activeIDs {
					status := r.Ledger.Status(id)
					fmt.Fprintf(b, "- [%s] Task #%d\n", status, id)
				}
			} else if total > 0 {
				// Fallback: task exists but no first-pending found (edge case).
				fmt.Fprintf(b, "- Pending task available (waiting for execution)\n")
			}
			b.WriteString("\n")
		}
	}
}

func (r *Renderer) renderObjective(b *strings.Builder, ctx *Context) {
	if ctx.Objective != "" {
		b.WriteString("# Objective\n")
		b.WriteString(ctx.Objective)
		b.WriteString("\n\n")
	}
}

func (r *Renderer) renderMode(b *strings.Builder, ctx *Context) {
	b.WriteString("# Mode\n")
	b.WriteString(ctx.Mode)
	b.WriteString("\n")
	if ctx.Query != "" {
		b.WriteString("Query: ")
		b.WriteString(ctx.Query)
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func (r *Renderer) renderStatus(b *strings.Builder, ctx *Context) {
	if len(ctx.Status) == 0 {
		return
	}
	b.WriteString("# Working Tree\n")
	for _, s := range ctx.Status {
		b.WriteString("  ")
		b.WriteString(s)
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func (r *Renderer) renderDiff(b *strings.Builder, ctx *Context) {
	if ctx.Diff == "" {
		return
	}
	lines := strings.Split(ctx.Diff, "\n")
	if len(lines) > r.MaxDiffLines {
		lines = lines[:r.MaxDiffLines]
	}
	b.WriteString("# Changes\n")
	b.WriteString("```diff\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if len(lines) < len(strings.Split(ctx.Diff, "\n")) {
		b.WriteString("... (truncated)\n")
	}
	b.WriteString("```\n\n")
}

func (r *Renderer) renderFiles(b *strings.Builder, ctx *Context) {
	if len(ctx.Files) == 0 {
		return
	}
	b.WriteString("# Relevant Code\n")

	for _, f := range ctx.Files {
		b.WriteString("## ")
		b.WriteString(f.Path)
		b.WriteString("\n")

		if f.Package != "" {
			b.WriteString("package: ")
			b.WriteString(f.Package)
			b.WriteString("\n")
		}
		if len(f.Imports) > 0 {
			b.WriteString("imports: ")
			b.WriteString(strings.Join(f.Imports, ", "))
			b.WriteString("\n")
		}

		r.renderFileSymbols(b, f)

		if f.Lines > 0 {
			fmt.Fprintf(b, "(%s, %d lines, %d symbols)\n\n",
				filepath.Base(f.Path), f.Lines, len(f.Symbols))
		} else {
			b.WriteString("\n")
		}
	}
}

func (r *Renderer) renderFileSymbols(b *strings.Builder, f FileSlice) {
	if len(f.Symbols) == 0 {
		return
	}
	b.WriteString("symbols:\n")
	for _, sym := range f.Symbols {
		b.WriteString("  ")
		b.WriteString(sym.Kind)
		b.WriteString(" ")
		b.WriteString(sym.Name)
		if sym.Signature != "" {
			b.WriteString(" ")
			b.WriteString(sym.Signature)
		}
		fmt.Fprintf(b, " [L%d]", sym.Line)
		if sym.Exported {
			b.WriteString(" (exported)")
		}
		b.WriteString("\n")
	}
}

func (r *Renderer) renderErrors(b *strings.Builder, ctx *Context) {
	if len(ctx.Errors) == 0 {
		return
	}
	// Compressed error rendering: only unique, deduplicated errors.
	seen := make(map[string]bool)
	b.WriteString("# Errors (Deduplicated — Root Causes First)\n")
	for _, e := range ctx.Errors {
		if seen[e] {
			continue
		}
		seen[e] = true
		b.WriteString("- ")
		b.WriteString(e)
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func (r *Renderer) RenderCompact(ctx *Context) string {
	var b strings.Builder

	if ctx.Objective != "" {
		b.WriteString(ctx.Objective)
		b.WriteString("\n")
	}
	if len(ctx.Status) > 0 {
		b.WriteString("changed: ")
		b.WriteString(strings.Join(ctx.Status, ", "))
		b.WriteString("\n")
	}

	// Compact windowed task status.
	if r.Ledger != nil {
		fp := r.Ledger.FirstPending()
		if fp > 0 {
			fmt.Fprintf(&b, "active-task: #%d (%s)\n", fp, r.Ledger.Status(fp))
		}
	}

	for _, f := range ctx.Files {
		b.WriteString(f.Path)
		n := len(f.Symbols)
		if n > 0 {
			fmt.Fprintf(&b, " (%d syms)", n)
		}
		b.WriteString("\n")
	}
	if ctx.Diff != "" {
		b.WriteString("diff present\n")
	}

	return b.String()
}

func (r *Renderer) Size(ctx *Context) Stats {
	s := ctx.Stats()
	s.PromptChars = len(r.Render(ctx))
	return s
}

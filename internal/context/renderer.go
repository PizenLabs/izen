package context

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Renderer struct {
	MaxDiffLines int
	MaxFileLines int
}

func DefaultRenderer() *Renderer {
	return &Renderer{
		MaxDiffLines: 100,
		MaxFileLines: 50,
	}
}

func (r *Renderer) Render(ctx *Context) string {
	var b strings.Builder

	r.renderObjective(&b, ctx)
	r.renderMode(&b, ctx)
	r.renderStatus(&b, ctx)
	r.renderDiff(&b, ctx)
	r.renderFiles(&b, ctx)
	r.renderErrors(&b, ctx)

	return b.String()
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
			b.WriteString(fmt.Sprintf("(%s, %d lines, %d symbols)\n\n",
				filepath.Base(f.Path), f.Lines, len(f.Symbols)))
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
		b.WriteString(fmt.Sprintf(" [L%d]", sym.Line))
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
	b.WriteString("# Errors\n")
	for _, e := range ctx.Errors {
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
	for _, f := range ctx.Files {
		b.WriteString(f.Path)
		n := len(f.Symbols)
		if n > 0 {
			b.WriteString(fmt.Sprintf(" (%d syms)", n))
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

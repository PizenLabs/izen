package adapter

import (
	"fmt"
	"strings"

	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
)

// ReactViteAdapter is the pure render adapter for a React single-page app on
// Vite. It maps Logical IR nodes onto the SPA layout
// (src/pages/<Name>.tsx, src/components/<Name>.tsx,
// src/handlers/<name>.ts, db/migrations/<name>.sql).
// It contains no planning logic and never mutates the node it renders.
type ReactViteAdapter struct{}

// NewReactViteAdapter returns a React + Vite render adapter.
func NewReactViteAdapter() *ReactViteAdapter { return &ReactViteAdapter{} }

// Framework implements FrameworkAdapter.
func (a *ReactViteAdapter) Framework() Framework { return FrameworkReactVite }

// RenderNode implements FrameworkAdapter.
func (a *ReactViteAdapter) RenderNode(node ir.IRNode) ([]FileArtifact, error) {
	switch n := node.(type) {
	case *ir.CreatePageNode:
		return []FileArtifact{{
			Path:    joinPath("src", "pages", pascal(n.Name)+".tsx"),
			Content: renderVitePage(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateSectionNode:
		return []FileArtifact{{
			Path:    joinPath("src", "components", pascal(n.Name)+".tsx"),
			Content: renderViteSection(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateComponentNode:
		return []FileArtifact{{
			Path:    joinPath("src", "components", pascal(n.Name)+".tsx"),
			Content: renderViteComponent(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateEndpointNode:
		return []FileArtifact{{
			Path:    joinPath("src", "handlers", slug(n.Name)+".ts"),
			Content: renderViteHandler(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateDatabaseMigrationNode:
		return []FileArtifact{{
			Path:    joinPath("db", "migrations", slug(n.Name)+".sql"),
			Content: renderMigration(n.Name, n.Description),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateStyleNode:
		return []FileArtifact{{
			Path:    joinPath("src", slug(n.Name)+".css"),
			Content: renderViteStyle(n.Name, n.Description),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateScriptNode:
		return []FileArtifact{{
			Path:    joinPath("src", slug(n.Name)+".ts"),
			Content: renderViteScript(n.Name, n.Behavior),
			Mode:    defaultFileMode,
		}}, nil
	default:
		return nil, fmt.Errorf("adapter react-vite: unsupported node kind %q", node.Kind())
	}
}

// renderViteStyle renders a stylesheet imported by the app entry.
func renderViteStyle(name, description string) string {
	var b strings.Builder
	b.WriteString("/* " + slug(name) + " */\n")
	if description != "" {
		b.WriteString("/* " + esc(description) + " */\n")
	}
	b.WriteString(":root {\n")
	b.WriteString("  --font-sans: system-ui, -apple-system, sans-serif;\n")
	b.WriteString("}\n")
	b.WriteString("body {\n")
	b.WriteString("  margin: 0;\n")
	b.WriteString("  font-family: var(--font-sans);\n")
	b.WriteString("}\n")
	return b.String()
}

// renderViteScript renders an app-side behaviour module.
func renderViteScript(name, behavior string) string {
	var b strings.Builder
	b.WriteString("// " + slug(name) + "\n")
	if behavior != "" {
		b.WriteString("// " + esc(behavior) + "\n")
	}
	b.WriteString("export function init() {\n")
	b.WriteString("  console.log('ready');\n")
	b.WriteString("}\n")
	return b.String()
}

// renderVitePage renders a React page component.
func renderVitePage(n *ir.CreatePageNode) string {
	var b strings.Builder
	b.WriteString("import type { ReactElement } from \"react\";\n\n")
	b.WriteString("export default function " + pascal(n.Name) + "Page(): ReactElement {\n")
	b.WriteString("  return (\n")
	b.WriteString("    <main>\n")
	b.WriteString("      <h1>" + esc(n.PageTitle()) + "</h1>\n")
	for _, s := range n.SectionNames() {
		b.WriteString("      <section>" + esc(s) + "</section>\n")
	}
	b.WriteString("    </main>\n")
	b.WriteString("  );\n")
	b.WriteString("}\n")
	return b.String()
}

// renderViteSection renders a React section component.
func renderViteSection(n *ir.CreateSectionNode) string {
	var b strings.Builder
	b.WriteString("import type { ReactElement } from \"react\";\n\n")
	b.WriteString("export default function " + pascal(n.Name) + "(): ReactElement {\n")
	b.WriteString("  return (\n")
	b.WriteString("    <section className=\"" + slug(n.Name) + "\">\n")
	if n.Content != "" {
		b.WriteString("      <p>{`" + esc(n.Content) + "`}</p>\n")
	}
	b.WriteString("    </section>\n")
	b.WriteString("  );\n")
	b.WriteString("}\n")
	return b.String()
}

// renderViteComponent renders a reusable React component.
func renderViteComponent(n *ir.CreateComponentNode) string {
	var b strings.Builder
	b.WriteString("import type { ReactElement } from \"react\";\n\n")
	b.WriteString("export default function " + pascal(n.Name) + "(): ReactElement {\n")
	if n.Purpose != "" {
		b.WriteString("  // " + esc(n.Purpose) + "\n")
	}
	b.WriteString("  return <div className=\"" + slug(n.Name) + "\" />;\n")
	b.WriteString("}\n")
	return b.String()
}

// renderViteHandler renders a fetch-style handler for a React SPA.
func renderViteHandler(n *ir.CreateEndpointNode) string {
	var b strings.Builder
	b.WriteString("export async function " + pascal(n.Name) + "(")
	b.WriteString("request: Request) {\n")
	if n.Description != "" {
		b.WriteString("  // " + esc(n.Description) + "\n")
	}
	b.WriteString("  return new Response(JSON.stringify({ ok: true }), {\n")
	b.WriteString("    headers: { \"content-type\": \"application/json\" },\n")
	b.WriteString("  });\n")
	b.WriteString("}\n")
	return b.String()
}

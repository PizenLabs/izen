package adapter

import (
	"fmt"
	"strings"

	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
)

// NextJSAdapter is the pure render adapter for the Next.js app router. It
// maps Logical IR nodes onto the Next.js file layout (app/<slug>/page.tsx,
// app/api/<route>/route.ts, app/components/<name>.tsx, migrations/<name>.sql).
// It contains no planning logic: the same node renders the same way every
// time, and the node is never mutated.
type NextJSAdapter struct{}

// NewNextJSAdapter returns a Next.js render adapter.
func NewNextJSAdapter() *NextJSAdapter { return &NextJSAdapter{} }

// Framework implements FrameworkAdapter.
func (a *NextJSAdapter) Framework() Framework { return FrameworkNextJS }

// RenderNode implements FrameworkAdapter.
func (a *NextJSAdapter) RenderNode(node ir.IRNode) ([]FileArtifact, error) {
	switch n := node.(type) {
	case *ir.CreatePageNode:
		return []FileArtifact{{
			Path:    joinPath("app", slug(n.Name), "page.tsx"),
			Content: renderNextPage(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateSectionNode:
		return []FileArtifact{{
			Path:    joinPath("app", "components", slug(n.Name)+".tsx"),
			Content: renderNextSection(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateComponentNode:
		return []FileArtifact{{
			Path:    joinPath("app", "components", slug(n.Name)+".tsx"),
			Content: renderNextComponent(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateEndpointNode:
		return []FileArtifact{{
			Path:    joinPath("app", "api", routeSegments(n.Route), "route.ts"),
			Content: renderNextRoute(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateDatabaseMigrationNode:
		return []FileArtifact{{
			Path:    joinPath("migrations", slug(n.Name)+".sql"),
			Content: renderMigration(n.Name, n.Description),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateStyleNode:
		return []FileArtifact{{
			Path:    joinPath("app", slug(n.Name)+".css"),
			Content: renderNextStyle(n.Name, n.Description),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateScriptNode:
		return []FileArtifact{{
			Path:    joinPath("app", "lib", slug(n.Name)+".ts"),
			Content: renderNextScript(n.Name, n.Behavior),
			Mode:    defaultFileMode,
		}}, nil
	default:
		return nil, fmt.Errorf("adapter nextjs: unsupported node kind %q", node.Kind())
	}
}

// renderNextStyle renders a global stylesheet imported by the app layout.
func renderNextStyle(name, description string) string {
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

// renderNextScript renders a client-side behaviour module.
func renderNextScript(name, behavior string) string {
	var b strings.Builder
	b.WriteString("\"use client\";\n\n")
	b.WriteString("export function init" + pascal(name) + "() {\n")
	if behavior != "" {
		b.WriteString("  // " + esc(behavior) + "\n")
	}
	b.WriteString("  console.log(\"ready\");\n")
	b.WriteString("}\n")
	return b.String()
}

// renderNextPage renders a Next.js App Router page component.
func renderNextPage(n *ir.CreatePageNode) string {
	var b strings.Builder
	b.WriteString("export default function " + pascal(n.Name) + "Page() {\n")
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

// renderNextSection renders a section as a server component under
// app/components.
func renderNextSection(n *ir.CreateSectionNode) string {
	var b strings.Builder
	b.WriteString("export default function " + pascal(n.Name) + "() {\n")
	b.WriteString("  return (\n")
	b.WriteString("    <section className=\"" + slug(n.Name) + "\">\n")
	if n.Content != "" {
		b.WriteString("      <p>" + esc(n.Content) + "</p>\n")
	}
	b.WriteString("    </section>\n")
	b.WriteString("  );\n")
	b.WriteString("}\n")
	return b.String()
}

// renderNextComponent renders a reusable client component.
func renderNextComponent(n *ir.CreateComponentNode) string {
	var b strings.Builder
	b.WriteString("\"use client\";\n\n")
	b.WriteString("export default function " + pascal(n.Name) + "() {\n")
	if n.Purpose != "" {
		b.WriteString("  // " + esc(n.Purpose) + "\n")
	}
	b.WriteString("  return (\n")
	b.WriteString("    <div className=\"" + slug(n.Name) + "\" />\n")
	b.WriteString("  );\n")
	b.WriteString("}\n")
	return b.String()
}

// renderNextRoute renders a Next.js route handler for an API endpoint.
func renderNextRoute(n *ir.CreateEndpointNode) string {
	var b strings.Builder
	method := strings.ToUpper(n.Method)
	if method == "" {
		method = "GET"
	}
	b.WriteString("import { NextRequest, NextResponse } from \"next/server\";\n\n")
	b.WriteString("export async function " + method + "(request: NextRequest) {\n")
	if n.Description != "" {
		b.WriteString("  // " + esc(n.Description) + "\n")
	}
	b.WriteString("  return NextResponse.json({ ok: true });\n")
	b.WriteString("}\n")
	return b.String()
}

// filepathFromRoute converts a logical URL route into a relative path
// segment list (e.g. "/api/users" → "api/users"), stripping a leading slash.
func filepathFromRoute(route string) string {
	route = strings.Trim(route, "/")
	if route == "" {
		return "index"
	}
	return route
}

// routeSegments converts a logical URL route into path segments suitable for
// a framework API directory, stripping a leading "api" segment so the
// adapter's own api/ prefix is not doubled (e.g. "/api/users" → "users").
func routeSegments(route string) string {
	segments := filepathFromRoute(route)
	if segments == "api" {
		return "index"
	}
	return strings.TrimPrefix(segments, "api/")
}

// renderMigration renders a generic SQL migration.
func renderMigration(name, description string) string {
	var b strings.Builder
	b.WriteString("-- migration: " + name + "\n")
	if description != "" {
		b.WriteString("-- " + description + "\n")
	}
	b.WriteString("--\n")
	b.WriteString("BEGIN;\n")
	b.WriteString("CREATE TABLE IF NOT EXISTS " + snake(name) + " (\n")
	b.WriteString("  id SERIAL PRIMARY KEY\n")
	b.WriteString(");\n")
	b.WriteString("COMMIT;\n")
	return b.String()
}

// esc escapes a value for safe interpolation into generated source.
func esc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

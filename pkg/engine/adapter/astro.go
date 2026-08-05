package adapter

import (
	"fmt"
	"strings"

	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
)

// AstroAdapter is the pure render adapter for the Astro framework. It maps
// Logical IR nodes onto the Astro file layout (src/pages/<slug>.astro,
// src/components/<name>.astro, src/pages/api/<route>.ts, db/<name>.sql).
// It contains no planning logic and never mutates the node it renders.
type AstroAdapter struct{}

// NewAstroAdapter returns an Astro render adapter.
func NewAstroAdapter() *AstroAdapter { return &AstroAdapter{} }

// Framework implements FrameworkAdapter.
func (a *AstroAdapter) Framework() Framework { return FrameworkAstro }

// RenderNode implements FrameworkAdapter.
func (a *AstroAdapter) RenderNode(node ir.IRNode) ([]FileArtifact, error) {
	switch n := node.(type) {
	case *ir.CreatePageNode:
		return []FileArtifact{{
			Path:    joinPath("src", "pages", slug(n.Name)+".astro"),
			Content: renderAstroPage(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateSectionNode:
		return []FileArtifact{{
			Path:    joinPath("src", "components", slug(n.Name)+".astro"),
			Content: renderAstroSection(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateComponentNode:
		return []FileArtifact{{
			Path:    joinPath("src", "components", slug(n.Name)+".astro"),
			Content: renderAstroComponent(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateEndpointNode:
		return []FileArtifact{{
			Path:    joinPath("src", "pages", "api", routeSegments(n.Route)+".ts"),
			Content: renderAstroEndpoint(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateDatabaseMigrationNode:
		return []FileArtifact{{
			Path:    joinPath("db", slug(n.Name)+".sql"),
			Content: renderMigration(n.Name, n.Description),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateStyleNode:
		return []FileArtifact{{
			Path:    joinPath("src", "styles", slug(n.Name)+".css"),
			Content: renderAstroStyle(n.Name, n.Description),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateScriptNode:
		return []FileArtifact{{
			Path:    joinPath("src", "scripts", slug(n.Name)+".ts"),
			Content: renderAstroScript(n.Name, n.Behavior),
			Mode:    defaultFileMode,
		}}, nil
	default:
		return nil, fmt.Errorf("adapter astro: unsupported node kind %q", node.Kind())
	}
}

// renderAstroStyle renders a global stylesheet under src/styles.
func renderAstroStyle(name, description string) string {
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

// renderAstroScript renders a frontmatter-loaded client behaviour module.
func renderAstroScript(name, behavior string) string {
	var b strings.Builder
	b.WriteString("// " + slug(name) + "\n")
	if behavior != "" {
		b.WriteString("// " + esc(behavior) + "\n")
	}
	b.WriteString("document.addEventListener('DOMContentLoaded', () => {\n")
	b.WriteString("  console.log('ready');\n")
	b.WriteString("});\n")
	return b.String()
}

// renderAstroPage renders an Astro page with frontmatter.
func renderAstroPage(n *ir.CreatePageNode) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("// " + esc(n.NodeDesc()) + "\n")
	b.WriteString("---\n")
	b.WriteString("<html lang=\"en\">\n")
	b.WriteString("  <head>\n")
	b.WriteString("    <meta charset=\"utf-8\" />\n")
	b.WriteString("    <title>" + esc(n.PageTitle()) + "</title>\n")
	b.WriteString("  </head>\n")
	b.WriteString("  <body>\n")
	b.WriteString("    <h1>" + esc(n.PageTitle()) + "</h1>\n")
	for _, s := range n.SectionNames() {
		b.WriteString("    <section>" + esc(s) + "</section>\n")
	}
	b.WriteString("  </body>\n")
	b.WriteString("</html>\n")
	return b.String()
}

// renderAstroSection renders an Astro section component.
func renderAstroSection(n *ir.CreateSectionNode) string {
	var b strings.Builder
	b.WriteString("---\n")
	if n.Content != "" {
		b.WriteString("const content = `" + esc(n.Content) + "`;\n")
	}
	b.WriteString("---\n")
	b.WriteString("<section class=\"" + slug(n.Name) + "\">\n")
	if n.Content != "" {
		b.WriteString("  <p>{content}</p>\n")
	}
	b.WriteString("</section>\n")
	return b.String()
}

// renderAstroComponent renders an Astro component.
func renderAstroComponent(n *ir.CreateComponentNode) string {
	var b strings.Builder
	b.WriteString("---\n")
	if n.Purpose != "" {
		b.WriteString("// " + esc(n.Purpose) + "\n")
	}
	b.WriteString("---\n")
	b.WriteString("<div class=\"" + slug(n.Name) + "\"></div>\n")
	return b.String()
}

// renderAstroEndpoint renders an Astro API endpoint handler.
func renderAstroEndpoint(n *ir.CreateEndpointNode) string {
	var b strings.Builder
	method := strings.ToUpper(n.Method)
	if method == "" {
		method = "GET"
	}
	b.WriteString("import type { APIRoute } from \"astro\";\n\n")
	b.WriteString("export const " + method + ": APIRoute = ({ request }) => {\n")
	if n.Description != "" {
		b.WriteString("  // " + esc(n.Description) + "\n")
	}
	b.WriteString("  return new Response(JSON.stringify({ ok: true }), {\n")
	b.WriteString("    headers: { \"content-type\": \"application/json\" },\n")
	b.WriteString("  });\n")
	b.WriteString("};\n")
	return b.String()
}

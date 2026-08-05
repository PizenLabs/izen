package adapter

import (
	"fmt"
	"strings"

	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
)

// StaticWebAdapter is the pure render adapter for a static HTML/CSS/JS site
// with no build step. It maps Logical IR nodes onto the flat site layout
// (<slug>.html, <slug>.css, <slug>.js, sections/<slug>.html).
// It contains no planning logic and never mutates the node it renders.
type StaticWebAdapter struct{}

// NewStaticWebAdapter returns a static HTML/CSS/JS render adapter.
func NewStaticWebAdapter() *StaticWebAdapter { return &StaticWebAdapter{} }

// Framework implements FrameworkAdapter.
func (a *StaticWebAdapter) Framework() Framework { return FrameworkStaticWeb }

// RenderNode implements FrameworkAdapter.
func (a *StaticWebAdapter) RenderNode(node ir.IRNode) ([]FileArtifact, error) {
	switch n := node.(type) {
	case *ir.CreatePageNode:
		return []FileArtifact{{
			Path:    slug(n.Name) + ".html",
			Content: renderStaticPage(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateSectionNode:
		return []FileArtifact{{
			Path:    joinPath("sections", slug(n.Name)+".html"),
			Content: renderStaticSection(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateComponentNode:
		return []FileArtifact{{
			Path:    joinPath("partials", slug(n.Name)+".html"),
			Content: renderStaticPartial(n.Name, n.Purpose),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateStyleNode:
		return []FileArtifact{{
			Path:    slug(n.Name) + ".css",
			Content: renderStaticStyle(n.Name, n.Description),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateScriptNode:
		return []FileArtifact{{
			Path:    slug(n.Name) + ".js",
			Content: renderStaticScript(n.Name, n.Behavior),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateDatabaseMigrationNode:
		return []FileArtifact{{
			Path:    joinPath("db", slug(n.Name)+".sql"),
			Content: renderMigration(n.Name, n.Description),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateEndpointNode:
		// A static site has no server runtime; a dynamic endpoint cannot be
		// rendered. The IR planner never emits endpoints for the static
		// framework.
		return nil, fmt.Errorf("adapter static-web: cannot render an API endpoint — static sites have no server runtime")
	default:
		return nil, fmt.Errorf("adapter static-web: unsupported node kind %q", node.Kind())
	}
}

// renderStaticPage renders a complete HTML page. Sections composed into the
// page are rendered inline as <section> blocks; the page links the stylesheet
// and script assets so the rendered site is directly openable.
func renderStaticPage(n *ir.CreatePageNode) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n")
	b.WriteString("<html lang=\"en\">\n")
	b.WriteString("<head>\n")
	b.WriteString("  <meta charset=\"utf-8\" />\n")
	b.WriteString("  <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\" />\n")
	b.WriteString("  <title>" + esc(n.PageTitle()) + "</title>\n")
	b.WriteString("  <link rel=\"stylesheet\" href=\"styles.css\" />\n")
	b.WriteString("</head>\n")
	b.WriteString("<body>\n")
	b.WriteString("  <main id=\"" + slug(n.Name) + "\">\n")
	b.WriteString("    <h1>" + esc(n.PageTitle()) + "</h1>\n")
	for _, s := range n.SectionNames() {
		b.WriteString("    <section>" + esc(s) + "</section>\n")
	}
	b.WriteString("  </main>\n")
	b.WriteString("  <script src=\"script.js\"></script>\n")
	b.WriteString("</body>\n")
	b.WriteString("</html>\n")
	return b.String()
}

// renderStaticSection renders an HTML section fragment.
func renderStaticSection(n *ir.CreateSectionNode) string {
	var b strings.Builder
	b.WriteString("<section id=\"" + slug(n.Name) + "\">\n")
	if n.Content != "" {
		b.WriteString("  <p>" + esc(n.Content) + "</p>\n")
	}
	b.WriteString("</section>\n")
	return b.String()
}

// renderStaticPartial renders an HTML partial fragment.
func renderStaticPartial(name, purpose string) string {
	var b strings.Builder
	b.WriteString("<!-- partial: " + slug(name) + " -->\n")
	b.WriteString("<div class=\"" + slug(name) + "\">\n")
	if purpose != "" {
		b.WriteString("  <!-- " + esc(purpose) + " -->\n")
	}
	b.WriteString("</div>\n")
	return b.String()
}

// renderStaticStyle renders a CSS stylesheet.
func renderStaticStyle(name, description string) string {
	var b strings.Builder
	b.WriteString("/* " + slug(name) + " */\n")
	if description != "" {
		b.WriteString("/* " + esc(description) + " */\n")
	}
	b.WriteString(":root {\n")
	b.WriteString("  --font-sans: system-ui, -apple-system, sans-serif;\n")
	b.WriteString("  --color-text: #1a1a1a;\n")
	b.WriteString("  --color-bg: #ffffff;\n")
	b.WriteString("}\n")
	b.WriteString("\n")
	b.WriteString("body {\n")
	b.WriteString("  margin: 0;\n")
	b.WriteString("  font-family: var(--font-sans);\n")
	b.WriteString("  color: var(--color-text);\n")
	b.WriteString("  background: var(--color-bg);\n")
	b.WriteString("}\n")
	return b.String()
}

// renderStaticScript renders a JavaScript file.
func renderStaticScript(name, behavior string) string {
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

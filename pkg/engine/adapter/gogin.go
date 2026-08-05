package adapter

import (
	"fmt"
	"strings"

	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
)

// GoGinAdapter is the pure render adapter for a Go HTTP service on the Gin
// router. It maps Logical IR nodes onto the Go layout
// (internal/handlers/<name>.go, templates/<name>.html,
// templates/partials/<name>.html, migrations/<name>.sql).
// It contains no planning logic and never mutates the node it renders.
type GoGinAdapter struct{}

// NewGoGinAdapter returns a Go + Gin render adapter.
func NewGoGinAdapter() *GoGinAdapter { return &GoGinAdapter{} }

// Framework implements FrameworkAdapter.
func (a *GoGinAdapter) Framework() Framework { return FrameworkGoGin }

// RenderNode implements FrameworkAdapter.
func (a *GoGinAdapter) RenderNode(node ir.IRNode) ([]FileArtifact, error) {
	switch n := node.(type) {
	case *ir.CreatePageNode:
		return []FileArtifact{{
			Path:    joinPath("templates", slug(n.Name)+".html"),
			Content: renderGinPage(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateSectionNode:
		return []FileArtifact{{
			Path:    joinPath("templates", "partials", slug(n.Name)+".html"),
			Content: renderGinPartial(n.Name, n.Content),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateComponentNode:
		return []FileArtifact{{
			Path:    joinPath("templates", "partials", slug(n.Name)+".html"),
			Content: renderGinPartial(n.Name, n.Purpose),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateEndpointNode:
		return []FileArtifact{{
			Path:    joinPath("internal", "handlers", snake(n.Name)+".go"),
			Content: renderGinHandler(n),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateDatabaseMigrationNode:
		return []FileArtifact{{
			Path:    joinPath("migrations", snake(n.Name)+".sql"),
			Content: renderMigration(n.Name, n.Description),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateStyleNode:
		return []FileArtifact{{
			Path:    joinPath("static", slug(n.Name)+".css"),
			Content: renderGinStyle(n.Name, n.Description),
			Mode:    defaultFileMode,
		}}, nil
	case *ir.CreateScriptNode:
		return []FileArtifact{{
			Path:    joinPath("static", slug(n.Name)+".js"),
			Content: renderGinScript(n.Name, n.Behavior),
			Mode:    defaultFileMode,
		}}, nil
	default:
		return nil, fmt.Errorf("adapter go-gin: unsupported node kind %q", node.Kind())
	}
}

// renderGinStyle renders a static CSS asset served by the Gin router.
func renderGinStyle(name, description string) string {
	var b strings.Builder
	b.WriteString("/* " + slug(name) + " */\n")
	if description != "" {
		b.WriteString("/* " + esc(description) + " */\n")
	}
	b.WriteString("body {\n")
	b.WriteString("  margin: 0;\n")
	b.WriteString("  font-family: system-ui, -apple-system, sans-serif;\n")
	b.WriteString("}\n")
	return b.String()
}

// renderGinScript renders a static JS asset served by the Gin router.
func renderGinScript(name, behavior string) string {
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

// renderGinPage renders an HTML page template.
func renderGinPage(n *ir.CreatePageNode) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n")
	b.WriteString("<html lang=\"en\">\n")
	b.WriteString("<head>\n")
	b.WriteString("  <meta charset=\"utf-8\" />\n")
	b.WriteString("  <title>" + esc(n.PageTitle()) + "</title>\n")
	b.WriteString("</head>\n")
	b.WriteString("<body>\n")
	b.WriteString("  <h1>" + esc(n.PageTitle()) + "</h1>\n")
	for _, s := range n.SectionNames() {
		b.WriteString("  <section>" + esc(s) + "</section>\n")
	}
	b.WriteString("</body>\n")
	b.WriteString("</html>\n")
	return b.String()
}

// renderGinPartial renders an HTML partial template.
func renderGinPartial(name, content string) string {
	var b strings.Builder
	b.WriteString("{{ define \"" + slug(name) + "\" }}\n")
	b.WriteString("<section class=\"" + slug(name) + "\">\n")
	if content != "" {
		b.WriteString("  <p>" + esc(content) + "</p>\n")
	}
	b.WriteString("</section>\n")
	b.WriteString("{{ end }}\n")
	return b.String()
}

// renderGinHandler renders a Gin handler function. The route registration
// comment records the logical route so a later wiring stage can mount it.
func renderGinHandler(n *ir.CreateEndpointNode) string {
	var b strings.Builder
	method := strings.ToUpper(n.Method)
	if method == "" {
		method = "GET"
	}
	handler := pascal(n.Name)
	b.WriteString("package handlers\n\n")
	b.WriteString("import (\n")
	b.WriteString("\t\"net/http\"\n\n")
	b.WriteString("\t\"github.com/gin-gonic/gin\"\n")
	b.WriteString(")\n\n")
	if n.Description != "" {
		b.WriteString("// " + handler + " " + esc(n.Description) + "\n")
	}
	b.WriteString("// Route: " + method + " " + n.Route + "\n")
	b.WriteString("func " + handler + "(c *gin.Context) {\n")
	b.WriteString("\tc.JSON(http.StatusOK, gin.H{\"ok\": true})\n")
	b.WriteString("}\n")
	return b.String()
}

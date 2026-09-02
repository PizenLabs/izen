package context

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// escapeXML escapes s for safe embedding in XML text and attribute values.
func escapeXML(s string) string {
	var b strings.Builder
	// xml.EscapeText cannot fail for valid UTF-8 input.
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// RenderXML formats the compiled context into a structured XML document. Every
// emitted context unit retains its provenance metadata (ID, kind, source, token
// cost, and relevance) as attributes, with the unit content encoded as the body.
func RenderXML(cc *CompiledContext) (string, error) {
	if cc == nil {
		return "", fmt.Errorf("context: cannot render nil compiled context")
	}

	intent := cc.Intent
	if intent == "" {
		intent = "unknown"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<compiled_context intent="%s" budget_tokens="%d" used_tokens="%d" depth="%s">`,
		escapeXML(intent), cc.Budget, cc.TotalTokens, cc.Depth.String())

	for _, unit := range cc.Units {
		fmt.Fprintf(&b, "\n  <context_unit id=\"%s\" source=\"%s\" kind=\"%s\" relevance=\"%.2f\" token_cost=\"%d\">",
			escapeXML(unit.ID), escapeXML(unit.Source), escapeXML(unit.Kind.String()),
			unit.Relevance, unit.TokenCost)
		if unit.Content != "" {
			fmt.Fprintf(&b, "\n    %s\n  ", escapeXML(unit.Content))
		}
		fmt.Fprintf(&b, "</context_unit>")
	}

	fmt.Fprintf(&b, "\n</compiled_context>")

	return b.String(), nil
}

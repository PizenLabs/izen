package pipeline

import (
	"strings"

	"github.com/PizenLabs/izen/pkg/engine/layer1"
)

// CapabilityGraph is the read-only capability surface a system-prompt header
// is rendered from. *layer1.Graph satisfies it.
type CapabilityGraph interface {
	// Stack returns the detected workspace stack.
	Stack() layer1.Stack
	// Supports reports whether the workspace exposes the capability.
	Supports(cap layer1.Capability) bool
	// Resolve returns the concrete command for the capability.
	Resolve(cap layer1.Capability) (string, bool)
}

// CapabilityHeader renders the strict workspace-capability header that must be
// prepended to every composed LLM system prompt. It is the anti-hallucination
// contract of the engine: the model is told exactly which toolchain commands
// exist for this workspace and — critically — which do not. A static HTML/JS
// project therefore never claims a Go build or a Go test command.
//
// The header is derived exclusively from the Layer 1 capability graph; no
// heuristic stack detection is consulted.
func CapabilityHeader(g CapabilityGraph) string {
	if g == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("### WORKSPACE CAPABILITIES (auto-detected — authoritative)\n")
	b.WriteString("STACK: ")
	b.WriteString(string(g.Stack()))
	b.WriteByte('\n')
	written := 0
	for _, c := range layer1.AllCapabilities() {
		if !g.Supports(c) {
			continue
		}
		if cmd, ok := g.Resolve(c); ok {
			b.WriteString(strings.ToUpper(string(c)))
			b.WriteString(": ")
			b.WriteString(cmd)
			b.WriteByte('\n')
			written++
		}
	}
	if written == 0 {
		b.WriteString("CAPABILITIES: none detected\n")
	}
	b.WriteString("Do not invent build, test, lint or format commands beyond those listed.")
	return b.String()
}

// CapabilityHeaderJSON renders the compact JSON form of the capability graph,
// suitable for embedding in a system prompt or an audit trace. It falls back
// to a stable empty document when the graph is unavailable.
func CapabilityHeaderJSON(g CapabilityGraph) []byte {
	if g == nil {
		return []byte(`{"stack":"unknown"}`)
	}
	if jg, ok := g.(interface{ ToCompactJSON() []byte }); ok {
		return jg.ToCompactJSON()
	}
	// Structural fallback for non-Graph implementations: render the same
	// compact shape so the audit format never varies.
	var b strings.Builder
	b.WriteString(`{"stack":"`)
	b.WriteString(string(g.Stack()))
	b.WriteString(`","caps":{`)
	first := true
	for _, c := range layer1.AllCapabilities() {
		if cmd, ok := g.Resolve(c); ok {
			if !first {
				b.WriteByte(',')
			}
			first = false
			b.WriteString(`"`)
			b.WriteString(string(c))
			b.WriteString(`":"`)
			b.WriteString(strings.ReplaceAll(cmd, `"`, `\"`))
			b.WriteString(`"`)
		}
	}
	b.WriteString("}}")
	return []byte(b.String())
}

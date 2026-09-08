package layer1

import "encoding/json"

// Stack identifies the dominant application stack of a workspace.
type Stack string

const (
	StackGo      Stack = "go"
	StackNode    Stack = "node"
	StackRust    Stack = "rust"
	StackPython  Stack = "python"
	StackDocker  Stack = "docker"
	StackStatic  Stack = "static"
	StackUnknown Stack = "unknown"
)

// WorkspaceCapability is the read-only capability surface of a workspace.
type WorkspaceCapability interface {
	// Supports reports whether the workspace exposes the capability.
	Supports(cap Capability) bool
	// Resolve returns the concrete command for the capability and whether it
	// is supported. An unsupported capability yields an empty command.
	Resolve(cap Capability) (string, bool)
	// ToCompactJSON renders minimal metadata suitable for an LLM system prompt.
	ToCompactJSON() []byte
}

// Graph is an immutable capability resolution table for one workspace. Once
// constructed by Detect it exposes no mutators, making it safe for concurrent
// read-only use.
type Graph struct {
	stack    Stack
	commands map[Capability]string
}

// Stack returns the detected stack.
func (g *Graph) Stack() Stack { return g.stack }

// Supports reports whether the workspace exposes the capability.
func (g *Graph) Supports(cap Capability) bool {
	_, ok := g.commands[cap]
	return ok
}

// Resolve returns the concrete command for the capability.
func (g *Graph) Resolve(cap Capability) (string, bool) {
	cmd, ok := g.commands[cap]
	return cmd, ok
}

// ToCompactJSON renders the graph as minimal metadata for an LLM system
// prompt. Capability keys are emitted in lexical order.
func (g *Graph) ToCompactJSON() []byte {
	doc := struct {
		Stack string            `json:"stack"`
		Caps  map[string]string `json:"caps,omitempty"`
	}{
		Stack: string(g.stack),
	}
	if len(g.commands) > 0 {
		caps := make(map[string]string, len(g.commands))
		for c, cmd := range g.commands {
			caps[string(c)] = cmd
		}
		doc.Caps = caps
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return []byte(`{"stack":"unknown"}`)
	}
	return data
}

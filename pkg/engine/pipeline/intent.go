// Package pipeline is the central orchestration seam of the Izen engine. It
// wires the six foundation layers into one coherent Pipeline Engine:
//
//	Layer 0  pkg/engine/layer0   - knowledge resolution (absolute constraints)
//	Layer 1  pkg/engine/layer1   - workspace capability graph (stack truth)
//	Layer 2  pkg/engine/layer2   - policy-governed ExecutionContext (SoR)
//	Layer 3  pkg/engine/layer3   - PolicyGuard + stateless worker execution
//	Layer 4  pkg/engine/layer4   - validation DAG engine (RAM structural first)
//	Layer 5  pkg/engine/telemetry- non-blocking lifecycle event bus
//
// The engine performs no legacy heuristic context gathering and no heuristic
// stack detection: the workspace stack is whatever Layer 1 detects and the
// execution context is whatever Layer 2 assembles from the lea System of
// Record. The detected capability surface is injected into every composed LLM
// system prompt (via the shared prompt registry) so models never hallucinate a
// toolchain (e.g. assuming Go for a static HTML/JS project).
package pipeline

// Intent classifies a user request into a model-routing family. Intent is the
// policy axis of the engine: it selects the model tier and the Layer 2 context
// budget before any execution starts.
type Intent string

const (
	// IntentReasoning covers heavy-reasoning modes (/plan, /investigate,
	// /review). It routes to a high-capability model under a strict context
	// budget so analysis never over-consumes the window.
	IntentReasoning Intent = "reasoning"
	// IntentExecution covers execution modes (/build). It routes to a fast
	// coding model with a balanced context budget.
	IntentExecution Intent = "execution"
	// IntentInformational covers read-only modes (/ask). It routes to a
	// minimal-capability model under a minimal context policy; the pipeline
	// never proposes or applies mutations for this intent.
	IntentInformational Intent = "informational"
)

// allIntents preserves declaration order for AllIntents and Valid.
var allIntents = []Intent{IntentReasoning, IntentExecution, IntentInformational}

// Valid reports whether i is one of the defined routing intents.
func (i Intent) Valid() bool {
	for _, x := range allIntents {
		if i == x {
			return true
		}
	}
	return false
}

// String returns the machine-readable intent label.
func (i Intent) String() string { return string(i) }

// AllIntents returns every defined intent in declaration order.
func AllIntents() []Intent {
	return append([]Intent(nil), allIntents...)
}

// IntentForMode maps a mode name onto its routing intent. Modes that demand
// heavy structural reasoning (/plan, /investigate, /review) route to
// IntentReasoning; /build routes to IntentExecution; read-only /ask routes to
// IntentInformational. Unknown modes fall back to IntentExecution so a request
// still carries a sane, executable budget.
func IntentForMode(mode string) Intent {
	switch mode {
	case "plan", "investigate", "review":
		return IntentReasoning
	case "build":
		return IntentExecution
	case "ask":
		return IntentInformational
	default:
		return IntentExecution
	}
}

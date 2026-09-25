package registry

// Provider wire-policy vocabulary for the adaptive runtime metadata surface.
//
// A model's wire policy describes what the provider requires on the HTTP wire
// (independent of the model's semantic capabilities). Izen's adaptive runtime
// resolves the interaction contract at invocation time; the Model Registry TUI
// renders the resolved path so the user can see how a model will be invoked
// without disabling or hiding any discovered model.
const (
	// AgenticBadge is the capability badge rendered for models whose provider
	// wire policy restricts them to an agentic harness. It is informational
	// only: the adaptive runtime auto-promotes the execution path.
	AgenticBadge = "[Agentic]"

	// RuntimePathAutoPromote is the adaptive execution path for models that
	// require an agentic wire harness. For /ask (DirectCompletion) the runtime
	// binds read-only tools so the provider accepts the invocation.
	RuntimePathAutoPromote = "Auto-Promote (Binds ReadOnly Tools for /ask)"

	// RuntimePathStandard is the default adaptive execution path: the model is
	// invoked directly through its resolved interaction contract.
	RuntimePathStandard = "DirectCompletion / AgenticLoop (Standard)"

	// WirePolicyAgentic is the human label for a model that requires an
	// agentic harness.
	WirePolicyAgentic = "Agentic harness required"
	// WirePolicyStandard is the human label for a standard wire policy.
	WirePolicyStandard = "Standard"
)

// agenticHarnessModels is the POSITIVE provider wire-policy registry: models
// whose provider requires an agentic tool schema on the wire. This is not a
// blacklist. The models stay discovered, selectable and executable; the
// adaptive runtime auto-promotes their interaction contract and attaches
// authentic read-only tools instead of rejecting them.
var agenticHarnessModels = []struct {
	Provider string
	ID       string
}{
	{Provider: "openrouter", ID: "thinkingmachines/inkling:free"},
	{Provider: "openrouter", ID: "thinkingmachines/inkling-small:free"},
}

// isAgenticHarnessModel is the exact-match wire-policy lookup (trim +
// lowercase). It is never substring/family matching, so a policy entry can
// never affect a sibling model.
func isAgenticHarnessModel(provider, modelID string) bool {
	p := normalizeModelKey(provider)
	id := normalizeModelKey(modelID)
	if p == "" || id == "" {
		return false
	}
	for _, e := range agenticHarnessModels {
		if normalizeModelKey(e.Provider) == p && normalizeModelKey(e.ID) == id {
			return true
		}
	}
	return false
}

// RequiresAgenticHarness reports whether the model's provider wire policy
// restricts it to an agentic harness. Such models are still discovered and
// selectable; the adaptive runtime auto-promotes their execution path rather
// than hiding or rejecting them.
func RequiresAgenticHarness(m ModelDescriptor) bool {
	return isAgenticHarnessModel(m.Provider, m.ID)
}

// RequiresAgenticHarnessWire is the (provider, modelID) form used by provider
// adapters before a ModelDescriptor exists.
func RequiresAgenticHarnessWire(provider, modelID string) bool {
	return isAgenticHarnessModel(provider, modelID)
}

// WirePolicyLabel returns the human-readable provider wire policy for a model.
func WirePolicyLabel(m ModelDescriptor) string {
	if RequiresAgenticHarness(m) {
		return WirePolicyAgentic
	}
	return WirePolicyStandard
}

// RuntimePathFor returns the adaptive execution-path label for a model. Models
// that require an agentic harness are auto-promoted; every other model follows
// the standard DirectCompletion / AgenticLoop path.
func RuntimePathFor(m ModelDescriptor) string {
	if RequiresAgenticHarness(m) {
		return RuntimePathAutoPromote
	}
	return RuntimePathStandard
}

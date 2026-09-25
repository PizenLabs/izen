package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrOpenRouterAgenticGate is OpenRouter's routing step "Gate Free Endpoints
// by Agentic Harness" rejecting a request.
//
// VERIFIED AGAINST THE LIVE API (2026-09): the gate is a User-Agent allowlist on
// the PROVIDER side, not a wire-contract requirement. For
// thinkingmachines/inkling-small:free:
//
//	tools present, UA "izen/0.2.0"                    -> 403 Gate Free Endpoints…
//	no tools,        UA "izen/0.2.0"                 -> 403 Gate Free Endpoints…
//	tools present,   UA "claude-cli/2.0.0"            -> 200
//	tools present,   UA "codex_cli_rs/0.1.0"          -> 200
//	tools present,   UA "opencode/0.2.0"              -> 200
//	tools present,   UA "cursor/0.2.0" / "cline"      -> 200
//	tools present,   UA "cli", "agent", "izen-cli"    -> 403
//
// The paid catalog id thinkingmachines/inkling-small answers 200 for an
// ordinary client. So Dynamic Contract Promotion (promoted contract + bound
// read-only tools) is necessary but NOT sufficient to pass this gate: the model
// is reachable only from a User-Agent OpenRouter has registered as an agentic
// harness. Izen never impersonates another product's User-Agent, so the gate is
// surfaced as an actionable, reversion-free diagnostic instead.
var ErrOpenRouterAgenticGate = errors.New("openrouter: model is gated to registered agentic harnesses")

// agenticHarnessGateStep is the routing step OpenRouter reports in the error
// metadata when the harness gate rejects a request.
const agenticHarnessGateStep = "gate free endpoints by agentic harness"

// openRouterErrorMetadata is the subset of the provider error envelope that
// carries routing diagnostics. OpenRouter reports why a request never reached
// an endpoint here (e.g. the agentic-harness gate), which is strictly more
// actionable than the prose message alone.
type openRouterErrorMetadata struct {
	Error struct {
		Metadata struct {
			FailedRoutingStep string `json:"failed_routing_step"`
		} `json:"metadata"`
	} `json:"error"`
}

// isAgenticHarnessGate reports whether a provider error is the agentic-harness
// ROUTING GATE.
//
// Detection is metadata-only, on OpenRouter's machine-readable
// error.metadata.failed_routing_step. A prose match on "only available on
// agentic harnesses" is deliberately NOT used: that same sentence is the
// provider's generic compatibility wording, so a text match would swallow the
// distinct compatibility class (httpx.ProviderError.IsModelCompatibility) into
// this one. The routing step is the only signal that proves the request was
// stopped by the gate rather than refused by the model.
func isAgenticHarnessGate(statusCode int, body []byte) bool {
	if statusCode != 403 {
		return false
	}
	var envelope openRouterErrorMetadata
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(envelope.Error.Metadata.FailedRoutingStep), agenticHarnessGateStep)
}

// asAgenticGateError classifies the agentic-harness routing gate with an
// actionable message. It returns nil for every other error, so callers keep the
// existing classification behavior.
func asAgenticGateError(model string, statusCode int, body []byte) error {
	if !isAgenticHarnessGate(statusCode, body) {
		return nil
	}
	pe := NewProviderError("openrouter", statusCode, body)
	return fmt.Errorf("%w: model %q is served only to agentic harnesses OpenRouter has registered (%s)",
		ErrOpenRouterAgenticGate, model, pe.RawMessage)
}

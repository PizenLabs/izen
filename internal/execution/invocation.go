package execution

import (
	"errors"
	"fmt"

	domain "github.com/PizenLabs/izen/internal/core/domain"
)

// ErrUnassignedTargetModel is returned when an invocation request carries no
// explicit model binding for its target node. The worker MUST reject locally
// before any provider call.
var ErrUnassignedTargetModel = errors.New("execution: no model assigned to target node")

// InvocationRequest is the explicit invocation contract enforced at admission.
// Every background worker invocation MUST carry a TargetModel resolved
// dynamically from the active Workspace Target configuration at the exact
// moment of prompt admission. Workers MUST NOT maintain independent model
// configuration state or resolve model IDs via fallback constants.
type InvocationRequest struct {
	Prompt    string
	Target    domain.WorkspaceTarget
	ModelID   string
	Provider  string
	Reasoning domain.ReasoningOption
}

// Validate enforces that ModelID is explicitly bound. An empty ModelID is a
// local preflight rejection — no HTTP request is sent.
func (r InvocationRequest) Validate() error {
	if r.ModelID == "" {
		return fmt.Errorf("%w [%s]", ErrUnassignedTargetModel, string(r.Target))
	}
	return nil
}

// ToExecuteRequest converts an InvocationRequest into an ExecuteRequest with
// the explicit ModelID binding preserved verbatim. The Model field travels to
// the provider without fallback substitution.
func (r InvocationRequest) ToExecuteRequest() ExecuteRequest {
	return ExecuteRequest{
		Prompt: r.Prompt,
		Mode:   string(r.Target),
		Model:  r.ModelID,
	}
}

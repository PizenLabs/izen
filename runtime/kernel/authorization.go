package kernel

import (
	"fmt"
	"sort"
	"strings"
)

// Grant is the Control Plane's explicit decision about what an execution may
// do. It is a value, not a session, not a capability flag on an object, and not
// something a provider can influence.
//
// Three properties make it usable as an authority:
//
//   - It is total about its target set. A grant either names the admissible
//     destinations or it names none, in which case nothing outside a
//     contract-declared target may be touched.
//   - It cannot be widened after the fact. Narrowing a Grant produces a new
//     Grant; there is no method that adds authority.
//   - It is inspectable. Every authorization decision the kernel refuses is
//     reported with the grant that produced the refusal.
type Grant struct {
	// ID names the grant for audit. An unnamed grant cannot be attributed.
	ID string
	// Capabilities are the capability identifiers this grant permits.
	Capabilities []CapabilityID
	// Targets are the workspace-relative destinations this grant covers. An
	// empty list means the grant covers only targets the Spec's Contract
	// already names; it never means "everything".
	Targets []string
}

// NewGrant builds a grant. It fails closed on an unnamed grant or an empty
// capability set, because a grant that permits nothing is a confusing way to
// express a mistake — NewGrant refuses rather than returning one.
func NewGrant(id string, capabilities []CapabilityID, targets []string) (Grant, error) {
	if strings.TrimSpace(id) == "" {
		return Grant{}, blockf(FailureAuthorization, "", "", "grant has no id")
	}
	if len(capabilities) == 0 {
		return Grant{}, blockf(FailureAuthorization, "", "", "grant %q permits no capabilities", id)
	}
	seen := make(map[CapabilityID]bool, len(capabilities))
	for _, c := range capabilities {
		if !c.Valid() {
			return Grant{}, blockf(FailureAuthorization, "", c, "grant %q names capability %q outside the closed vocabulary", id, c)
		}
		if seen[c] {
			return Grant{}, blockf(FailureAuthorization, "", c, "grant %q names capability %q twice", id, c)
		}
		seen[c] = true
	}
	for _, t := range targets {
		if strings.TrimPrefix(t, "./") == "" {
			return Grant{}, blockf(FailureAuthorization, "", "", "grant %q names an empty target", id)
		}
	}
	return Grant{
		ID:           id,
		Capabilities: append([]CapabilityID(nil), capabilities...),
		Targets:      append([]string(nil), targets...),
	}, nil
}

// Permits reports whether the grant covers the given capability. A capability
// outside the closed vocabulary is never permitted, even if it appears in the
// grant's list: the vocabulary is the outer boundary.
func (g Grant) Permits(c CapabilityID) bool {
	if !c.Valid() {
		return false
	}
	for _, allowed := range g.Capabilities {
		if allowed == c {
			return true
		}
	}
	return false
}

// Covers reports whether the grant covers the given concrete destination.
//
// A step that names NO target is not asking for a destination, so there is
// nothing for a target set to cover. Such a step is gated by the capability list
// alone — and that is sound rather than permissive, because a capability that
// mutates must name a destination: file.write refuses an empty target in its own
// gate, and spec.Validate refuses a mutating step whose target the contract does
// not name. A grant therefore cannot reach a mutating capability with no
// destination.
//
// An empty grant target list is interpreted against the contract, so callers must
// pass the contract's targets. This is why the function takes them explicitly
// rather than reading them from the grant: the contract, not the grant, is what
// names the admissible destinations.
func (g Grant) Covers(target string, contractTargets []string) bool {
	normalised := strings.TrimPrefix(target, "./")
	if normalised == "" {
		return true
	}
	if len(g.Targets) == 0 {
		return contractNames(contractTargets, normalised)
	}
	return contractNames(g.Targets, normalised)
}

// Narrow returns a copy of the grant limited to the given capabilities and
// targets. Narrowing is the only way a Grant changes, which is what makes
// "authority never expanded" a property of the type rather than a convention.
func (g Grant) Narrow(capabilities []CapabilityID, targets []string) Grant {
	return Grant{
		ID:           g.ID,
		Capabilities: append([]CapabilityID(nil), capabilities...),
		Targets:      append([]string(nil), targets...),
	}
}

// Authorizer decides whether a step may run. It is the kernel's single
// authorization gate, and it is an interface rather than an inline check
// because the Control Plane may want a policy engine, a human prompt, or a
// static allow-list — but the kernel must be able to run with none of those
// present, in which case the kernel's own DefaultAuthorizer applies.
//
// Authorize is only ever allowed to say yes or no about a specific Request. It
// has no channel through which to mutate the workspace, and it never returns a
// widened Grant.
type Authorizer interface {
	// Authorize reports whether req may proceed under grant.
	Authorize(req Request, grant Grant) error
}

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(req Request, grant Grant) error

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(req Request, grant Grant) error {
	if f == nil {
		return blockf(FailureAuthorization, req.Step, req.Capability, "no authorizer is bound")
	}
	return f(req, grant)
}

// Compile-time proof the adapter satisfies the boundary.
var _ Authorizer = AuthorizerFunc(nil)

// Authorize applies grant to req. It returns a Block naming exactly which limb
// of the authority was missing, so a refusal is diagnosable without reading the
// grant.
func (g Grant) Authorize(req Request, contractTargets []string) error {
	if strings.TrimSpace(g.ID) == "" {
		return blockf(FailureAuthorization, req.Step, req.Capability, "no grant is bound to this execution")
	}
	if !g.Permits(req.Capability) {
		return blockf(FailureAuthorization, req.Step, req.Capability,
			"grant %q does not permit capability %q", g.ID, req.Capability)
	}
	// A mutating capability must name a destination even when the grant's target
	// set would otherwise cover everything. This is checked here rather than only
	// in the capability so that the rule holds for every mutating capability,
	// including ones written later.
	if req.Capability.Mutating() && strings.TrimPrefix(req.Target, "./") == "" {
		return blockf(FailureAuthorization, req.Step, req.Capability,
			"mutating capability %q requires an explicit target", req.Capability)
	}
	if !g.Covers(req.Target, contractTargets) {
		return blockf(FailureAuthorization, req.Step, req.Capability,
			"grant %q does not cover target %q", g.ID, req.Target)
	}
	return nil
}

// DefaultAuthorizer is the kernel's built-in gate: it applies the grant's
// capability set and target set, nothing more. It is deliberately the weakest
// policy that is still an authority, so a deployment that does not configure a
// policy gets explicit, narrow, checkable behavior rather than an implicit
// permissive one.
type DefaultAuthorizer struct{}

// Authorize implements Authorizer.
func (DefaultAuthorizer) Authorize(req Request, grant Grant) error {
	return grant.Authorize(req, grant.Targets)
}

// Compile-time proof the built-in gate satisfies the boundary.
var _ Authorizer = DefaultAuthorizer{}

// String renders the grant's capability surface in canonical order.
func (g Grant) String() string {
	caps := append([]CapabilityID(nil), g.Capabilities...)
	sort.Slice(caps, func(i, j int) bool { return caps[i] < caps[j] })
	return fmt.Sprintf("grant=%s capabilities=[%s] targets=[%s]",
		g.ID, joinIDs(caps), strings.Join(g.Targets, " "))
}

package kernel

import (
	"fmt"
	"strings"
)

// ContractKind is the closed vocabulary of completion obligations. Each kind
// carries a DIFFERENT evidence obligation, which is why they cannot be
// collapsed: "at least one mutation" is not a universal completion rule, and a
// CREATE contract is never silently relabelled as a PATCH contract.
type ContractKind string

const (
	// ContractCreate: produce a new target. Requires a produced artifact, a
	// durable mutation, and an observation that the target now exists.
	ContractCreate ContractKind = "CREATE"
	// ContractPatch: change an existing target. Requires a produced artifact, a
	// durable mutation, and an observed filesystem delta for the target.
	ContractPatch ContractKind = "PATCH"
	// ContractDelete: remove a target. Requires a durable mutation and an
	// observation that the target is now absent.
	ContractDelete ContractKind = "DELETE"
	// ContractObserve: report what the workspace actually contains. Requires an
	// observation event and a delivered response. Mutation is forbidden, so a
	// spec that also declares a mutating step is invalid rather than silently
	// downgraded.
	ContractObserve ContractKind = "OBSERVE"
	// ContractAnswer: respond from a delivered response without touching the
	// workspace at all. Mutation is forbidden.
	ContractAnswer ContractKind = "ANSWER"
)

// allContractKinds is the canonical ordered vocabulary.
var allContractKinds = []ContractKind{
	ContractObserve,
	ContractAnswer,
	ContractCreate,
	ContractPatch,
	ContractDelete,
}

// Valid reports whether k is a member of the closed vocabulary.
func (k ContractKind) Valid() bool {
	for _, known := range allContractKinds {
		if known == k {
			return true
		}
	}
	return false
}

// String returns the raw contract label.
func (k ContractKind) String() string { return string(k) }

// RequiresMutation reports whether the contract demands a durable filesystem
// change. OBSERVE and ANSWER never do.
func (k ContractKind) RequiresMutation() bool {
	switch k {
	case ContractCreate, ContractPatch, ContractDelete:
		return true
	default:
		return false
	}
}

// ForbidsMutation reports whether the contract makes any mutation a violation
// rather than merely unnecessary. This is stronger than "does not require": a
// mutation during an OBSERVE contract is a contract violation, not a harmless
// extra step.
func (k ContractKind) ForbidsMutation() bool {
	switch k {
	case ContractObserve, ContractAnswer:
		return true
	default:
		return false
	}
}

// Contract is the immutable obligation set a completion claim is judged against.
//
// It is derived once, from facts the caller already owns, and then never
// changes. Its immutability is what makes PROVEN a falsifiable claim: the
// obligations cannot be edited after the evidence starts arriving.
type Contract struct {
	// Kind is the canonical contract kind.
	Kind ContractKind
	// Targets are the workspace-relative targets the objective names. A target
	// list is part of the contract: evidence for a target outside this set never
	// substitutes for a target inside it.
	Targets []string
	// RequiresVerification demands a verification verdict. A verifier that is
	// provably not applicable is recorded as such and stays distinguishable
	// from a pass.
	RequiresVerification bool
	// RequiresObservation demands a real observation event — the evidence that
	// the runtime actually looked at the workspace rather than answering from
	// priors.
	RequiresObservation bool
}

// RequiresMutation reports whether the contract demands a durable mutation.
func (c Contract) RequiresMutation() bool { return c.Kind.RequiresMutation() }

// targetsAre reports whether every declared target has the given presence.
// An empty target set never satisfies a presence requirement: "all of nothing
// exists" is not evidence of anything.
func (c Contract) targetsAre(present map[string]bool) bool {
	if len(c.Targets) == 0 {
		return false
	}
	for _, t := range c.Targets {
		if !present[t] {
			return false
		}
	}
	return true
}

// Step is one unit of work in an ExecutionProgram: a capability, a concrete
// target, and the arguments that capability needs.
//
// A Step is a REQUEST, not an instruction. Nothing in a Step is authoritative:
// the kernel decides whether it may run, the capability decides whether it can
// act, and only the resulting evidence decides what became true.
type Step struct {
	// ID names the step within the program. It must be unique and non-empty: a
	// duplicated identifier makes an evidence record ambiguous, and evidence
	// must never be ambiguous.
	ID string
	// Capability is the capability this step invokes.
	Capability CapabilityID
	// Target is the concrete workspace-relative destination, or "" for a step
	// that acts on the workspace as a whole.
	//
	// There is deliberately no inference here. A Step does not say "the HTML
	// file"; it says a concrete path, or it says nothing and lets the step fail.
	// Heuristic targeting is what turns "build a website" into "overwrite
	// index.html" without anybody having decided to.
	Target string
	// Args are capability-specific arguments, carried verbatim.
	Args map[string]string
	// Note is an optional human-facing label. It carries no authority and is
	// never read by the kernel when deciding anything.
	Note string
}

// Program is an ordered sequence of steps. Order is significant and is the
// program's only control flow: the kernel runs steps in order and stops at the
// first block.
type Program []Step

// ids returns the step identifiers in program order.
func (p Program) ids() []string {
	out := make([]string, 0, len(p))
	for _, s := range p {
		out = append(out, s.ID)
	}
	return out
}

// mutatingCapabilities returns the mutating capabilities the program declares,
// deduplicated and in canonical order. The engine checks this against the
// contract before dispatch so a forbidden mutation is an invalid spec rather
// than a mid-flight violation.
func (p Program) mutatingCapabilities() []CapabilityID {
	seen := make(map[CapabilityID]bool, len(p))
	for _, s := range p {
		if s.Capability.Mutating() {
			seen[s.Capability] = true
		}
	}
	var out []CapabilityID
	for _, id := range allCapabilityIDs {
		if seen[id] {
			out = append(out, id)
		}
	}
	return out
}

// Spec is the complete, immutable description of one execution: what is being
// attempted, under which contract, with which budget, and in which order.
//
// The Spec is the Control Plane's authority made explicit. It is deliberately
// a value type with no callbacks and no references to live objects, so a spec
// can be logged, compared, hashed, and replayed without the runtime keeping the
// machinery that produced it alive.
type Spec struct {
	// ExecutionID uniquely identifies this execution. It must be non-empty:
	// an execution nobody can name cannot be audited.
	ExecutionID string
	// Objective is the human-authored statement of intent. The kernel never
	// infers obligations from it, and never reads it to decide anything. It is
	// carried so evidence and events can be reported against the reason the
	// execution exists.
	Objective string
	// Contract is the obligation set the completion claim is judged against.
	Contract Contract
	// Program is the ordered work.
	Program Program
	// Budget bounds the execution. A zero Budget means "no bound declared",
	// which is recorded explicitly rather than treated as unlimited success:
	// an execution that consumed no budget because nothing bounded it says so.
	Budget Budget
}

// Validate reports whether the spec is executable as written. It is total and
// deterministic: identical specs always validate identically, and validation
// happens before any capability is invoked so an unsatisfiable program never
// mutates anything.
func (s Spec) Validate(registry *Registry) error {
	if strings.TrimSpace(s.ExecutionID) == "" {
		return blockf(FailureInvalidSpec, "", "", "spec has no execution id")
	}
	if !s.Contract.Kind.Valid() {
		return blockf(FailureInvalidSpec, "", "", "spec has contract kind %q outside the closed vocabulary", s.Contract.Kind)
	}
	if len(s.Program) == 0 {
		return blockf(FailureInvalidSpec, "", "", "spec declares no steps")
	}

	seen := make(map[string]bool, len(s.Program))
	for i, step := range s.Program {
		if strings.TrimSpace(step.ID) == "" {
			return blockf(FailureInvalidSpec, "", step.Capability, "step %d has no id", i)
		}
		if seen[step.ID] {
			return blockf(FailureInvalidSpec, step.ID, step.Capability, "step id %q appears more than once", step.ID)
		}
		seen[step.ID] = true

		if !step.Capability.Valid() {
			return blockf(FailureInvalidSpec, step.ID, step.Capability, "step %q names capability %q outside the closed vocabulary", step.ID, step.Capability)
		}
		if !registry.Has(step.Capability) {
			return blockf(FailureCapabilityUnavailable, step.ID, step.Capability, "step %q requires capability %q, which is not registered", step.ID, step.Capability)
		}
		// A mutating step under a contract that forbids mutation is refused at
		// validation. Letting it through and discovering it afterwards would
		// mean the workspace had already changed by the time anyone noticed.
		if s.Contract.Kind.ForbidsMutation() && step.Capability.Mutating() {
			return blockf(FailureInvalidSpec, step.ID, step.Capability,
				"step %q invokes mutating capability %q under %s contract, which forbids mutation", step.ID, step.Capability, s.Contract.Kind)
		}
		// A declared target that lies outside the contract's target set is a
		// spec error, not a widening. The contract names the admissible
		// destinations; a step cannot name a different one.
		if len(s.Contract.Targets) > 0 && step.Target != "" && step.Capability.Mutating() {
			if !contractNames(s.Contract.Targets, step.Target) {
				return blockf(FailureInvalidSpec, step.ID, step.Capability,
					"step %q targets %q, which the contract does not name", step.ID, step.Target)
			}
		}
	}

	if s.Contract.RequiresMutation() {
		mutating := s.Program.mutatingCapabilities()
		if len(mutating) == 0 {
			return blockf(FailureInvalidSpec, "", "", "%s contract declares no mutating step, so its mutation obligation can never be satisfied", s.Contract.Kind)
		}
	}
	if s.Contract.RequiresObservation && !s.Program.observes() {
		return blockf(FailureInvalidSpec, "", "", "contract requires an observation but the program declares no observing step")
	}
	if s.Contract.Kind.RequiresMutation() && len(s.Contract.Targets) == 0 {
		return blockf(FailureInvalidSpec, "", "", "%s contract declares no targets, so no evidence could ever satisfy it", s.Contract.Kind)
	}
	return nil
}

// observes reports whether the program contains at least one step that can
// actually look at the workspace.
func (p Program) observes() bool {
	for _, s := range p {
		switch s.Capability {
		case WorkspaceDiscover, FileRead, FileSearch, FileExists, CommandRun:
			return true
		default:
			// A mutating capability is not an observation capability. "I wrote
			// a file" is not "I looked at the workspace".
		}
	}
	return false
}

// contractNames reports whether targets names target, comparing workspace-
// relative paths exactly. There is deliberately no fuzzy or prefix matching: a
// near-miss destination is a refusal, because substituting one is a mutation
// nobody authorized.
func contractNames(targets []string, target string) bool {
	normalised := strings.TrimPrefix(target, "./")
	if normalised == "" {
		return false
	}
	for _, t := range targets {
		candidate := strings.TrimPrefix(t, "./")
		if candidate != "" && candidate == normalised {
			return true
		}
	}
	return false
}

// String renders the spec as a single stable line, suitable for a log record.
func (s Spec) String() string {
	return fmt.Sprintf("execution=%s contract=%s steps=[%s]", s.ExecutionID, s.Contract.Kind, strings.Join(s.Program.ids(), " "))
}

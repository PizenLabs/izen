package domain

// ExecutionClass enumerates the authority tier of an execution.
type ExecutionClass uint8

const (
	ClassReadOnly          ExecutionClass = iota // $prompt trivial, /ask
	ClassAnalysis                                // /investigate, $prompt repository
	ClassPlanning                                // /plan
	ClassMicroMutation                           // /build $hot — bounded Micro-Plan
	ClassControlledMutation                      // /build — full plan
	ClassReview                                  // /review
)

func (c ExecutionClass) String() string {
	switch c {
	case ClassReadOnly:
		return "READ_ONLY"
	case ClassAnalysis:
		return "ANALYSIS"
	case ClassPlanning:
		return "PLANNING"
	case ClassMicroMutation:
		return "MICRO_MUTATION"
	case ClassControlledMutation:
		return "CONTROLLED_MUTATION"
	case ClassReview:
		return "REVIEW"
	default:
		return "UNKNOWN"
	}
}

// Capability bitmask for AuthorityRule. Distinct from core/capability's
// Capability type; these are domain-authority flags.
type CapabilityFlag uint32

const (
	CapRead            CapabilityFlag = 1 << iota // read workspace
	CapSearch                                     // search / grep / symbol query
	CapTest                                       // run tests/diagnostics
	CapExecDiagnostic                             // diagnostic exec (lint, vet)
	CapWrite                                      // write files
	CapPatch                                      // apply patches
	CapCheckpoint                                 // checkpoint operations
	CapRollback                                   // rollback operations
	CapExecRestricted                             // restricted shell exec under guard
)

// CapabilitySet is a bitmask of CapabilityFlag.
type DomainCapabilitySet uint32

func (s DomainCapabilitySet) Has(flag CapabilityFlag) bool { return uint32(s)&uint32(flag) != 0 }
func (s DomainCapabilitySet) Contains(flag CapabilityFlag) bool { return s.Has(flag) }

// AuthorityRule declares which capabilities and approvals a class grants by default.
type AuthorityRule struct {
	Class               ExecutionClass      `json:"class"`
	DefaultCapabilities DomainCapabilitySet `json:"capabilities"` // bitmask
	RequiresApproval    bool                `json:"requires_approval"`
	RequiresCheckpoint  bool                `json:"requires_checkpoint"`
	MaxBudget           ResourceBudget      `json:"max_budget"`
}

// DefaultAuthorityRules is the normative table (ARCH:4 narrative, ARCH:15).
var DefaultAuthorityRules = map[ExecutionClass]AuthorityRule{
	ClassReadOnly: {
		Class:               ClassReadOnly,
		DefaultCapabilities: DomainCapabilitySet(CapRead | CapSearch),
		RequiresApproval:    false,
		RequiresCheckpoint:  false,
	},
	ClassAnalysis: {
		Class:               ClassAnalysis,
		DefaultCapabilities: DomainCapabilitySet(CapRead | CapTest | CapSearch | CapExecDiagnostic),
		RequiresApproval:    false,
		RequiresCheckpoint:  false,
	},
	ClassPlanning: {
		Class:               ClassPlanning,
		DefaultCapabilities: DomainCapabilitySet(CapRead | CapSearch),
		RequiresApproval:    false,
		RequiresCheckpoint:  false,
	},
	ClassMicroMutation: {
		Class:               ClassMicroMutation,
		DefaultCapabilities: DomainCapabilitySet(CapRead | CapWrite | CapTest | CapPatch | CapCheckpoint),
		RequiresApproval:    false,
		RequiresCheckpoint:  true,
		MaxBudget:           ResourceBudget{MaxFiles: 2, MaxDiffLines: 50, MaxAttempts: 1},
	},
	ClassControlledMutation: {
		Class:               ClassControlledMutation,
		DefaultCapabilities: DomainCapabilitySet(CapRead | CapWrite | CapTest | CapPatch | CapExecRestricted | CapCheckpoint | CapRollback),
		RequiresApproval:    true,
		RequiresCheckpoint:  true,
	},
	ClassReview: {
		Class:               ClassReview,
		DefaultCapabilities: DomainCapabilitySet(CapRead | CapTest | CapExecDiagnostic),
		RequiresApproval:    false,
		RequiresCheckpoint:  false,
	},
}

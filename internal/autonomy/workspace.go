package autonomy

import (
	"fmt"
	"strings"
)

// Workspace is an execution space treated as a capability domain. Workspaces
// are NOT a linear pipeline: each one declares which capabilities it may use
// and which it forbids. The router selects a workspace from
// (intent + risk + required capability), never from a hardcoded phase order.
type Workspace string

// The canonical workspaces. They intentionally mirror the execution phases so
// existing engines remain addressable, but here they are pure capability
// domains.
const (
	WorkspaceAsk         Workspace = "ask"
	WorkspaceInvestigate Workspace = "investigate"
	WorkspacePlan        Workspace = "plan"
	WorkspaceBuild       Workspace = "build"
	WorkspaceReview      Workspace = "review"
	WorkspaceNone        Workspace = ""
)

// String returns the canonical workspace label.
func (w Workspace) String() string {
	if w == "" {
		return "none"
	}
	return string(w)
}

// Contract declares the capability boundary of one workspace: the capabilities
// the workspace may exercise and those it forbids. A forbidden capability
// trumps an allowed one — a contract is a deny-by-default rule set.
type Contract struct {
	Workspace Workspace
	Allowed   CapabilitySet
	Forbidden CapabilitySet
	// ReadOnly marks the workspace as incapable of mutation regardless of the
	// Allowed set (Ask/Investigate/Plan/Review never touch disk content).
	ReadOnly bool
}

// Allows reports whether the contract permits cap for the workspace.
func (c Contract) Allows(cap Capability) bool {
	if c.ReadOnly && cap == CapMutate {
		return false
	}
	if c.Forbidden.Has(cap) {
		return false
	}
	return c.Allowed.Has(cap)
}

// Covers reports whether the contract permits every required capability.
func (c Contract) Covers(required CapabilitySet) bool {
	if len(required) == 0 {
		return true
	}
	for _, cap := range required {
		if !c.Allows(cap) {
			return false
		}
	}
	return true
}

// WorkspaceContracts is the immutable contract table. It defines what each
// capability domain may and may not do. This table is the capability authority
// for workspace selection: the selector never invents a workspace boundary.
var WorkspaceContracts = map[Workspace]Contract{
	WorkspaceAsk: {
		Workspace: WorkspaceAsk,
		Allowed:   CapabilitySet{CapRead},
		Forbidden: CapabilitySet{CapMutate, CapVerify},
		ReadOnly:  true,
	},
	WorkspaceInvestigate: {
		Workspace: WorkspaceInvestigate,
		Allowed:   CapabilitySet{CapRead, CapAnalyze},
		Forbidden: CapabilitySet{CapMutate},
		ReadOnly:  true,
	},
	WorkspacePlan: {
		Workspace: WorkspacePlan,
		Allowed:   CapabilitySet{CapRead, CapAnalyze, CapPropose},
		Forbidden: CapabilitySet{CapMutate},
		ReadOnly:  true,
	},
	WorkspaceBuild: {
		Workspace: WorkspaceBuild,
		Allowed:   CapabilitySet{CapRead, CapAnalyze, CapPropose, CapMutate, CapVerify},
		Forbidden: CapabilitySet{},
		ReadOnly:  false,
	},
	WorkspaceReview: {
		Workspace: WorkspaceReview,
		Allowed:   CapabilitySet{CapRead, CapAnalyze, CapVerify},
		Forbidden: CapabilitySet{CapMutate},
		ReadOnly:  true,
	},
}

// ContractFor returns the capability contract for a workspace. Unknown
// workspaces yield the Ask contract (the most restrictive read-only boundary).
func ContractFor(w Workspace) Contract {
	if c, ok := WorkspaceContracts[w]; ok {
		return c
	}
	return WorkspaceContracts[WorkspaceAsk]
}

// RiskLevel classifies the mutation risk of acting on a target.
type RiskLevel int

const (
	RiskUnknown RiskLevel = iota
	RiskLow
	RiskMedium
	RiskHigh
	RiskCritical
)

// String returns the canonical risk label.
func (r RiskLevel) String() string {
	switch r {
	case RiskLow:
		return "low"
	case RiskMedium:
		return "medium"
	case RiskHigh:
		return "high"
	case RiskCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// ParseRisk maps a risk label onto the RiskLevel enum.
func ParseRisk(s string) RiskLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return RiskLow
	case "medium", "moderate":
		return RiskMedium
	case "high":
		return RiskHigh
	case "critical":
		return RiskCritical
	default:
		return RiskUnknown
	}
}

// PreferredWorkspace maps an intent onto its primary capability domain. The
// mapping is intent-first: a mode is never used to derive the workspace.
func PreferredWorkspace(i Intent) Workspace {
	switch i {
	case IntentConversation:
		return WorkspaceAsk
	case IntentExplanation:
		return WorkspaceAsk
	case IntentInvestigation:
		return WorkspaceInvestigate
	case IntentDebugging:
		return WorkspaceInvestigate
	case IntentPlanning:
		return WorkspacePlan
	case IntentVerification:
		return WorkspaceReview
	case IntentModification:
		return WorkspaceBuild
	case IntentRefactoring:
		return WorkspaceBuild
	default:
		return WorkspaceAsk
	}
}

// WorkspaceRoute is the outcome of capability-based workspace selection: the
// selected workspace, whether its contract covers the required capabilities,
// and the justification.
type WorkspaceRoute struct {
	Workspace Workspace
	Covers    bool
	Reason    string
}

// SelectWorkspace picks the capability domain for an intent given the mutation
// risk and the required capability vector. Selection is a pure function of the
// workspace contracts — no mode, no phase graph, no pipeline order.
//
// The contract boundaries are enforced here, not downstream: an intent that
// requires mutation always selects BUILD (the only domain whose contract
// permits CapMutate); an intent that forbids mutation never enters BUILD even
// when a target is named.
func SelectWorkspace(i Intent, risk RiskLevel, required CapabilitySet) WorkspaceRoute {
	if !i.RequiresWorkspace() {
		return WorkspaceRoute{Workspace: WorkspaceAsk, Covers: true,
			Reason: "conversation intent — direct response, no workspace"}
	}

	pref := PreferredWorkspace(i)

	if i.RequiresMutation() {
		// Mutation is only legal inside the BUILD capability domain. This is a
		// hard contract: no other workspace grants CapMutate.
		return WorkspaceRoute{
			Workspace: WorkspaceBuild,
			Covers:    ContractFor(WorkspaceBuild).Covers(required),
			Reason:    fmt.Sprintf("mutation intent requires BUILD capability domain (risk %s)", risk),
		}
	}

	// Read-only intents: keep the preferred workspace when its contract covers
	// the required capabilities; otherwise fall back to the most permissive
	// read-only domain that does.
	contract := ContractFor(pref)
	if contract.Covers(required) {
		return WorkspaceRoute{Workspace: pref, Covers: true,
			Reason: fmt.Sprintf("workspace %s covers required capabilities %s", pref, required)}
	}
	for _, ws := range []Workspace{WorkspaceInvestigate, WorkspacePlan, WorkspaceReview, WorkspaceAsk} {
		if ContractFor(ws).Covers(required) {
			return WorkspaceRoute{Workspace: ws, Covers: true,
				Reason: fmt.Sprintf("workspace %s covers required capabilities %s", ws, required)}
		}
	}
	return WorkspaceRoute{Workspace: pref, Covers: false,
		Reason: fmt.Sprintf("no workspace contract covers required capabilities %s", required)}
}

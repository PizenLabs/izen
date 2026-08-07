package command

// WorkspaceType identifies one of the five workflow contexts. A workspace
// defines the current working context — it answers "what am I trying to do?",
// and carries the immutable permission boundary for everything performed
// inside it.
type WorkspaceType int

const (
	// WorkspaceAsk explains, inspects, and understands. Read-only chat.
	WorkspaceAsk WorkspaceType = iota
	// WorkspaceInvestigate observes, diagnoses, and explains. Bounded forensic
	// loops, never mutating files.
	WorkspaceInvestigate
	// WorkspacePlan thinks, organizes, and produces an execution plan. No
	// execution.
	WorkspacePlan
	// WorkspaceBuild modifies the workspace. The only workflow that changes
	// project files.
	WorkspaceBuild
	// WorkspaceReview validates, verifies, and reports. Read-only audit.
	WorkspaceReview
)

// String returns the canonical workspace name (its slash command, marker-less).
func (w WorkspaceType) String() string {
	switch w {
	case WorkspaceAsk:
		return "ask"
	case WorkspaceInvestigate:
		return "investigate"
	case WorkspacePlan:
		return "plan"
	case WorkspaceBuild:
		return "build"
	case WorkspaceReview:
		return "review"
	default:
		return "unknown"
	}
}

// Permissions returns the immutable PermissionSet bound to the workspace.
//
//	| Workspace    | Read | Analyze | Execute | Write |
//	|--------------|------|---------|---------|-------|
//	| ask          | Y    | N       | N       | N     |
//	| investigate  | Y    | Y       | N       | N     |
//	| plan         | Y    | Y       | N       | N     |
//	| build        | Y    | Y       | Y       | Y     |
//	| review       | Y    | Y       | Y       | N     |
func (w WorkspaceType) Permissions() PermissionSet {
	switch w {
	case WorkspaceAsk:
		return PermissionSet(PermRead)
	case WorkspaceInvestigate:
		return PermissionSet(PermRead | PermAnalyze)
	case WorkspacePlan:
		return PermissionSet(PermRead | PermAnalyze)
	case WorkspaceBuild:
		return PermissionSet(PermRead | PermAnalyze | PermWrite | PermExecute)
	case WorkspaceReview:
		return PermissionSet(PermRead | PermAnalyze | PermExecute)
	default:
		return 0
	}
}

// description returns the one-line human summary of the workspace.
func (w WorkspaceType) description() string {
	switch w {
	case WorkspaceAsk:
		return "explain, inspect, understand — read-only chat"
	case WorkspaceInvestigate:
		return "debug bugs, failures, regressions — bounded forensic loops"
	case WorkspacePlan:
		return "architecture, migrations, refactors — no execution"
	case WorkspaceBuild:
		return "implement, refactor, write tests — controlled execution"
	case WorkspaceReview:
		return "audit changes, detect risks, inspect regressions"
	default:
		return ""
	}
}

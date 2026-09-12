package scopeguard

import (
	"fmt"
	"strings"
	"sync"
)

// Workspace is one of the five semantic workspaces. All five operate on
// the identical TaskState, EventLedger and CheckpointStore; a transition
// alters Semantic Policy and Capability Allowances, NEVER task identity
// or history (Invariant 5: workspace switch != task reset).
type Workspace string

const (
	WorkspacePlan        Workspace = "plan"
	WorkspaceInvestigate Workspace = "investigate"
	WorkspaceBuild       Workspace = "build"
	WorkspaceReview      Workspace = "review"
	WorkspaceOrganize    Workspace = "organize"
)

// Valid reports whether w is a known workspace.
func (w Workspace) Valid() bool {
	switch w {
	case WorkspacePlan, WorkspaceInvestigate, WorkspaceBuild, WorkspaceReview, WorkspaceOrganize:
		return true
	default:
		return false
	}
}

// CapabilitySet is the allow-list of capability classes for a workspace.
type CapabilitySet struct {
	Read  bool `json:"read"`
	Write bool `json:"write"`
	Patch bool `json:"patch"`
	Test  bool `json:"test"`
}

// Allows reports whether op is permitted in the set.
func (c CapabilitySet) Allows(op OpClass) bool {
	switch op {
	case OpRead:
		return c.Read
	case OpWrite:
		return c.Write
	case OpPatch:
		return c.Write || c.Patch
	case OpDelete:
		return c.Write
	case OpTest:
		return c.Test
	default:
		return false
	}
}

// WorkspacePolicy is the semantic policy + capability allowance for one
// workspace mode.
type WorkspacePolicy struct {
	// Mode is the workspace this policy governs.
	Mode Workspace `json:"mode"`
	// Allowed is the capability allow-list.
	Allowed CapabilitySet `json:"allowed"`
	// Focus is the context emphasis for the workspace.
	Focus string `json:"focus"`
}

// PolicyFor returns the canonical policy for a workspace:
//
//	/plan:       READ        — objectives, constraints, acceptance criteria
//	/investigate: READ, TEST — symbol graphs, call stacks, diagnostics
//	/build:      READ, WRITE, PATCH, TEST — bounded implementation in scope
//	/review:     READ, TEST  — diff verification, evidence, regression
//	/organize:   READ, WRITE — scoped reorganization within AuthorizedTargetScope
func PolicyFor(w Workspace) (WorkspacePolicy, error) {
	switch w {
	case WorkspacePlan:
		return WorkspacePolicy{Mode: w, Allowed: CapabilitySet{Read: true},
			Focus: "High-level objectives, constraints, acceptance criteria"}, nil
	case WorkspaceInvestigate:
		return WorkspacePolicy{Mode: w, Allowed: CapabilitySet{Read: true, Test: true},
			Focus: "Symbol graphs, call stacks, diagnostic execution"}, nil
	case WorkspaceBuild:
		return WorkspacePolicy{Mode: w, Allowed: CapabilitySet{Read: true, Write: true, Patch: true, Test: true},
			Focus: "Bounded implementation within AuthorizedTargetScope"}, nil
	case WorkspaceReview:
		return WorkspacePolicy{Mode: w, Allowed: CapabilitySet{Read: true, Test: true},
			Focus: "Diff verification, evidence validation, regression tests"}, nil
	case WorkspaceOrganize:
		return WorkspacePolicy{Mode: w, Allowed: CapabilitySet{Read: true, Write: true},
			Focus: "Scoped reorganization within AuthorizedTargetScope"}, nil
	default:
		return WorkspacePolicy{}, fmt.Errorf("scopeguard: unknown workspace %q", w)
	}
}

// WorkspaceSession binds one durable task to its current workspace policy
// while preserving identity, lineage, evidence and negative knowledge
// across switches.
type WorkspaceSession struct {
	mu sync.Mutex
	// TaskID is the single task identity (never rewritten by switches).
	TaskID string `json:"taskId"`
	// CheckpointID is the active checkpoint pointer (preserved).
	CheckpointID string `json:"checkpointId"`
	// EvidenceLedger is the verified evidence lineage (preserved).
	EvidenceLedger []EvidenceRef `json:"evidenceLedger"`
	// ActiveNegativeKnowledge is the verified negative set (preserved).
	ActiveNegativeKnowledge []NegativeRef `json:"activeNegativeKnowledge"`
	// Workspace is the current semantic mode.
	Workspace Workspace `json:"workspace"`
	// Policy is the active semantic policy (derived from Workspace).
	Policy WorkspacePolicy `json:"policy"`
	// Switches counts workspace transitions (audit).
	Switches int `json:"switches"`
}

// EvidenceRef is a lightweight evidence pointer preserved across switches.
type EvidenceRef struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Digest  string `json:"digest,omitempty"`
}

// NegativeRef is a lightweight negative-knowledge pointer preserved
// across switches.
type NegativeRef struct {
	ID         string `json:"id"`
	Hypothesis string `json:"hypothesis"`
}

// Ledger is the minimal ledger sink for workspace-switch lineage.
type Ledger interface {
	RecordCustomEvent(taskID string, eventType string, payload map[string]any) error
}

// NewWorkspaceSession binds a task to its initial workspace.
func NewWorkspaceSession(taskID, checkpointID string, w Workspace) (*WorkspaceSession, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("scopeguard: empty task id")
	}
	if !w.Valid() {
		return nil, fmt.Errorf("scopeguard: unknown workspace %q", w)
	}
	pol, err := PolicyFor(w)
	if err != nil {
		return nil, err
	}
	return &WorkspaceSession{
		TaskID:       taskID,
		CheckpointID: checkpointID,
		Workspace:    w,
		Policy:       pol,
	}, nil
}

// AttachEvidence records verified evidence into the session lineage.
func (s *WorkspaceSession) AttachEvidence(ref EvidenceRef) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.EvidenceLedger = append(s.EvidenceLedger, ref)
}

// AttachNegative records verified negative knowledge into the lineage.
func (s *WorkspaceSession) AttachNegative(ref NegativeRef) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ActiveNegativeKnowledge = append(s.ActiveNegativeKnowledge, ref)
}

// Snapshot returns a copy of the preserved lineage fields.
func (s *WorkspaceSession) Snapshot() (taskID, checkpointID string, evidence []EvidenceRef, negatives []NegativeRef, w Workspace) {
	if s == nil {
		return "", "", nil, nil, ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.TaskID, s.CheckpointID,
		append([]EvidenceRef(nil), s.EvidenceLedger...),
		append([]NegativeRef(nil), s.ActiveNegativeKnowledge...),
		s.Workspace
}

// SwitchTo transitions the session to a new workspace policy. It alters
// ONLY the semantic policy and capability allowances: TaskID,
// CheckpointID, EvidenceLedger and ActiveNegativeKnowledge are preserved
// verbatim. A WORKSPACE_SWITCHED lineage event is recorded when a ledger
// sink is supplied (nil ledger disables lineage without failing).
func (s *WorkspaceSession) SwitchTo(next Workspace, ledger Ledger) error {
	if s == nil {
		return fmt.Errorf("scopeguard: nil session")
	}
	if !next.Valid() {
		return fmt.Errorf("scopeguard: unknown workspace %q", next)
	}
	pol, err := PolicyFor(next)
	if err != nil {
		return err
	}
	s.mu.Lock()
	prev := s.Workspace
	s.Workspace = next
	s.Policy = pol
	s.Switches++
	taskID := s.TaskID
	s.mu.Unlock()
	if ledger != nil {
		_ = ledger.RecordCustomEvent(taskID, "WORKSPACE_SWITCHED", map[string]any{
			"from": string(prev),
			"to":   string(next),
		})
	}
	return nil
}

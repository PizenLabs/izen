package ephemeral

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// DefaultRecompactEvidenceKeep bounds the TOKEN_LIMIT resume capsule to
// the most recent evidence summaries so the replacement worker fits its
// context window without losing the current step or budget.
const DefaultRecompactEvidenceKeep = 5

// StepDefinition is the bounded description of one unit of work. It carries
// only what a replacement worker needs to continue: identity, goal, tool
// allow-list and capability requirements. It never carries prompt history.
type StepDefinition struct {
	ID               string   `json:"id"`
	Goal             string   `json:"goal"`
	RequiredCaps     []string `json:"requiredCaps,omitempty"`
	MinContextTokens int      `json:"minContextTokens,omitempty"`
}

// EvidenceSummary is a compact, transcript-free digest of one piece of
// verified evidence (a digest, a check result, a file outcome). Summaries
// replace raw tool outputs and conversation turns.
type EvidenceSummary struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Digest  string `json:"digest,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// BudgetState is the remaining bounded-recovery budget carried in the
// capsule so every handoff observes the same exhaustion boundary.
type BudgetState struct {
	RecoveryAttemptsLeft int `json:"recoveryAttemptsLeft"`
	MaxRecoveryAttempts  int `json:"maxRecoveryAttempts"`
}

// TaskCapsule is the transcript-free, bounded state snapshot derived from
// TaskState for worker handoff. A replacement model MUST be able to resume
// using strictly the capsule plus the ResumeContract: no conversation
// history, no prompt transcripts, no raw tool logs.
type TaskCapsule struct {
	TaskID          string            `json:"taskId"`
	Objective       string            `json:"objective"`
	Constraints     []string          `json:"constraints,omitempty"`
	CompletedSteps  []string          `json:"completedSteps,omitempty"`
	CurrentStep     StepDefinition    `json:"currentStep"`
	PendingSteps    []string          `json:"pendingSteps,omitempty"`
	ActiveScope     []string          `json:"activeScope,omitempty"`
	LatestEvidence  []EvidenceSummary `json:"latestEvidence,omitempty"`
	RemainingBudget BudgetState       `json:"remainingBudget"`
}

// ResumeContract binds a capsule to one handoff: which checkpoint the new
// worker resumes from, why the previous worker stopped, and which tools it
// may invoke. The contract is the ONLY context a replacement session
// receives.
type ResumeContract struct {
	Capsule      TaskCapsule   `json:"capsule"`
	CheckpointID string        `json:"checkpointId"`
	ResumeReason FailureReason `json:"resumeReason"`
	AllowedTools []string      `json:"allowedTools,omitempty"`
}

// CapsuleSource is the task-level context the runtime supplies when
// deriving a capsule. It extends the durable TaskState with plan-level
// detail the ledger does not store (objectives, step graph, evidence).
type CapsuleSource struct {
	State          durable.TaskState
	Objective      string
	Constraints    []string
	CompletedSteps []string
	Current        StepDefinition
	PendingSteps   []string
	Evidence       []EvidenceSummary
	Budget         BudgetState
	AllowedTools   []string
}

// DeriveCapsule builds a bounded TaskCapsule from task state. It copies
// slices so later mutation of the source cannot leak into the capsule, and
// it caps evidence to the most recent maxEvidence entries to keep the
// replacement prompt bounded.
func DeriveCapsule(src CapsuleSource, maxEvidence int) TaskCapsule {
	if maxEvidence < 0 {
		maxEvidence = 0
	}
	ev := append([]EvidenceSummary(nil), src.Evidence...)
	if maxEvidence > 0 && len(ev) > maxEvidence {
		ev = append([]EvidenceSummary(nil), ev[len(ev)-maxEvidence:]...)
	}
	objective := src.Objective
	if strings.TrimSpace(objective) == "" {
		objective = src.State.Intent
	}
	return TaskCapsule{
		TaskID:          src.State.ID,
		Objective:       objective,
		Constraints:     append([]string(nil), src.Constraints...),
		CompletedSteps:  append([]string(nil), src.CompletedSteps...),
		CurrentStep:     src.Current,
		PendingSteps:    append([]string(nil), src.PendingSteps...),
		ActiveScope:     append([]string(nil), src.State.ActiveTargetScope...),
		LatestEvidence:  ev,
		RemainingBudget: src.Budget,
	}
}

// BuildResumeContract derives the bounded contract a replacement worker
// receives. It never embeds transcripts: callers pass only the capsule,
// checkpoint, reason and tool allow-list. A nil/empty tool list means "no
// tools" rather than "all tools".
func BuildResumeContract(capsule TaskCapsule, checkpointID string, reason FailureReason, allowedTools []string) (ResumeContract, error) {
	if strings.TrimSpace(capsule.TaskID) == "" {
		return ResumeContract{}, fmt.Errorf("ephemeral: empty capsule task id")
	}
	if strings.TrimSpace(capsule.CurrentStep.ID) == "" {
		return ResumeContract{}, fmt.Errorf("ephemeral: capsule has no current step")
	}
	if !reason.Valid() {
		return ResumeContract{}, fmt.Errorf("ephemeral: invalid resume reason %q", reason)
	}
	if strings.TrimSpace(checkpointID) == "" {
		return ResumeContract{}, fmt.Errorf("ephemeral: empty checkpoint id")
	}
	return ResumeContract{
		Capsule:      capsule,
		CheckpointID: checkpointID,
		ResumeReason: reason,
		AllowedTools: append([]string(nil), allowedTools...),
	}, nil
}

// Recompact returns a copy of c with evidence trimmed to the most recent
// keep entries and constraints de-duplicated. Used on TOKEN_LIMIT failover
// so the replacement worker fits an equal-or-larger context window without
// losing the current step or budget state.
func (c TaskCapsule) Recompact(keep int) TaskCapsule {
	out := c
	if keep < 0 {
		keep = 0
	}
	if len(out.LatestEvidence) > keep {
		out.LatestEvidence = append([]EvidenceSummary(nil), out.LatestEvidence[len(out.LatestEvidence)-keep:]...)
	} else {
		out.LatestEvidence = append([]EvidenceSummary(nil), out.LatestEvidence...)
	}
	seen := make(map[string]struct{}, len(out.Constraints))
	dedup := out.Constraints[:0]
	for _, k := range out.Constraints {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		dedup = append(dedup, k)
	}
	out.Constraints = dedup
	out.CompletedSteps = append([]string(nil), out.CompletedSteps...)
	out.PendingSteps = append([]string(nil), out.PendingSteps...)
	out.ActiveScope = append([]string(nil), out.ActiveScope...)
	return out
}

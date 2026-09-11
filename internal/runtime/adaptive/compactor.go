package adaptive

import (
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/ephemeral"
)

// CompactedContext is the smallest faithful execution context
// re-materialized from durable state. By construction it carries ONLY the
// preserved fields below: stream prose, transient tool chatter, superseded
// reasoning and unreferenced explorations are discarded (they are never
// inputs, so they cannot leak into the output).
type CompactedContext struct {
	Intent               string
	ActiveScope          []string
	CurrentStepID        string
	CurrentStepGoal      string
	VerifiedEvidenceRefs []string
	RemainingBudget      int
	MaxBudget            int
	ActiveNegatives      []NegativeKnowledge
	ContextTier          ContextTier
	CheckpointID         string
}

// CompactInput is the durable + ephemeral state the compactor reads. It
// carries no transcripts: callers pass materialized state, step
// definitions, evidence summaries, budget and ACTIVE negatives only.
type CompactInput struct {
	State     durable.TaskState
	Objective string
	Current   ephemeral.StepDefinition
	Evidence  []ephemeral.EvidenceSummary
	Budget    ephemeral.BudgetState
	Negatives []NegativeKnowledge
	Tier      ContextTier
}

// Compactor re-materializes minimal execution context. The zero value is
// ready to use; Compact is pure and goroutine-safe.
type Compactor struct{}

// Compact preserves: original intent, active target scope, current step,
// verified evidence references, remaining budget, and ACTIVE negative
// knowledge. It discards everything else by construction (no prose fields
// exist on the input). It MUST NOT alter the authority graph (it touches
// no authority state) or invalidate active negative constraints (STALE
// entries are dropped, ACTIVE entries are carried verbatim).
func (Compactor) Compact(in CompactInput) CompactedContext {
	intent := in.State.Intent
	if intent == "" {
		intent = in.Objective
	}
	out := CompactedContext{
		Intent:          intent,
		ActiveScope:     append([]string(nil), in.State.ActiveTargetScope...),
		CurrentStepID:   in.Current.ID,
		CurrentStepGoal: in.Current.Goal,
		RemainingBudget: in.Budget.RecoveryAttemptsLeft,
		MaxBudget:       in.Budget.MaxRecoveryAttempts,
		ContextTier:     in.Tier,
		CheckpointID:    in.State.LastCheckpointID,
	}
	if out.ActiveScope == nil {
		out.ActiveScope = []string{}
	}
	seen := make(map[string]struct{})
	for _, e := range in.Evidence {
		key := e.Kind + "\x00" + e.Subject + "\x00" + e.Digest
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ref := e.Subject
		if e.Digest != "" {
			ref += "@" + e.Digest
		} else if e.Detail != "" {
			ref += ":" + e.Detail
		}
		out.VerifiedEvidenceRefs = append(out.VerifiedEvidenceRefs, ref)
	}
	if out.VerifiedEvidenceRefs == nil {
		out.VerifiedEvidenceRefs = []string{}
	}
	for _, n := range in.Negatives {
		if n.Status != StatusActiveNegativeKnowledge {
			continue
		}
		cp := n
		cp.EvidenceRefs = append([]string(nil), n.EvidenceRefs...)
		cp.TargetScope = append([]string(nil), n.TargetScope...)
		out.ActiveNegatives = append(out.ActiveNegatives, cp)
	}
	if out.ActiveNegatives == nil {
		out.ActiveNegatives = []NegativeKnowledge{}
	}
	return out
}

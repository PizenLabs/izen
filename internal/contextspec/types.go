// Package contextspec is the Context Domain: the compiled, bounded, revision-
// bound and UNTRUSTED semantic state that sits between an unbounded human
// conversation and the Execution Domain.
//
// The three domains are strictly separated:
//
//	Conversation Domain  — human-oriented, mutable, unbounded (internal/session)
//	Context Domain       — compiled, bounded, revision-bound, UNTRUSTED (here)
//	Execution Domain     — frozen contract + single RuntimeExecutor authority
//
// A ContextSpec is an LLM-derived semantic PROPOSAL. It never authorizes
// anything: it cannot write, patch, shell, grant scope, bypass policy or reach
// the RuntimeExecutor. Only the Control Plane (Pipeline) may commit a
// candidate and freeze an ExecutionSpec at an explicit hand-off boundary.
//
// This package intentionally imports nothing from the execution, patch or
// autonomy layers so there is no reachable path from compiled context to
// execution authority. The workspace snapshot is read through the narrow
// SnapshotPort interface, whose production adapter lives at the composition
// boundary.
package contextspec

import (
	"sort"
	"time"

	"github.com/PizenLabs/izen/internal/session"
)

// Payload schema versions.
const (
	// ContextSpecVersion is the ContextSpec schema version.
	ContextSpecVersion = 1
	// ExecutionSpecVersion is the ExecutionSpec schema version.
	ExecutionSpecVersion = 1
)

// ConversationState is the Conversation Domain projection fed to the compiler:
// the raw, human-oriented history plus its monotonic revision clock. It is
// intentionally unbounded and never crosses into execution.
type ConversationState struct {
	// Revision is the monotonic conversation revision this state describes.
	Revision uint64
	// Objective is the current human objective (session objective or the
	// explicit hand-off objective).
	Objective string
	// Messages is the bounded conversation window the compiler reasons over.
	Messages []session.Message
}

// ConversationStateFromSession projects a live session into the compiler's
// ConversationState. The window is the session's own bounded history, so the
// compiler never sees an unbounded transcript.
func ConversationStateFromSession(s *session.Session, objective string) ConversationState {
	if s == nil {
		return ConversationState{Objective: objective}
	}
	if objective == "" {
		objective = s.ObjectiveIntent()
	}
	return ConversationState{
		Revision:  s.ConversationRevision(),
		Objective: objective,
		Messages:  append([]session.Message(nil), s.History...),
	}
}

// Provenance traces one semantic item to the conversation revision and turn it
// was derived from. It is lineage metadata — it must never inflate the
// execution payload.
type Provenance struct {
	ConversationRevision uint64 `json:"conversation_revision"`
	SourceTurn           int    `json:"source_turn"`
	Role                 string `json:"role,omitempty"`
}

// TargetRef is a workspace target the conversation referred to. Referencing a
// target is NOT authority to mutate it.
type TargetRef struct {
	Path       string     `json:"path"`
	Provenance Provenance `json:"provenance"`
}

// Constraint is one semantic constraint with an explicit active/superseded
// flag. Superseded constraints remain in the ContextSpec for audit/lineage but
// are excluded from the execution payload.
type Constraint struct {
	ID         string     `json:"id"`
	Text       string     `json:"text"`
	Active     bool       `json:"active"`
	Provenance Provenance `json:"provenance"`
}

// Decision is one active decision of the conversation. Only the last active
// decision per topic survives; earlier contradictory decisions are retained
// with Active=false as lineage.
type Decision struct {
	ID         string     `json:"id"`
	Topic      string     `json:"topic"`
	Text       string     `json:"text"`
	Active     bool       `json:"active"`
	Provenance Provenance `json:"provenance"`
}

// Question is an unresolved open question raised by the conversation.
type Question struct {
	ID         string     `json:"id"`
	Text       string     `json:"text"`
	Provenance Provenance `json:"provenance"`
}

// ContextSpec is the compiled semantic state of the conversation at exactly one
// conversation revision. It is UNTRUSTED: it is a proposal, never authority.
type ContextSpec struct {
	// Version is the payload schema version.
	Version int `json:"version"`
	// SpecRevision is the monotonic accepted-compilation counter.
	SpecRevision uint64 `json:"spec_revision"`
	// ConversationRevision is the conversation revision this spec was compiled
	// from. Freshness is exactly ConversationRevision == current revision.
	ConversationRevision uint64 `json:"conversation_revision"`

	Goal          string       `json:"goal,omitempty"`
	Targets       []TargetRef  `json:"targets,omitempty"`
	Constraints   []Constraint `json:"constraints,omitempty"`
	Decisions     []Decision   `json:"decisions,omitempty"`
	OpenQuestions []Question   `json:"open_questions,omitempty"`

	CompiledAt time.Time `json:"compiled_at,omitempty"`
}

// IsFresh reports revision coherence. This is the ONLY freshness test in the
// system: there is no semantic dirty detector.
func (s ContextSpec) IsFresh(conversationRevision uint64) bool {
	return s.ConversationRevision == conversationRevision
}

// ActiveTargets returns the referenced target paths in first-seen order.
func (s ContextSpec) ActiveTargets() []string {
	seen := make(map[string]struct{}, len(s.Targets))
	out := make([]string, 0, len(s.Targets))
	for _, t := range s.Targets {
		if t.Path == "" {
			continue
		}
		if _, ok := seen[t.Path]; ok {
			continue
		}
		seen[t.Path] = struct{}{}
		out = append(out, t.Path)
	}
	return out
}

// ActiveConstraints returns only the constraints still in force.
func (s ContextSpec) ActiveConstraints() []Constraint {
	out := make([]Constraint, 0, len(s.Constraints))
	for _, c := range s.Constraints {
		if c.Active {
			out = append(out, c)
		}
	}
	return out
}

// ActiveDecisions returns only the decisions still in force.
func (s ContextSpec) ActiveDecisions() []Decision {
	out := make([]Decision, 0, len(s.Decisions))
	for _, d := range s.Decisions {
		if d.Active {
			out = append(out, d)
		}
	}
	return out
}

// Clone returns a deep copy so an untrusted candidate can never alias committed
// runtime state.
func (s *ContextSpec) Clone() *ContextSpec {
	if s == nil {
		return nil
	}
	out := *s
	out.Targets = append([]TargetRef(nil), s.Targets...)
	out.Constraints = append([]Constraint(nil), s.Constraints...)
	out.Decisions = append([]Decision(nil), s.Decisions...)
	out.OpenQuestions = append([]Question(nil), s.OpenQuestions...)
	return &out
}

// Scope is the bounded target geometry of a frozen execution contract. It is
// descriptive: it does not grant scope.
type Scope struct {
	Targets    []string `json:"targets,omitempty"`
	Declared   bool     `json:"declared,omitempty"`
	Provenance string   `json:"provenance,omitempty"`
}

// Budget is the descriptive execution budget frozen into the contract. It is
// not a pre-approval and does not widen $hot semantics.
type Budget struct {
	MaxOutputTokens  int `json:"max_output_tokens,omitempty"`
	MaxContextTokens int `json:"max_context_tokens,omitempty"`
	MaxAttempts      int `json:"max_attempts,omitempty"`
	MaxFiles         int `json:"max_files,omitempty"`
	MaxDiffLines     int `json:"max_diff_lines,omitempty"`
}

// ExecutionSpec is the frozen execution contract produced at the hand-off
// boundary. It describes WHAT execution has been frozen for; it does not
// authorize itself. There is deliberately no AuthorizedBy field — authorization
// remains the independent, existing execution authority.
type ExecutionSpec struct {
	Version int    `json:"version"`
	SpecID  string `json:"spec_id"`
	Intent  string `json:"intent"`

	// Revisions bind the contract to the exact context it was frozen from.
	ContextRevision      uint64 `json:"context_revision"`
	ConversationRevision uint64 `json:"conversation_revision"`
	ContextSpecRevision  uint64 `json:"context_spec_revision"`

	Scope Scope `json:"scope"`

	// TargetHashes is the workspace snapshot the contract was frozen against.
	// A live workspace whose digest diverges from WorkspaceDigest must refuse
	// execution with ErrStaleWorkspaceSnapshot — it must never silently refresh.
	TargetHashes    map[string]string `json:"target_hashes,omitempty"`
	WorkspaceDigest string            `json:"workspace_digest,omitempty"`

	// Constraints/Decisions carry ONLY the active semantic state — historical
	// superseded items stay in ContextSpec lineage.
	Constraints []Constraint `json:"constraints,omitempty"`
	Decisions   []Decision   `json:"decisions,omitempty"`

	Budget   Budget    `json:"budget"`
	FrozenAt time.Time `json:"frozen_at"`
}

// WorkspaceSnapshot is the observation of a target set against the CURRENT
// workspace, produced by the existing OCC hashing mechanism (never a parallel
// hasher).
type WorkspaceSnapshot struct {
	Targets []string          `json:"targets,omitempty"`
	Hashes  map[string]string `json:"hashes,omitempty"`
	Digest  string            `json:"digest,omitempty"`
}

// SnapshotPort observes the current workspace state for a target set. The
// production adapter is bound to execution.OCCVerifier at the composition
// boundary; the Context Domain itself owns no filesystem or execution code.
type SnapshotPort interface {
	Observe(targets []string) WorkspaceSnapshot
}

// sortedTargets returns a deterministic copy of a target set.
func sortedTargets(targets []string) []string {
	out := append([]string(nil), targets...)
	sort.Strings(out)
	return out
}

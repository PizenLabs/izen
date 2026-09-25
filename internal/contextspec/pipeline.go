package contextspec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PipelineOptions binds the Context Domain ports. Compiler and Store default to
// the deterministic RuleCompiler and a fresh Store. Snapshot and Audit are
// optional; CurrentRevision is required for real CAS behaviour (nil means the
// caller guarantees the conversation cannot advance during compilation).
type PipelineOptions struct {
	Compiler        ContextCompiler
	Snapshot        SnapshotPort
	Audit           AuditSink
	Store           *Store
	CurrentRevision func() uint64
	Now             func() time.Time
}

// Pipeline is the Control Plane of the Context Domain: it lazily compiles
// ContextSpec candidates, CAS-commits them, freezes ExecutionSpec contracts at
// the hand-off boundary and validates workspace snapshot coherence. It never
// executes and never authorizes.
type Pipeline struct {
	compiler        ContextCompiler
	snapshot        SnapshotPort
	audit           AuditSink
	store           *Store
	currentRevision func() uint64
	now             func() time.Time
}

// NewPipeline wires a Context Domain pipeline.
func NewPipeline(opts PipelineOptions) *Pipeline {
	p := &Pipeline{
		compiler:        opts.Compiler,
		snapshot:        opts.Snapshot,
		audit:           opts.Audit,
		store:           opts.Store,
		currentRevision: opts.CurrentRevision,
		now:             opts.Now,
	}
	if p.compiler == nil {
		p.compiler = NewRuleCompiler()
	}
	if p.store == nil {
		p.store = NewStore()
	}
	if p.audit == nil {
		p.audit = nopAudit{}
	}
	if p.now == nil {
		p.now = time.Now
	}
	return p
}

// Current returns the committed ContextSpec (or nil when never compiled).
func (p *Pipeline) Current() *ContextSpec {
	if p == nil {
		return nil
	}
	return p.store.Current()
}

// EnsureFresh returns a fresh ContextSpec, compiling one only when required
// (lazy compilation). The second return value reports whether a compilation
// actually occurred. It is the only compilation entry point.
func (p *Pipeline) EnsureFresh(ctx context.Context, cs ConversationState) (*ContextSpec, bool, error) {
	if p == nil {
		return nil, false, ErrInvalidContext
	}
	if current := p.store.Current(); current != nil && current.IsFresh(cs.Revision) {
		return current, false, nil
	}
	p.emit(AuditEvent{Kind: EventCompilationStarted, ConversationRevision: cs.Revision})
	candidate, err := p.compiler.Compile(ctx, CompileInput{
		ConversationRevision: cs.Revision,
		Objective:            cs.Objective,
		Messages:             cs.Messages,
		PreviousSpec:         p.store.Current(),
	})
	if err != nil {
		return nil, false, err
	}
	nowRevision := cs.Revision
	if p.currentRevision != nil {
		nowRevision = p.currentRevision()
	}
	committed, err := p.store.Commit(candidate, nowRevision, p.now())
	if err != nil {
		if errors.Is(err, ErrStaleContextCandidate) {
			p.emit(AuditEvent{
				Kind:                 EventCompilationDiscardedStale,
				ConversationRevision: nowRevision,
				SpecRevision:         candidate.Spec.SpecRevision,
			})
		}
		return nil, false, err
	}
	p.emit(AuditEvent{
		Kind:                 EventCompilationAccepted,
		ConversationRevision: committed.ConversationRevision,
		SpecRevision:         committed.SpecRevision,
	})
	return committed, true, nil
}

// FreezeOptions describes one execution hand-off request.
type FreezeOptions struct {
	// Intent is the human objective crossing the boundary.
	Intent string
	// Targets is the explicit target set (e.g. resolved by the execution
	// strategy). When empty, the compiled context's active targets are used.
	Targets []string
	// RequireTargets fails closed with ErrUnresolvedExecutionTarget when no
	// target can be resolved (mutation hand-offs).
	RequireTargets bool
	// Declared marks an explicit, human-declared scope boundary ($hot).
	Declared bool
	// ScopeProvenance records how the scope was established (existing Izen
	// vocabulary: "declared" / "dynamic").
	ScopeProvenance string
	// Budget is the descriptive execution budget.
	Budget Budget
}

// FreezeExecution compiles-if-needed, resolves the target geometry, captures
// the CURRENT workspace snapshot and returns the frozen ExecutionSpec contract.
// The ExecutionSpec does not authorize itself.
func (p *Pipeline) FreezeExecution(ctx context.Context, cs ConversationState, opts FreezeOptions) (*ExecutionSpec, error) {
	if p == nil {
		return nil, ErrInvalidContext
	}
	spec, _, err := p.EnsureFresh(ctx, cs)
	if err != nil {
		return nil, err
	}
	targets := dedupeTargets(opts.Targets)
	if len(targets) == 0 {
		targets = spec.ActiveTargets()
	}
	if opts.RequireTargets && len(targets) == 0 {
		return nil, ErrUnresolvedExecutionTarget
	}
	var snap WorkspaceSnapshot
	if p.snapshot != nil {
		snap = p.snapshot.Observe(targets)
	}
	es := &ExecutionSpec{
		Version:              ExecutionSpecVersion,
		Intent:               opts.Intent,
		ContextRevision:      spec.ConversationRevision,
		ConversationRevision: spec.ConversationRevision,
		ContextSpecRevision:  spec.SpecRevision,
		Scope: Scope{
			Targets:    targets,
			Declared:   opts.Declared,
			Provenance: opts.ScopeProvenance,
		},
		TargetHashes:    cloneHashes(snap.Hashes),
		WorkspaceDigest: snap.Digest,
		Constraints:     spec.ActiveConstraints(),
		Decisions:       spec.ActiveDecisions(),
		Budget:          opts.Budget,
		FrozenAt:        p.now(),
	}
	es.SpecID = executionSpecID(es)
	p.emit(AuditEvent{
		Kind:                 EventExecutionContextFrozen,
		ConversationRevision: es.ConversationRevision,
		SpecRevision:         es.ContextSpecRevision,
		ContextRevision:      es.ContextRevision,
		ExecutionSpecID:      es.SpecID,
	})
	return es, nil
}

// ValidateFrozenSnapshot re-observes the workspace and fails closed with
// ErrStaleWorkspaceSnapshot when it diverges from the frozen contract. A
// caller must re-hand-off on failure — never silently refresh and continue.
func (p *Pipeline) ValidateFrozenSnapshot(es *ExecutionSpec) error {
	if es == nil {
		return ErrInvalidContext
	}
	if p == nil || p.snapshot == nil {
		return nil
	}
	now := p.snapshot.Observe(es.Scope.Targets)
	if es.WorkspaceDigest != "" && now.Digest != es.WorkspaceDigest {
		p.emit(AuditEvent{
			Kind:                 EventWorkspaceSnapshotMismatch,
			ConversationRevision: es.ConversationRevision,
			SpecRevision:         es.ContextSpecRevision,
			ContextRevision:      es.ContextRevision,
			ExecutionSpecID:      es.SpecID,
			Detail:               "workspace digest diverged from frozen contract",
		})
		return fmt.Errorf("%w: expected %s, observed %s", ErrStaleWorkspaceSnapshot, short(es.WorkspaceDigest), short(now.Digest))
	}
	for _, target := range sortedTargets(es.Scope.Targets) {
		want, ok := es.TargetHashes[target]
		if !ok {
			continue
		}
		if got := now.Hashes[target]; got != want {
			p.emit(AuditEvent{
				Kind:            EventWorkspaceSnapshotMismatch,
				ExecutionSpecID: es.SpecID,
				Detail:          "target " + target + " diverged",
			})
			return fmt.Errorf("%w: target %q", ErrStaleWorkspaceSnapshot, target)
		}
	}
	return nil
}

// ExecutionPayload renders the bounded execution-facing projection of a frozen
// contract: intent + active semantic state + scope + revisions. Raw
// conversation history is deliberately absent.
func (p *Pipeline) ExecutionPayload(es *ExecutionSpec) string {
	if es == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("[EXECUTION CONTEXT]\n")
	b.WriteString("intent: " + es.Intent + "\n")
	fmt.Fprintf(&b, "conversation_revision: %d\n", es.ConversationRevision)
	fmt.Fprintf(&b, "context_revision: %d\n", es.ContextRevision)
	if len(es.Scope.Targets) > 0 {
		b.WriteString("scope: " + strings.Join(es.Scope.Targets, ", ") + "\n")
	}
	for _, c := range es.Constraints {
		b.WriteString("constraint: " + c.Text + "\n")
	}
	for _, d := range es.Decisions {
		b.WriteString("decision: " + d.Text + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// executionSpecID is the deterministic content address of a frozen contract.
// FrozenAt is deliberately excluded so an identical contract is reproducible.
func executionSpecID(es *ExecutionSpec) string {
	var b strings.Builder
	writeField(&b, "izen-execution-spec-v1")
	writeField(&b, es.Intent)
	writeField(&b, strconv.FormatUint(es.ContextRevision, 10))
	writeField(&b, strconv.FormatUint(es.ContextSpecRevision, 10))
	for _, t := range sortedTargets(es.Scope.Targets) {
		writeField(&b, t)
		writeField(&b, es.TargetHashes[t])
	}
	for _, c := range es.Constraints {
		writeField(&b, c.Text)
	}
	for _, d := range es.Decisions {
		writeField(&b, d.Text)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "exec-" + hex.EncodeToString(sum[:])[:16]
}

func dedupeTargets(targets []string) []string {
	seen := make(map[string]struct{}, len(targets))
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func cloneHashes(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "(empty)"
	}
	return s
}

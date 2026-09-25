// Package contextpipeline is the composition-boundary adapter for the Context
// Domain. It is the ONLY package that reaches both the context specification
// (internal/contextspec) and the execution authority (internal/execution): the
// OCC snapshot port reads the existing workspace hashing mechanism, and the
// audit sink projects context events onto the shared domain bus.
//
// It adds no execution authority. It only observes workspace state and emits
// content-free telemetry.
package contextpipeline

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/contextspec"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// OCCSnapshotPort adapts the RuntimeExecutor's existing OCC verifier into the
// Context Domain's SnapshotPort. It never writes and never authorizes.
type OCCSnapshotPort struct {
	occ *execution.OCCVerifier
}

// NewOCCSnapshotPort binds the port to the executor's OCC verifier.
func NewOCCSnapshotPort(occ *execution.OCCVerifier) *OCCSnapshotPort {
	return &OCCSnapshotPort{occ: occ}
}

// Observe returns the current per-target hashes and aggregate digest for the
// declared targets, reusing the canonical OCC hashing (no parallel hasher).
func (p *OCCSnapshotPort) Observe(targets []string) contextspec.WorkspaceSnapshot {
	snapshot := contextspec.WorkspaceSnapshot{Targets: append([]string(nil), targets...)}
	if p == nil || p.occ == nil {
		return snapshot
	}
	snapshot.Hashes = p.occ.TargetHashes(targets)
	snapshot.Digest = p.occ.TreeDigest(targets)
	return snapshot
}

// BusAuditSink projects Context Pipeline audit events onto the shared event bus
// as content-free activity events (revisions and identities only).
type BusAuditSink struct {
	bus *events.Bus
}

// NewBusAuditSink binds the sink to the shared domain bus.
func NewBusAuditSink(bus *events.Bus) *BusAuditSink {
	return &BusAuditSink{bus: bus}
}

// EmitContext implements contextspec.AuditSink.
func (s *BusAuditSink) EmitContext(ev contextspec.AuditEvent) {
	if s == nil || s.bus == nil {
		return
	}
	line := fmt.Sprintf("[context] %s conv_rev=%d spec_rev=%d ctx_rev=%d",
		ev.Kind, ev.ConversationRevision, ev.SpecRevision, ev.ContextRevision)
	if ev.ExecutionSpecID != "" {
		line += " exec=" + ev.ExecutionSpecID
	}
	if ev.Detail != "" {
		line += " " + ev.Detail
	}
	s.bus.Publish(events.NewActivity(line))
}

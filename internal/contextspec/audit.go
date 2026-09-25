package contextspec

// Canonical Context Pipeline audit event kinds. They reuse the Izen audit
// vocabulary style (dotted, stage-qualified) and carry no raw conversation and
// no file contents.
const (
	// EventCompilationStarted marks the start of a lazy compilation.
	EventCompilationStarted = "context.compilation.started"
	// EventCompilationAccepted marks a candidate committed by the Control Plane.
	EventCompilationAccepted = "context.compilation.accepted"
	// EventCompilationDiscardedStale marks a CAS rejection.
	EventCompilationDiscardedStale = "context.compilation.discarded_stale"
	// EventExecutionContextFrozen marks the freezing of an ExecutionSpec.
	EventExecutionContextFrozen = "execution.context.frozen"
	// EventWorkspaceSnapshotMismatch marks a stale workspace refusal.
	EventWorkspaceSnapshotMismatch = "execution.workspace.snapshot_mismatch"
)

// AuditEvent is a content-free Context Pipeline observation. It records
// revisions and identities only — never raw user conversation or file bytes.
type AuditEvent struct {
	Kind                 string
	ConversationRevision uint64
	SpecRevision         uint64
	ContextRevision      uint64
	ExecutionSpecID      string
	Detail               string
}

// AuditSink receives Context Pipeline audit events. It is a projection port:
// the Context Domain never logs directly.
type AuditSink interface {
	EmitContext(AuditEvent)
}

// nopAudit is the safe default when no sink is wired.
type nopAudit struct{}

func (nopAudit) EmitContext(AuditEvent) {}

func (p *Pipeline) emit(ev AuditEvent) {
	if p == nil || p.audit == nil {
		return
	}
	p.audit.EmitContext(ev)
}

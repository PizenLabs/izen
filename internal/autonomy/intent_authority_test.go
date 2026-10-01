package autonomy

import (
	"errors"
	"testing"
)

// ── PHASE 14: CANONICAL INTENT AUTHORITY ────────────────────────────────────
//
// The failure this suite pins is a SPLIT-BRAIN lifecycle: preflight classifies
// the objective as read-only, the context compiler compiles a read-only payload,
// and autonomy then elevates the objective to a mutation. The model then judges
// a workspace it was never shown, and no component can say which contract the
// run is really under.
//
// The repair is a BLOCKING REVISION with three mandatory steps. Each test here
// isolates one of them, and the last test walks the whole transaction.

// TestIntentAuthority_FirstResolutionIsAuthoritative pins that the preflight
// classification owns the first resolution. A second, un-revised Resolve call
// must NOT overwrite it — otherwise any component could quietly re-classify the
// objective and the canonical intent would be whatever the last caller believed.
func TestIntentAuthority_FirstResolutionIsAuthoritative(t *testing.T) {
	a := NewIntentAuthority()
	if got := a.Resolve(IntentInvestigation); got != IntentInvestigation {
		t.Fatalf("first resolve = %s, want %s", got, IntentInvestigation)
	}
	if got := a.Resolve(IntentModification); got != IntentInvestigation {
		t.Fatalf("second resolve = %s, want the first classification to stand", got)
	}
	current, err := a.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if current != IntentInvestigation {
		t.Fatalf("canonical intent = %s, want %s", current, IntentInvestigation)
	}
	if a.Phase() != IntentResolved {
		t.Fatalf("phase = %s, want %s", a.Phase(), IntentResolved)
	}
}

// TestIntentAuthority_BlockingRevisionInvalidatesEverythingDerived is the core
// of INVARIANT 5 step 1. Elevating the intent must invalidate the previous one
// AND every artefact derived from it, and Current() must FAIL CLOSED while the
// transaction is open so no concurrent component can read the stale intent.
func TestIntentAuthority_BlockingRevisionInvalidatesEverythingDerived(t *testing.T) {
	a := NewIntentAuthority()
	a.Resolve(IntentInvestigation)
	if err := a.CommitContext(IntentInvestigation, true); err != nil {
		t.Fatalf("CommitContext: %v", err)
	}
	if !a.Ready() {
		t.Fatal("a resolved intent with a valid context must be ready")
	}
	before := a.Revision()

	rev, err := a.Elevate(IntentModification, "gateway dispatched a mutation contract")
	if err != nil {
		t.Fatalf("Elevate: %v", err)
	}
	if rev.From != IntentInvestigation || rev.To != IntentModification {
		t.Fatalf("revision = %s -> %s, want investigation -> modification", rev.From, rev.To)
	}
	if rev.Revision != before+1 {
		t.Fatalf("revision counter = %d, want %d", rev.Revision, before+1)
	}
	if !rev.ContextInvalidated {
		t.Fatal("a revision that invalidated nothing was not a revision")
	}
	// The transaction is OPEN: the canonical intent is synchronized, but nothing
	// may proceed on it yet.
	if a.Phase() != IntentRevising {
		t.Fatalf("phase = %s, want %s", a.Phase(), IntentRevising)
	}
	if a.Ready() {
		t.Fatal("a half-revised intent must not be ready")
	}
	if a.ContextValid() {
		t.Fatal("the pre-revision context must be invalid under the new intent")
	}
	// Step 2: the canonical intent is synchronized. While the transaction is
	// open Current() FAILS CLOSED rather than handing out a value, so no
	// concurrent component can read a half-revised intent.
	if _, err := a.Current(); !errors.Is(err, ErrIntentRevisionIncomplete) {
		t.Fatalf("Current during revision err = %v, want ErrIntentRevisionIncomplete", err)
	}
	if a.LastRevision().To != IntentModification {
		t.Fatalf("synchronized intent = %s, want %s", a.LastRevision().To, IntentModification)
	}
	if a.Revision() != before+1 {
		t.Fatalf("revision counter = %d, want %d", a.Revision(), before+1)
	}
	// Closing the transaction exposes the synchronized intent.
	if err := a.CommitContext(IntentModification, true); err != nil {
		t.Fatalf("CommitContext: %v", err)
	}
	current, err := a.Current()
	if err != nil {
		t.Fatalf("Current after commit: %v", err)
	}
	if current != IntentModification {
		t.Fatalf("canonical intent = %s, want %s", current, IntentModification)
	}
}

// TestIntentAuthority_ContextReCompilationGatesReadiness is INVARIANT 5 step 2:
// the context must be re-compiled UNDER THE MODIFICATION CONTRACT before the run
// may dispatch. Committing a context compiled for the previous intent fails.
func TestIntentAuthority_ContextReCompilationGatesReadiness(t *testing.T) {
	a := NewIntentAuthority()
	a.Resolve(IntentInvestigation)
	if _, err := a.Elevate(IntentModification, "elevation"); err != nil {
		t.Fatalf("Elevate: %v", err)
	}
	// A context compiled for the OLD intent cannot be committed.
	if err := a.CommitContext(IntentInvestigation, true); err == nil {
		t.Fatal("a context compiled under the previous intent was accepted")
	}
	if a.Ready() {
		t.Fatal("the run must not be ready on a stale-intent context")
	}
	// The re-compiled context closes the transaction.
	if err := a.CommitContext(IntentModification, true); err != nil {
		t.Fatalf("CommitContext(modification): %v", err)
	}
	if !a.Ready() {
		t.Fatal("a re-compiled, valid context must close the revision")
	}
	if a.Phase() != IntentResolved {
		t.Fatalf("phase = %s, want %s", a.Phase(), IntentResolved)
	}
}

// TestIntentAuthority_InvalidProvenanceKeepsTheRevisionOpen pins the fail-closed
// direction: a re-compilation that does not satisfy its provenance contract
// leaves the transaction OPEN, so the run parks instead of dispatching a
// mutation over an empty context.
func TestIntentAuthority_InvalidProvenanceKeepsTheRevisionOpen(t *testing.T) {
	a := NewIntentAuthority()
	a.Resolve(IntentInvestigation)
	if _, err := a.Elevate(IntentModification, "elevation"); err != nil {
		t.Fatalf("Elevate: %v", err)
	}
	if err := a.CommitContext(IntentModification, false); err == nil {
		t.Fatal("a context failing its provenance contract was accepted")
	}
	if a.Ready() {
		t.Fatal("the run must not be ready on a context that fails provenance")
	}
	if a.Phase() != IntentRevising {
		t.Fatalf("phase = %s, want %s (the revision stays open)", a.Phase(), IntentRevising)
	}
}

// TestIntentAuthority_DowngradeIsRejected pins that authority, once held, is
// not withdrawn inside a lifecycle. A later classifier reading must not be able
// to strip a mutation contract from a run that is already executing under one —
// only a FRESH lifecycle can.
func TestIntentAuthority_DowngradeIsRejected(t *testing.T) {
	a := NewIntentAuthority()
	a.Resolve(IntentModification)
	if _, err := a.Elevate(IntentInvestigation, "downgrade attempt"); !errors.Is(err, ErrIntentDowngradeRejected) {
		t.Fatalf("err = %v, want ErrIntentDowngradeRejected", err)
	}
	current, _ := a.Current()
	if current != IntentModification {
		t.Fatalf("canonical intent = %s, want the mutation contract to stand", current)
	}
}

// TestIntentAuthority_ConcurrentReadFailsClosed pins the concurrency contract: a
// reader that arrives while the revision transaction is open is told so rather
// than handed the invalidated value. In the driver's terms, Preflight, the
// Context Compiler and the Driver can never hold conflicting intent states.
func TestIntentAuthority_ConcurrentReadFailsClosed(t *testing.T) {
	a := NewIntentAuthority()
	a.Resolve(IntentInvestigation)
	// Open a revision and hold it open.
	if _, err := a.Elevate(IntentModification, "elevation"); err != nil {
		t.Fatalf("Elevate: %v", err)
	}
	if _, err := a.Elevate(IntentRefactoring, "concurrent second elevation"); !errors.Is(err, ErrIntentRevisionIncomplete) {
		t.Fatalf("second Elevate err = %v, want ErrIntentRevisionIncomplete", err)
	}
	// Close it, then the concurrent reader succeeds against the NEW intent.
	if err := a.CommitContext(IntentModification, true); err != nil {
		t.Fatalf("CommitContext: %v", err)
	}
	current, err := a.Current()
	if err != nil {
		t.Fatalf("Current after commit: %v", err)
	}
	if current != IntentModification {
		t.Fatalf("canonical intent = %s, want %s", current, IntentModification)
	}
}

// TestIntentAuthority_ElevationToTheSameIntentIsANoOp pins that a no-op
// elevation does not invalidate a perfectly valid context. A "revision" that
// changes nothing would otherwise throw away the compiled payload for no
// reason, which is its own kind of false negative.
func TestIntentAuthority_ElevationToTheSameIntentIsANoOp(t *testing.T) {
	a := NewIntentAuthority()
	a.Resolve(IntentModification)
	if err := a.CommitContext(IntentModification, true); err != nil {
		t.Fatalf("CommitContext: %v", err)
	}
	rev, err := a.Elevate(IntentModification, "same intent")
	if err != nil {
		t.Fatalf("Elevate: %v", err)
	}
	if rev.ContextInvalidated {
		t.Fatal("a no-op elevation invalidated the compiled context")
	}
	if !a.Ready() {
		t.Fatal("a no-op elevation must leave the run ready")
	}
	if a.Revision() != 0 {
		t.Fatalf("revision counter = %d, want 0 for a no-op elevation", a.Revision())
	}
}

// TestIntentAuthority_InvalidateContextOnWorkspaceDrift pins the
// Boundary-5 path: when the workspace moves under a still-valid intent, the
// context is dropped and the run must re-compile before dispatching again.
func TestIntentAuthority_InvalidateContextOnWorkspaceDrift(t *testing.T) {
	a := NewIntentAuthority()
	a.Resolve(IntentModification)
	if err := a.CommitContext(IntentModification, true); err != nil {
		t.Fatalf("CommitContext: %v", err)
	}
	if !a.Ready() {
		t.Fatal("precondition: the run must be ready")
	}
	a.InvalidateContext()
	if a.Ready() || a.ContextValid() {
		t.Fatal("the run must not be ready after a workspace drift invalidated the context")
	}
	// The intent itself is unchanged — only the context is gone. A reader
	// therefore fails closed rather than seeing a different intent.
	if a.Phase() != IntentRevising {
		t.Fatalf("phase = %s, want %s", a.Phase(), IntentRevising)
	}
	if _, err := a.Current(); !errors.Is(err, ErrIntentRevisionIncomplete) {
		t.Fatalf("Current after invalidation err = %v, want ErrIntentRevisionIncomplete", err)
	}
	if err := a.CommitContext(IntentModification, true); err != nil {
		t.Fatalf("re-CommitContext: %v", err)
	}
	if !a.Ready() {
		t.Fatal("re-compiling under the SAME intent must restore readiness")
	}
}

// TestIntentAuthority_NilAuthorityFailsClosed pins that the zero-value
// authority is safe, not silently permissive.
func TestIntentAuthority_NilAuthorityFailsClosed(t *testing.T) {
	var a *IntentAuthority
	if a.Ready() {
		t.Fatal("a nil authority must never be ready")
	}
	if _, err := a.Elevate(IntentModification, "x"); err == nil {
		t.Fatal("a nil authority must refuse an elevation")
	}
	if got, err := a.Current(); err != nil || got != IntentUnknown {
		t.Fatalf("Current = (%s, %v), want (unknown, nil)", got, err)
	}
}

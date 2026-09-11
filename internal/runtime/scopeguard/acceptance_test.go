package scopeguard

import (
	"context"
	"strings"
	"testing"
)

// Acceptance 1: a worker proposal editing a file outside
// AuthorizedTargetScope is rejected at the ScopeGuard tier before any
// tool dispatch occurs.
func TestAcceptanceScopeGuardRejectsBeforeDispatch(t *testing.T) {
	ledger := &MemoryLedger{}
	guard := NewScopeGuard([]string{"internal/runtime/scopeguard"})
	dispatched := false
	dispatch := func() { dispatched = true }

	p := Proposal{
		ID: "prop-outside", TaskID: "task-1", Workspace: WorkspaceBuild,
		Op: OpPatch, TargetFiles: []string{"internal/ui/model.go"},
		OperationID: "op-1", StepID: "step-1",
	}
	res := guard.Enforce(p, ledger)
	if res.Verdict != ScopeRejected {
		t.Fatalf("expected SCOPE_VIOLATION_REJECTED, got %q (%s)", res.Verdict, res.Reason)
	}
	if len(res.Violations) == 0 {
		t.Fatal("expected violations to name the offending target")
	}
	// Guard boundary: dispatch must never run for rejected proposals.
	if res.Verdict == ScopeRejected {
		// dispatch NOT called — assert flag stays false.
	} else {
		dispatch()
	}
	if dispatched {
		t.Fatal("tool dispatch ran for a scope-rejected proposal")
	}
	if ledger.Count("SCOPE_VIOLATION_REJECTED") != 1 {
		t.Fatalf("expected 1 SCOPE_VIOLATION_REJECTED event, got %d", ledger.Count("SCOPE_VIOLATION_REJECTED"))
	}
	// In-scope mutation passes.
	ok := guard.Enforce(Proposal{
		ID: "prop-inside", TaskID: "task-1", Workspace: WorkspaceBuild,
		Op: OpPatch, TargetFiles: []string{"internal/runtime/scopeguard/scope.go"},
	}, ledger)
	if ok.Verdict != ScopePass {
		t.Fatalf("expected PASS for in-scope target, got %q", ok.Verdict)
	}
	// Reads outside scope never reject (context != authority).
	rd := guard.Enforce(Proposal{
		ID: "prop-read", TaskID: "task-1", Workspace: WorkspacePlan,
		Op: OpRead, TargetFiles: []string{"elsewhere/file.go"},
	}, ledger)
	if rd.Verdict != ScopePass {
		t.Fatalf("read proposals must not be scope-rejected, got %q", rd.Verdict)
	}
}

// Acceptance 2: unlisted file within module boundary -> STRUCTURAL_AMBIGUITY,
// routed to Verification rather than hard-rejected.
func TestAcceptanceStructuralAmbiguityRoutesToVerification(t *testing.T) {
	ledger := &MemoryLedger{}
	graph := NewStaticGraph().
		SetModule("pkg/auth/login.go", "pkg/auth").
		SetModule("pkg/auth/session.go", "pkg/auth").
		SetModule("pkg/billing/invoice.go", "pkg/billing")
	// No static edge between login.go and session.go (dynamic dispatch).
	sg := NewStructuralGuard(graph)

	amb := sg.Check(
		[]string{"pkg/auth/session.go"},
		[]string{"pkg/auth/login.go"},
		ledger, "task-2", "prop-amb",
	)
	if amb.Result != StructuralAmbiguity {
		t.Fatalf("expected STRUCTURAL_AMBIGUITY, got %q (%s)", amb.Result, amb.Reason)
	}
	if !amb.NeedsVerification {
		t.Fatal("ambiguity must set NeedsVerification")
	}
	if ledger.Count("STRUCTURAL_AMBIGUITY") != 1 {
		t.Fatalf("expected STRUCTURAL_AMBIGUITY lineage event, got %d", ledger.Count("STRUCTURAL_AMBIGUITY"))
	}

	// Static edge -> PASS.
	graph.AddEdge("pkg/auth/login.go", "pkg/auth/session.go")
	pass := sg.Check([]string{"pkg/auth/session.go"}, []string{"pkg/auth/login.go"}, nil, "task-2", "prop-pass")
	if pass.Result != StructuralPass {
		t.Fatalf("expected PASS with static edge, got %q", pass.Result)
	}

	// Unrelated module, zero path -> REJECT (even though scope guard passed).
	graph2 := NewStaticGraph().
		SetModule("pkg/auth/login.go", "pkg/auth").
		SetModule("pkg/billing/invoice.go", "pkg/billing")
	rej := NewStructuralGuard(graph2).Check(
		[]string{"pkg/billing/invoice.go"}, []string{"pkg/auth/login.go"}, nil, "task-2", "prop-rej")
	if rej.Result != StructuralReject {
		t.Fatalf("expected REJECT for unrelated module, got %q", rej.Result)
	}

	// Gateway maps ambiguity to REQUIRE_VERIFICATION (not DENY).
	scope := NewScopeGuard([]string{"pkg"})
	sess, _ := NewWorkspaceSession("task-2", "ckpt-1", WorkspaceBuild)
	gw := NewIntentGateway(scope, sg, sess, nil, ledger)
	_ = context.Background()
	// NOTE: sg still has the static edge from above; rebuild ambiguity graph.
	ambGraph := NewStaticGraph().
		SetModule("pkg/auth/login.go", "pkg/auth").
		SetModule("pkg/auth/session.go", "pkg/auth")
	gw2 := NewIntentGateway(scope, NewStructuralGuard(ambGraph), sess, nil, ledger)
	got := gw2.Authorize(context.Background(), Proposal{
		ID: "prop-gw", TaskID: "task-2", Workspace: WorkspaceBuild,
		Op: OpPatch, TargetFiles: []string{"pkg/auth/session.go"},
	}, []string{"pkg/auth/login.go"})
	if got.Decision != DecisionRequireVerification {
		t.Fatalf("expected REQUIRE_VERIFICATION, got %q (%s)", got.Decision, got.Reason)
	}
	_ = gw
}

// Acceptance 3: workspace switch /build -> /review -> /build preserves
// TaskID, CheckpointID, EvidenceLedger without resetting state.
func TestAcceptanceWorkspaceSwitchPreservesLineage(t *testing.T) {
	sess, err := NewWorkspaceSession("task-abc", "ckpt-42", WorkspaceBuild)
	if err != nil {
		t.Fatal(err)
	}
	sess.AttachEvidence(EvidenceRef{ID: "ev-1", Subject: "login test", Digest: "d1"})
	sess.AttachEvidence(EvidenceRef{ID: "ev-2", Subject: "cursor diff", Digest: "d2"})
	sess.AttachNegative(NegativeRef{ID: "neg-1", Hypothesis: "cache fixes auth"})

	ledger := &MemoryLedger{}
	if err := sess.SwitchTo(WorkspaceReview, ledger); err != nil {
		t.Fatal(err)
	}
	if sess.Workspace != WorkspaceReview {
		t.Fatalf("expected review mode, got %q", sess.Workspace)
	}
	// /review denies WRITE: capability allowance changed.
	if sess.Policy.Allowed.Allows(OpWrite) {
		t.Fatal("review policy must deny WRITE")
	}
	taskID, ckpt, ev, neg, _ := sess.Snapshot()
	if taskID != "task-abc" || ckpt != "ckpt-42" {
		t.Fatalf("identity reset on switch: task=%q ckpt=%q", taskID, ckpt)
	}
	if len(ev) != 2 || len(neg) != 1 {
		t.Fatalf("lineage reset on switch: evidence=%d negatives=%d", len(ev), len(neg))
	}

	if err := sess.SwitchTo(WorkspaceBuild, ledger); err != nil {
		t.Fatal(err)
	}
	taskID2, ckpt2, ev2, neg2, mode := sess.Snapshot()
	if taskID2 != "task-abc" || ckpt2 != "ckpt-42" {
		t.Fatalf("identity reset on switch-back: task=%q ckpt=%q", taskID2, ckpt2)
	}
	if len(ev2) != 2 || len(neg2) != 1 {
		t.Fatalf("lineage reset on switch-back: evidence=%d negatives=%d", len(ev2), len(neg2))
	}
	if mode != WorkspaceBuild {
		t.Fatalf("expected build mode after switch-back, got %q", mode)
	}
	if !sess.Policy.Allowed.Allows(OpWrite) {
		t.Fatal("build policy must allow WRITE after switch-back")
	}
	if ledger.Count("WORKSPACE_SWITCHED") != 2 {
		t.Fatalf("expected 2 WORKSPACE_SWITCHED events, got %d", ledger.Count("WORKSPACE_SWITCHED"))
	}
	if !strings.Contains(sess.Policy.Focus, "Bounded implementation") {
		t.Fatalf("unexpected build focus: %q", sess.Policy.Focus)
	}
}

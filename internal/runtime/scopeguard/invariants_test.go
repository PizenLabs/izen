package scopeguard

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/internal/runtime/adaptive"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/ephemeral"
)

// Acceptance 4: end-to-end compliance across all 6 Blocking Architectural
// Invariants. Each invariant maps to a deterministic assertion against the
// real durable/ephemeral/adaptive substrate plus the Phase 4 pipeline.
func TestInvariantsEndToEnd(t *testing.T) {
	ctx := context.Background()

	// ── Invariant 1: Worker failure != Task failure ──
	// A classified worker failure interrupts the task; the task survives
	// with identity intact and resumes via handoff.
	t.Run("Invariant1_WorkerFailureIsNotTaskFailure", func(t *testing.T) {
		dir := t.TempDir()
		store := durable.NewTaskStore(dir)
		if err := store.Open(); err != nil {
			t.Fatal(err)
		}
		st, err := store.CreateTask("task-i1", "fix auth", []string{"pkg/auth"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.RecordFailure(st.ID, "PROVIDER_TIMEOUT", "op-1", "llm timeout"); err != nil {
			t.Fatal(err)
		}
		got, ok := store.State(st.ID)
		if !ok {
			t.Fatal("task vanished after worker failure")
		}
		if got.ID != st.ID {
			t.Fatalf("task identity rewritten: %q -> %q", st.ID, got.ID)
		}
		if err := store.RecordHandoff(st.ID, "worker-a", "worker-b", "ckpt-1", "failover"); err != nil {
			t.Fatal(err)
		}
		got2, _ := store.State(st.ID)
		if got2.ID != st.ID || got2.LastCheckpointID != "ckpt-1" {
			t.Fatalf("handoff rewrote identity: %+v", got2)
		}
	})

	// ── Invariant 2: Resume != Transcript replay ──
	// Handoff uses ResumeContract + TaskCapsule only: no transcripts.
	t.Run("Invariant2_ResumeUsesCapsuleOnly", func(t *testing.T) {
		src := ephemeral.CapsuleSource{
			State:   durable.TaskState{ID: "task-i2", Intent: "fix auth", ActiveTargetScope: []string{"pkg/auth"}},
			Current: ephemeral.StepDefinition{ID: "step-1", Goal: "patch login"},
			Budget:  ephemeral.BudgetState{RecoveryAttemptsLeft: 2, MaxRecoveryAttempts: 3},
		}
		capsule := ephemeral.DeriveCapsule(src, 5)
		contract, err := ephemeral.BuildResumeContract(capsule, "ckpt-9", ephemeral.FailureProviderUnavailable, []string{"read", "patch"})
		if err != nil {
			t.Fatal(err)
		}
		if contract.Capsule.TaskID != "task-i2" || contract.CheckpointID != "ckpt-9" {
			t.Fatalf("contract lost identity: %+v", contract)
		}
		if contract.Capsule.CurrentStep.ID != "step-1" {
			t.Fatal("contract lost current step")
		}
	})

	// ── Invariant 3: Cache miss != State loss ──
	// Snapshot is derived: deleting snapshot.json loses nothing; replay
	// from ledger.ndjson rebuilds canonical state.
	t.Run("Invariant3_CacheMissIsNotStateLoss", func(t *testing.T) {
		dir := t.TempDir()
		store := durable.NewTaskStore(dir)
		if err := store.Open(); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateTask("task-i3", "fix auth", []string{"pkg/auth"}); err != nil {
			t.Fatal(err)
		}
		if err := store.Checkpoint("task-i3", "ckpt-1"); err != nil {
			t.Fatal(err)
		}
		if err := store.RebuildSnapshot(); err != nil {
			t.Fatal(err)
		}
		got, ok := store.State("task-i3")
		if !ok || got.LastCheckpointID != "ckpt-1" {
			t.Fatalf("snapshot rebuild lost state: %+v ok=%v", got, ok)
		}
	})

	// ── Invariant 4: Context expansion != Authority expansion ──
	// Ladder growth never widens write authority: a /plan read-tier
	// expansion still denies WRITE at the gateway.
	t.Run("Invariant4_ContextExpansionIsNotAuthorityExpansion", func(t *testing.T) {
		planner := adaptive.NewContextPlanner()
		dec := adaptive.PressureDecision{Expand: true, Signal: "missing-symbol", Reason: "evidence pressure"}
		if _, expanded, _ := planner.RequestExpansion("task-i4", dec); !expanded {
			t.Fatal("expected tier expansion on evidence pressure")
		}
		sess, _ := NewWorkspaceSession("task-i4", "ckpt-1", WorkspacePlan)
		gw := NewIntentGateway(NewScopeGuard([]string{"pkg/auth"}), nil, sess, nil, nil)
		got := gw.Authorize(ctx, Proposal{
			ID: "p", TaskID: "task-i4", Workspace: WorkspacePlan,
			Op: OpWrite, TargetFiles: []string{"pkg/auth/login.go"},
		}, []string{"pkg/auth/login.go"})
		if got.Decision != DecisionDeny {
			t.Fatalf("context expansion granted write authority: %q", got.Decision)
		}
	})

	// ── Invariant 5: Workspace switch != Task reset ──
	t.Run("Invariant5_WorkspaceSwitchIsNotTaskReset", func(t *testing.T) {
		sess, _ := NewWorkspaceSession("task-i5", "ckpt-7", WorkspaceBuild)
		sess.AttachEvidence(EvidenceRef{ID: "ev-1", Subject: "diff"})
		if err := sess.SwitchTo(WorkspaceInvestigate, nil); err != nil {
			t.Fatal(err)
		}
		if err := sess.SwitchTo(WorkspaceReview, nil); err != nil {
			t.Fatal(err)
		}
		if err := sess.SwitchTo(WorkspaceBuild, nil); err != nil {
			t.Fatal(err)
		}
		taskID, ckpt, ev, _, _ := sess.Snapshot()
		if taskID != "task-i5" || ckpt != "ckpt-7" || len(ev) != 1 {
			t.Fatalf("workspace switches reset task: %q %q evidence=%d", taskID, ckpt, len(ev))
		}
	})

	// ── Invariant 6: Missing response != Missing side effect ──
	// ExecutionCursor + TreeDigest reconciliation: a committed side
	// effect whose response was lost is NOT re-executed on retry.
	t.Run("Invariant6_MissingResponseIsNotMissingSideEffect", func(t *testing.T) {
		dir := t.TempDir()
		store := durable.NewTaskStore(dir)
		if err := store.Open(); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateTask("task-i6", "fix auth", []string{"pkg/auth"}); err != nil {
			t.Fatal(err)
		}
		exec := NewRuntimeExecutor(store)
		p := Proposal{
			ID: "p-i6", TaskID: "task-i6", Workspace: WorkspaceBuild,
			Op: OpWrite, TargetFiles: []string{"pkg/auth/login.go"},
			OperationID: "op-i6", StepID: "step-1",
		}
		calls := 0
		effect := func(ctx context.Context) (string, error) {
			calls++
			return "post-digest", nil // side effect committed; response "lost"
		}
		// First attempt: precondition holds, effect runs once.
		if _, err := exec.Execute(ctx, p, "pre-digest", "pre-digest", effect, nil); err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("expected 1 effect call, got %d", calls)
		}
		// Retry after "lost response": the worktree already reflects the
		// postcondition digest, so the cursor reconciles to
		// ALREADY_COMMITTED without re-executing.
		res, err := exec.Execute(ctx, p, "pre-digest", "", effect, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Executed {
			t.Fatal("re-executed an already-committed side effect")
		}
		if calls != 1 {
			t.Fatalf("effect re-invoked on retry: calls=%d", calls)
		}
	})
}

// Full pipeline integration: out-of-scope proposals never reach the
// executor effect; ambiguous proposals require verification first.
func TestPipelineIntegration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := durable.NewTaskStore(dir)
	if err := store.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask("task-pipe", "fix auth", []string{"pkg/auth"}); err != nil {
		t.Fatal(err)
	}
	ledger := StoreLedger{Store: store}
	scope := NewScopeGuard([]string{"pkg/auth"})
	graph := NewStaticGraph().
		SetModule("pkg/auth/login.go", "pkg/auth").
		SetModule("pkg/auth/session.go", "pkg/auth")
	sess, _ := NewWorkspaceSession("task-pipe", "ckpt-1", WorkspaceBuild)
	gw := NewIntentGateway(scope, NewStructuralGuard(graph), sess, NewSimpleBudget(10, 1000, 100000), ledger)
	pipe := NewPipeline(gw, NewRuntimeExecutor(store))

	// 1. Out-of-scope: denied, effect never runs.
	ran := false
	_, _, err := pipe.RunProposal(ctx, Proposal{
		ID: "p-out", TaskID: "task-pipe", Workspace: WorkspaceBuild,
		Op: OpPatch, TargetFiles: []string{"pkg/billing/invoice.go"},
		OperationID: "op-out", StepID: "s1",
	}, []string{"pkg/auth/login.go"}, "pre", "pre",
		func(ctx context.Context) (string, error) { ran = true; return "x", nil }, nil, false)
	if err == nil {
		t.Fatal("expected denial for out-of-scope proposal")
	}
	if ran {
		t.Fatal("executor effect ran for out-of-scope proposal")
	}

	// 2. Ambiguous: blocked until verified, then executes.
	_, _, err = pipe.RunProposal(ctx, Proposal{
		ID: "p-amb", TaskID: "task-pipe", Workspace: WorkspaceBuild,
		Op: OpPatch, TargetFiles: []string{"pkg/auth/session.go"},
		OperationID: "op-amb", StepID: "s1",
	}, []string{"pkg/auth/login.go"}, "pre", "pre",
		func(ctx context.Context) (string, error) { return "post", nil },
		func(ctx context.Context) (bool, string, error) { return true, "lint clean", nil },
		false)
	if err == nil {
		t.Fatal("expected verification gate for ambiguous proposal")
	}
	gwRes, execRes, err := pipe.RunProposal(ctx, Proposal{
		ID: "p-amb", TaskID: "task-pipe", Workspace: WorkspaceBuild,
		Op: OpPatch, TargetFiles: []string{"pkg/auth/session.go"},
		OperationID: "op-amb", StepID: "s1",
	}, []string{"pkg/auth/login.go"}, "pre", "pre",
		func(ctx context.Context) (string, error) { return "post", nil },
		func(ctx context.Context) (bool, string, error) { return true, "lint clean", nil },
		true)
	if err != nil {
		t.Fatalf("verified ambiguity should execute: %v", err)
	}
	if gwRes.Decision != DecisionAllow || !execRes.Executed || !execRes.Verified {
		t.Fatalf("unexpected pipeline outcome: %+v %+v", gwRes, execRes)
	}
}

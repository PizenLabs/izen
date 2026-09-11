package runtime_test

// Master engine integration suite: end-to-end proposal lifecycle across the
// real durable / ephemeral / adaptive / scopeguard implementations.
//
// ZERO-MOCK: no mock interfaces for inter-package communication. Every
// state transition passes through the real TaskStore (ledger.ndjson on a
// temp workdir), the real RecoveryEngine, the real ContextPlanner and the
// real IntentGateway/RuntimeExecutor. Effects perform real file writes and
// digests come from durable.ComputeTreeDigest.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	izenruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/adaptive"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/ephemeral"
	"github.com/PizenLabs/izen/internal/runtime/scopeguard"
)

const engineTaskID = "task-engine-1"

var engineScope = []string{"pkg/auth"}

// engineFixture wires a real RuntimeEngine on an isolated temp workdir with
// pkg/auth/login.go present. It returns the engine and the workdir.
func engineFixture(t *testing.T, ws scopeguard.Workspace, graph scopeguard.GraphProvider, workers []ephemeral.WorkerDescriptor) (*izenruntime.RuntimeEngine, string) {
	t.Helper()
	dir := t.TempDir()
	seed := filepath.Join(dir, "pkg", "auth", "login.go")
	if err := os.MkdirAll(filepath.Dir(seed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seed, []byte("package auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	router := ephemeral.NewWorkerRouter(workers)
	eng, err := izenruntime.NewRuntimeEngine(dir, engineTaskID, engineScope, ws,
		scopeguard.NewSimpleBudget(10, 1000, 100000), router, graph)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Store().CreateTask(engineTaskID, "fix auth", engineScope); err != nil {
		t.Fatal(err)
	}
	return eng, dir
}

func defaultWorkers() []ephemeral.WorkerDescriptor {
	return []ephemeral.WorkerDescriptor{
		{ID: "worker-b", Provider: "openai", ContextTokens: 128000, Healthy: true},
	}
}

// engineWriteEffect returns a REAL side effect: it writes content to a
// workdir-relative file and reports the live canonical digest.
func engineWriteEffect(t *testing.T, workDir, rel, content string) scopeguard.Effect {
	t.Helper()
	return func(_ context.Context) (string, error) {
		p := filepath.Join(workDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return "", err
		}
		return durable.ComputeTreeDigest(workDir, rel)
	}
}

func okVerifier(_ context.Context) (bool, string, error) { return true, "lint clean", nil }

func failingVerifier(_ context.Context) (bool, string, error) {
	return false, "tests red", nil
}

// engineLedger asserts the task ledger contains every listed event type.
func engineLedger(t *testing.T, eng *izenruntime.RuntimeEngine, types ...string) string {
	t.Helper()
	raw, err := os.ReadFile(eng.Store().LedgerPath())
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, typ := range types {
		if !strings.Contains(body, typ) {
			t.Fatalf("ledger missing %s:\n%s", typ, body)
		}
	}
	return body
}

func engineProposal(op scopeguard.OpClass, targets []string, opID, stepID, detail string) scopeguard.Proposal {
	return scopeguard.Proposal{
		ID: "p-" + opID, TaskID: engineTaskID, Workspace: scopeguard.WorkspaceBuild,
		Op: op, TargetFiles: targets, OperationID: opID, StepID: stepID, Detail: detail,
	}
}

// Intake -> Guard -> Execution: an in-scope listed proposal flows through
// the real gateway and executor, persisting every hop.
func TestEngineProposalLifecycle(t *testing.T) {
	ctx := context.Background()
	eng, dir := engineFixture(t, scopeguard.WorkspaceBuild, nil, defaultWorkers())

	st, err := eng.ExecuteProposal(ctx,
		engineProposal(scopeguard.OpWrite, []string{"pkg/auth/login.go"}, "op-1", "step-1", "patch login"),
		[]string{"pkg/auth/login.go"},
		engineWriteEffect(t, dir, "pkg/auth/login.go", "package auth\n// patched\n"),
		okVerifier)
	if err != nil {
		t.Fatalf("lifecycle failed: %v", err)
	}
	if st.ID != engineTaskID {
		t.Fatalf("state returned for wrong task: %q", st.ID)
	}
	if st.Cursor != nil {
		t.Fatalf("cursor not cleared after verified commit: %+v", st.Cursor)
	}
	engineLedger(t, eng,
		string(durable.EventProposalAuthorized),
		string(durable.EventCursorDispatched),
		string(durable.EventExecutionCommitted),
		string(durable.EventVerificationResult),
	)
	raw, _ := os.ReadFile(filepath.Join(dir, "pkg", "auth", "login.go"))
	if !strings.Contains(string(raw), "patched") {
		t.Fatalf("side effect did not land: %q", raw)
	}
}

// Guard denies out-of-scope proposals before any side effect runs.
func TestEngineDeniesOutOfScope(t *testing.T) {
	ctx := context.Background()
	eng, _ := engineFixture(t, scopeguard.WorkspaceBuild, nil, defaultWorkers())

	ran := false
	_, err := eng.ExecuteProposal(ctx,
		engineProposal(scopeguard.OpPatch, []string{"pkg/billing/invoice.go"}, "op-out", "s1", "patch billing"),
		[]string{"pkg/auth/login.go"},
		func(_ context.Context) (string, error) {
			ran = true
			return "x", nil
		}, okVerifier)
	if err == nil || !strings.Contains(err.Error(), "scope violation") {
		t.Fatalf("expected scope violation, got %v", err)
	}
	if ran {
		t.Fatal("executor effect ran for out-of-scope proposal")
	}
	engineLedger(t, eng, string(durable.EventScopeViolationRejected))
}

// Structural ambiguity routes to verification first: blocked without it,
// executed once evidence arrives.
func TestEngineVerificationGate(t *testing.T) {
	ctx := context.Background()
	graph := scopeguard.NewStaticGraph().
		SetModule("pkg/auth/login.go", "pkg/auth").
		SetModule("pkg/auth/session.go", "pkg/auth")
	eng, dir := engineFixture(t, scopeguard.WorkspaceBuild, graph, defaultWorkers())
	// Seed the ambiguous target so digests are real.
	if err := os.WriteFile(filepath.Join(dir, "pkg", "auth", "session.go"), []byte("package auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	amb := engineProposal(scopeguard.OpPatch, []string{"pkg/auth/session.go"}, "op-amb", "s1", "patch session")
	// 1. Unverified ambiguity is gated, never executed.
	if _, err := eng.ExecuteProposal(ctx, amb, []string{"pkg/auth/login.go"},
		engineWriteEffect(t, dir, "pkg/auth/session.go", "package auth\n// amb\n"), nil); err == nil {
		t.Fatal("expected verification gate for ambiguous proposal")
	}
	engineLedger(t, eng, string(durable.EventStructuralAmbiguity))

	// 2. Verified ambiguity executes through the real executor.
	st, err := eng.ExecuteProposal(ctx, amb, []string{"pkg/auth/login.go"},
		engineWriteEffect(t, dir, "pkg/auth/session.go", "package auth\n// amb verified\n"), okVerifier)
	if err != nil {
		t.Fatalf("verified ambiguity should execute: %v", err)
	}
	if st.Cursor != nil {
		t.Fatalf("cursor not cleared after verified commit: %+v", st.Cursor)
	}
}

// Reconciliation boundary: a crash-interrupted (pending) cursor is
// reconciled before the new proposal is evaluated — safe retry proceeds,
// conflicts never silently re-execute.
func TestEngineReconciliationBoundary(t *testing.T) {
	ctx := context.Background()
	eng, dir := engineFixture(t, scopeguard.WorkspaceBuild, nil, defaultWorkers())

	pre, err := durable.ComputeTreeDigest(dir, "pkg/auth/login.go")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after dispatch, before commit.
	if err := eng.Store().DispatchCursor(durable.ExecutionCursor{
		TaskID: engineTaskID, StepID: "step-0", OperationID: "op-crash",
		PreconditionDigest: pre, PostconditionDigest: "post-never-observed",
	}); err != nil {
		t.Fatal(err)
	}
	// Worktree still matches the precondition: SAFE_RETRY, no conflict.
	st, err := eng.ExecuteProposal(ctx,
		engineProposal(scopeguard.OpWrite, []string{"pkg/auth/login.go"}, "op-retry", "step-1", "retry after crash"),
		[]string{"pkg/auth/login.go"},
		engineWriteEffect(t, dir, "pkg/auth/login.go", "package auth\n// retried\n"),
		okVerifier)
	if err != nil {
		t.Fatalf("safe retry should proceed: %v", err)
	}
	if st.Cursor != nil {
		t.Fatalf("cursor not cleared after retry commit: %+v", st.Cursor)
	}
	body, _ := os.ReadFile(eng.Store().LedgerPath())
	if strings.Contains(string(body), string(durable.EventTargetConflict)) {
		t.Fatalf("safe retry must not record TARGET_CONFLICT:\n%s", body)
	}
}

// Failure -> Negative Knowledge -> Recovery -> RePlan -> STALE: a failed
// verification records evidence pressure, advances the context tier in both
// the planner and the ledger, records negative knowledge, classifies the
// failure, and marks constraints STALE on re-plan.
func TestEngineVerificationFailureLoop(t *testing.T) {
	ctx := context.Background()
	eng, dir := engineFixture(t, scopeguard.WorkspaceBuild, nil, defaultWorkers())

	_, err := eng.ExecuteProposal(ctx,
		engineProposal(scopeguard.OpWrite, []string{"pkg/auth/login.go"}, "op-bad", "step-1", "risky patch"),
		[]string{"pkg/auth/login.go"},
		engineWriteEffect(t, dir, "pkg/auth/login.go", "package auth\n// risky\n"),
		failingVerifier)
	if err == nil {
		t.Fatal("expected verification failure")
	}
	if !eng.Store().HasEvidencePressure(engineTaskID, string(adaptive.SignalVerificationFailure)) {
		t.Fatal("EVIDENCE_PRESSURE not persisted")
	}
	if got := eng.Store().ContextTier(engineTaskID); got != 1 {
		t.Fatalf("durable context tier = %d, want 1", got)
	}
	if got := eng.Planner().Tier(engineTaskID); got != adaptive.TierL1 {
		t.Fatalf("planner tier = %s, want L1 (memory drift from ledger)", got)
	}
	all := eng.Store().NegativeKnowledge(engineTaskID)
	if len(all) == 0 {
		t.Fatal("NEGATIVE_KNOWLEDGE_RECORDED not persisted")
	}
	for _, rec := range all {
		if rec.Status != durable.NegativeStatusStale {
			t.Fatalf("expected STALE after RE_PLAN, got %+v", rec)
		}
	}
	if active := eng.Store().ActiveNegativeKnowledge(engineTaskID, nil); len(active) != 0 {
		t.Fatalf("STALE constraints still active: %+v", active)
	}
	engineLedger(t, eng,
		string(durable.EventEvidencePressure),
		string(durable.EventNegativeKnowledge),
		string(durable.EventFailureClassified),
		string(durable.EventNegativeKnowledgeStale),
		string(durable.EventContextTierAdvanced),
	)
	// The canonical prompt renderer reflects the lifecycle: the stale
	// record is withheld from active constraints.
	entries := []adaptive.NegativeKnowledge{{
		ID: "nk-1", Hypothesis: all[0].Hypothesis, WhyRejected: all[0].WhyRejected,
		EvidenceRefs: all[0].EvidenceRefs, TargetScope: all[0].TargetScope,
		Status: adaptive.StatusStaleNegativeKnowledge,
	}}
	if got := adaptive.RenderImmutableConstraints(entries); got != "" {
		t.Fatalf("stale knowledge must not render, got %q", got)
	}
}

// All 6 Blocking Architectural Invariants through the real engine and its
// real substrate — no mocks.
func TestEngineBlockingInvariants(t *testing.T) {
	ctx := context.Background()

	t.Run("Invariant1_WorkerFailureIsNotTaskFailure", func(t *testing.T) {
		eng, dir := engineFixture(t, scopeguard.WorkspaceBuild, nil, defaultWorkers())
		boom := errors.New("connection reset by peer")
		_, err := eng.ExecuteProposal(ctx,
			engineProposal(scopeguard.OpWrite, []string{"pkg/auth/login.go"}, "op-i1", "step-1", "patch login"),
			[]string{"pkg/auth/login.go"},
			func(_ context.Context) (string, error) {
				// Real classify path: transport failure on a real effect.
				_ = dir
				return "", boom
			}, okVerifier)
		if err == nil {
			t.Fatal("expected execution failure")
		}
		got, ok := eng.Store().State(engineTaskID)
		if !ok || got.ID != engineTaskID {
			t.Fatalf("task identity lost after worker failure: %+v ok=%v", got, ok)
		}
		// UNKNOWN with all gates holding resumes via the real router.
		engineLedger(t, eng,
			string(durable.EventFailureClassified),
			string(durable.EventWorkerHandoff),
		)
	})

	t.Run("Invariant2_ResumeIsNotTranscriptReplay", func(t *testing.T) {
		eng, _ := engineFixture(t, scopeguard.WorkspaceBuild, nil, defaultWorkers())
		st, _ := eng.Store().State(engineTaskID)
		capsule := ephemeral.DeriveCapsule(ephemeral.CapsuleSource{
			State:   st,
			Current: ephemeral.StepDefinition{ID: "step-1", Goal: "patch login"},
			Budget:  ephemeral.BudgetState{RecoveryAttemptsLeft: 2, MaxRecoveryAttempts: 3},
		}, ephemeral.DefaultMaxEvidence)
		contract, err := ephemeral.BuildResumeContract(capsule, "ckpt-9", ephemeral.FailureProviderUnavailable, []string{"read", "patch"})
		if err != nil {
			t.Fatal(err)
		}
		if contract.Capsule.TaskID != engineTaskID || contract.CheckpointID != "ckpt-9" {
			t.Fatalf("contract lost identity: %+v", contract)
		}
		prompt := ephemeral.RenderResumePrompt(contract)
		if strings.Contains(strings.ToLower(prompt), "transcript") {
			t.Fatalf("resume prompt leaks transcript: %q", prompt)
		}
	})

	t.Run("Invariant3_CacheMissIsNotStateLoss", func(t *testing.T) {
		eng, _ := engineFixture(t, scopeguard.WorkspaceBuild, nil, defaultWorkers())
		if err := eng.Store().Checkpoint(engineTaskID, "ckpt-1"); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(eng.Store().SnapshotPath()); err != nil {
			t.Fatal(err)
		}
		if err := eng.Store().RebuildSnapshot(); err != nil {
			t.Fatal(err)
		}
		got, ok := eng.Store().State(engineTaskID)
		if !ok || got.LastCheckpointID != "ckpt-1" {
			t.Fatalf("snapshot rebuild lost state: %+v ok=%v", got, ok)
		}
	})

	t.Run("Invariant4_ContextExpansionIsNotAuthorityExpansion", func(t *testing.T) {
		eng, _ := engineFixture(t, scopeguard.WorkspaceBuild, nil, defaultWorkers())
		dec := adaptive.EvidencePressureEvaluator{}.Evaluate(adaptive.PressureInput{VerificationFailed: true})
		if _, expanded, _ := eng.Planner().RequestExpansion(engineTaskID, dec); !expanded {
			t.Fatal("expected tier expansion on evidence pressure")
		}
		sess, err := scopeguard.NewWorkspaceSession(engineTaskID, "ckpt-1", scopeguard.WorkspacePlan)
		if err != nil {
			t.Fatal(err)
		}
		gw := scopeguard.NewIntentGateway(scopeguard.NewScopeGuard(engineScope), nil, sess, nil, nil)
		got := gw.EvaluateProposal(ctx, scopeguard.Proposal{
			ID: "p", TaskID: engineTaskID, Workspace: scopeguard.WorkspacePlan,
			Op: scopeguard.OpWrite, TargetFiles: []string{"pkg/auth/login.go"},
		}, []string{"pkg/auth/login.go"})
		if got.Decision != scopeguard.DecisionDeny {
			t.Fatalf("context expansion granted write authority: %q", got.Decision)
		}
	})

	t.Run("Invariant5_WorkspaceSwitchIsNotTaskReset", func(t *testing.T) {
		eng, _ := engineFixture(t, scopeguard.WorkspaceBuild, nil, defaultWorkers())
		eng.Policy().AttachEvidence(scopeguard.EvidenceRef{ID: "ev-1", Subject: "diff"})
		ledger := scopeguard.StoreLedger{Store: eng.Store()}
		for _, ws := range []scopeguard.Workspace{
			scopeguard.WorkspaceInvestigate, scopeguard.WorkspaceReview, scopeguard.WorkspaceBuild,
		} {
			if err := eng.Policy().SwitchTo(ws, ledger); err != nil {
				t.Fatal(err)
			}
		}
		taskID, ckpt, ev, _, _ := eng.Policy().Snapshot()
		if taskID != engineTaskID || len(ev) != 1 {
			t.Fatalf("workspace switches reset task: %q %q evidence=%d", taskID, ckpt, len(ev))
		}
		engineLedger(t, eng, string(durable.EventWorkspaceSwitched))
	})

	t.Run("Invariant6_MissingResponseIsNotMissingSideEffect", func(t *testing.T) {
		eng, dir := engineFixture(t, scopeguard.WorkspaceBuild, nil, defaultWorkers())
		target := filepath.Join(dir, "pkg", "auth", "login.go")
		// Real digests for both sides of the cursor: pre over v1, post
		// over the v2 the real effect will write.
		pre, err := durable.ComputeTreeDigest(dir, "pkg/auth/login.go")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("package auth\n// v2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		post, err := durable.ComputeTreeDigest(dir, "pkg/auth/login.go")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("package auth\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		calls := 0
		effect := func(_ context.Context) (string, error) {
			calls++
			// Real mutation; the response is then "lost".
			if werr := os.WriteFile(target, []byte("package auth\n// v2\n"), 0o644); werr != nil {
				return "", werr
			}
			return durable.ComputeTreeDigest(dir, "pkg/auth/login.go")
		}
		digestOf := func() (string, error) { return durable.ComputeTreeDigest(dir, "pkg/auth/login.go") }
		runner := &durable.IdempotentRunner{Store: eng.Store()}
		// First attempt: precondition holds, effect runs once and commits.
		executed, err := runner.Run(engineTaskID, "step-1", "op-i6", pre, post, digestOf,
			func() error { _, eerr := effect(ctx); return eerr })
		if err != nil || !executed {
			t.Fatalf("first attempt should execute: executed=%v err=%v", executed, err)
		}
		// Retry after the lost response: the worktree already reflects
		// the postcondition, so the cursor reconciles to
		// ALREADY_COMMITTED without re-executing the real effect.
		executed, err = runner.Run(engineTaskID, "step-1", "op-i6", pre, post, digestOf,
			func() error { _, eerr := effect(ctx); return eerr })
		if err != nil {
			t.Fatal(err)
		}
		if executed {
			t.Fatal("re-executed an already-committed side effect")
		}
		if calls != 1 {
			t.Fatalf("effect re-invoked on retry: calls=%d", calls)
		}
	})
}

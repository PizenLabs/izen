package autonomy

// ── POST-R7 INTEGRATION PROOF: CREATE vs MODIFY TARGET SEMANTICS ─────────────
//
// R1–R7 froze the runtime contract: discovery evidence is not authority, the
// executor never guesses a target, and a mutation cannot reach the provider or
// the kernel without an authoritative boundary. Those proofs all ran on
// MODIFY: a target that already exists and can be read into the compiled
// context.
//
// The integration defect these tests close is the CREATE half of the same
// contract. Two independent seams refused a legitimate, explicitly declared
// creation target:
//
//  1. TARGET BINDING (strategy gateway). The operation-family table recognises
//     "add a file" but not "add file named X", because its creation entry is the
//     literal "add a". The missing-target rule then read the absent target as an
//     incomplete resolution and parked the run at clarification forever.
//  2. CONTEXT PROVENANCE (grant-gated re-compilation). The workspace contract
//     demands existing bytes for every requested target. A target that does not
//     exist YET can never satisfy it, so the granted path refused to compile the
//     very target the admission gate had already accepted as a creation.
//
// The runtime ALREADY states the correct contract at two places:
// `preflightExecutionSpec` ("an explicitly named creation target binds it by
// statement") and `EvaluatePreflightAdmission` ("an explicitly stated file that
// does not exist yet is a legitimate CREATION target"). These tests make the
// binding and context seams agree with it.
//
// Every assertion below reads runtime state, the kernel's own presence proof, or
// the bytes on disk. None of them accepts a progress enum.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/contextcompiler"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/kernelbridge"
)

// createWorkspace builds a real workspace with one unrelated existing file so
// discovery has something to observe, plus a scripted provider.
func createWorkspace(t *testing.T) (string, *mockProvider, *events.Bus, *execution.RuntimeExecutor) {
	t.Helper()
	root := t.TempDir()
	writeTarget(t, root, "readme.md", "# Project\n\nAn existing file.\n")
	bus := events.NewBus(events.DefaultBufferSize)
	content := "# addtest\n\ncreated through the frozen kernel\n"
	mock := &mockProvider{responses: []*ai.Response{{Content: content}, {Content: content}, {Content: content}}}
	x := testExecutor(t, root, mock, bus)
	return root, mock, bus, x
}

// TestCreate_DeclaredAbsentTargetCreatesFileThroughKernel is the CREATE
// acceptance proof through the real production runtime composition
// (Driver → ExecutorAdapter → RuntimeExecutor → kernelbridge.Apply).
//
// It drives the exact prompt from the phase brief and proves, in order:
//   - the explicitly declared absent target is BOUND by the gateway;
//   - admission ADMITS it as a creation (no clarification loop);
//   - the provider is invoked exactly once to stage the artifact;
//   - the mutation is NOT applied before human approval;
//   - approval creates the file with the exact bytes the model produced;
//   - the kernel itself proves the new file is present and the objective is
//     PROVEN from that evidence.
func TestCreate_DeclaredAbsentTargetCreatesFileThroughKernel(t *testing.T) {
	root, mock, _, x := createWorkspace(t)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, nil, WithGrantLedger(fullWorkspaceGrantLedger()))

	// The target does not exist at baseline.
	if _, err := os.Stat(filepath.Join(root, "addtest.md")); err == nil {
		t.Fatal("fixture invariant: addtest.md already exists")
	}

	term, err := d.Run(context.Background(), "add file named addtest.md")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("the create run terminated before approval: %+v", term)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want AWAITING_HUMAN at the approval gate", d.State())
	}

	// 1. THE TARGET IS BOUND BY THE GATEWAY, not discovered into existence.
	if got := d.resolved.Targets; len(got) != 1 || got[0] != "addtest.md" {
		t.Fatalf("gateway targets = %v, want [addtest.md]", got)
	}
	// 2. THE OPERATION IS CREATE, derived from the authoritative scope.
	if op := d.objectiveSemantics().Operation; op != execution.OperationCreate {
		t.Fatalf("operation = %s, want CREATE", op)
	}
	// 3. ADMISSION ADMITS the explicitly named creation target.
	if outcome := EvaluatePreflightAdmission(d.preflightExecutionSpec(context.Background())); outcome.Blocked() {
		t.Fatalf("the creation target was refused at admission (verdict=%s reason=%q)", outcome.Verdict, outcome.Reason)
	}
	// 4. ONE provider invocation to stage the artifact; nothing applied.
	if calls := mock.calls(); calls != 1 {
		t.Fatalf("provider calls = %d, want 1 (the staging invocation)", calls)
	}
	if _, statErr := os.Stat(filepath.Join(root, "addtest.md")); statErr == nil {
		t.Fatal("addtest.md was created BEFORE approval")
	}
	boundary := d.Boundary()
	if boundary == nil || boundary.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("boundary = %+v, want the approval gate", boundary)
	}
	if len(boundary.Targets) != 1 || boundary.Targets[0] != "addtest.md" {
		t.Fatalf("approval gate covers %v, want exactly [addtest.md]", boundary.Targets)
	}

	// 5. HUMAN APPROVAL APPLIES THE CREATION.
	term, err = d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want COMPLETED", term)
	}

	// 6. INDEPENDENT FILESYSTEM EVIDENCE: the exact bytes are on disk.
	data, readErr := os.ReadFile(filepath.Join(root, "addtest.md"))
	if readErr != nil {
		t.Fatalf("addtest.md was not created: %v", readErr)
	}
	if string(data) != "# addtest\n\ncreated through the frozen kernel\n" {
		t.Fatalf("created bytes = %q", string(data))
	}

	// 7. KERNEL REACHABILITY: the frozen kernel's own observation proves the new
	// file is present. This is the bridge's independent re-check, not a claim.
	obs := kernelbridge.Observe(context.Background(), root, []string{"addtest.md"})
	if !obs.Proven() {
		t.Fatalf("the kernel did not prove the created file present: %+v", obs)
	}
	if !obs.Exists("addtest.md") {
		t.Fatalf("kernel presence evidence does not include addtest.md: %+v", obs)
	}

	// 8. COMPLETION IS EVIDENCE-BOUND: the objective is PROVEN, not merely
	// "a mutation happened".
	if outcome := d.objectiveEvaluation().Outcome; outcome != execution.ObjectiveProven {
		t.Fatalf("objective outcome = %s (%s), want PROVEN", outcome, d.objectiveEvaluation().Reason)
	}
}

// TestCreate_GrantedContextAcceptsAnAbsentCreationTarget pins the context seam
// directly: a DECLARED CREATION contract must compile an absent target as a
// named, truthful creation destination rather than refusing it as missing.
func TestCreate_GrantedContextAcceptsAnAbsentCreationTarget(t *testing.T) {
	_, _, _, x := createWorkspace(t)

	prov, err := x.RecompileGrantedIntentContext(context.Background(), []string{"addtest.md"},
		string(autonomy.IntentModification), contextcompiler.IntentContextCreation)
	if err != nil {
		t.Fatalf("a declared creation target was refused: %v", err)
	}
	if !prov.Valid {
		t.Fatalf("creation provenance refused a valid creation target: %+v", prov)
	}
	if !prov.ScopeMatched {
		t.Fatalf("creation target is not in scope: %+v", prov)
	}
	if len(prov.MissingTargets) != 0 {
		t.Fatalf("creation target reported missing: %v", prov.MissingTargets)
	}
	// The target is REPRESENTED in the payload (its creation declaration is real
	// content), not silently dropped. The contextcompiler unit test pins that
	// the representation is the explicit creation form rather than file bytes.
	if prov.ObservedTokens <= 0 {
		t.Fatalf("a declared creation target compiled to an empty payload: %+v", prov)
	}
}

// TestCreate_WorkspaceContractStillRefusesAnAbsentTarget is the negative half:
// the fix must NOT weaken the MODIFY contract. An absent target under the
// workspace (modification) contract is still refused, exactly as before. CREATE
// is a distinct contract, not a bypass.
func TestCreate_WorkspaceContractStillRefusesAnAbsentTarget(t *testing.T) {
	_, _, _, x := createWorkspace(t)

	_, err := x.RecompileGrantedIntentContext(context.Background(), []string{"addtest.md"},
		string(autonomy.IntentModification), contextcompiler.IntentContextWorkspace)
	if err == nil {
		t.Fatal("the MODIFY contract accepted an absent target; the creation fix weakened authority")
	}
	if !errors.Is(err, execution.ErrIntentContextProvenance) {
		t.Fatalf("err = %v, want ErrIntentContextProvenance", err)
	}
}

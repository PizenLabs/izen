package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── Phase 9A — Transaction foundation regression tests ─────────────────────
//
// These tests drive the REAL $hot mutation path (handleHotfixCmd →
// proposal → approval → applyHotfixPatch → buildResultMsg terminal) and pin
// the authoritative mutation-boundary invariants:
//
//	ONE user mutation → ONE MutationSet → record → apply → verify → COMMIT or
//	ROLLBACK, where a committed mutation is terminal and can never be undone by
//	a later failure.

// completeHotfixApply drives one $hot end to end (proposal + apply + terminal)
// on the given model and returns the model, the captured mutation boundary the
// apply owned, and the terminal buildResultMsg. It does NOT assert success so
// failure/cancel paths can reuse it.
func completeHotfixApply(t *testing.T, m *model, mock *mockProvider, prompt string) (*model, *execution.MutationSet, buildResultMsg) {
	t.Helper()
	cmd := m.handleHotfixCmd(prompt)
	if cmd == nil {
		t.Fatal("handleHotfixCmd returned nil")
	}
	msgs := runBuildCmdsFiltered(t, cmd)
	var hp hotfixProposalMsg
	for _, msg := range msgs {
		if p, ok := msg.(hotfixProposalMsg); ok {
			hp = p
		}
	}
	if hp.Err != nil {
		t.Fatalf("hotfix generation failed: %v", hp.Err)
	}
	upd, _ := m.Update(hp)
	m = upd.(*model)
	// Real approval path: the hotfix approval gate invokes applyHotfixPatch.
	cmd = m.applyHotfixPatch(hp.Task, hp.Patch)
	ms := m.execEng.MutationSet()
	if ms == nil {
		t.Fatal("no mutation boundary after apply dispatch")
	}
	res := cmd()
	br, ok := res.(buildResultMsg)
	if !ok {
		t.Fatalf("apply returned %T, want buildResultMsg", res)
	}
	m.hotfixActive = true
	upd, _ = m.Update(res)
	m = upd.(*model)
	return m, ms, br
}

// runSuccessfulHotfix writes target, then runs a full successful $hot against
// it, returning the model and the committed boundary.
func runSuccessfulHotfix(t *testing.T, target, orig, fixed string) (*model, *execution.MutationSet) {
	t.Helper()
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```html\n" + fixed + "```",
		TokenInput:  12,
		TokenOutput: 24,
	}}}
	m := hotfixTruthModel(t, mock)
	if err := os.WriteFile(target, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	m, ms, br := completeHotfixApply(t, m, mock, "Remove extra text from @"+target)
	if br.exitCode != 0 {
		t.Fatalf("hotfix apply failed: %v", br.err)
	}
	return m, ms
}

// stageHotfixTask mirrors what handleHotfixCmd does before the apply: the task
// is staged into the session ledger so the workflow transition guard
// (EventBuild requires an authorized plan) admits the apply.
func stageHotfixTask(m *model, task *plan.Task) {
	tasks := []plan.Task{*task}
	m.sess.StageTaskList(&tasks)
	_ = m.sess.Save()
}

const (
	hotfixOrig  = "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Home</h1>\n  <h2>Extra text</h2>\n</body>\n</html>\n"
	hotfixFixed = "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Home</h1>\n</body>\n</html>\n"
)

// TestMutationSet_SuccessfulHotfixCommitsTransaction is #1: a successful $hot
// commits its own MutationSet — the transaction is terminal and no snapshot
// remains staged.
func TestMutationSet_SuccessfulHotfixCommitsTransaction(t *testing.T) {
	m, ms := runSuccessfulHotfix(t, "index.html", hotfixOrig, hotfixFixed)
	if !ms.Committed() {
		t.Fatalf("mutation boundary state = %q, want committed", ms.State)
	}
	if len(ms.Transaction.Snapshots) != 0 {
		t.Fatalf("snapshots remain staged after commit: %d", len(ms.Transaction.Snapshots))
	}
	if ms.Transaction == nil || !ms.Transaction.Committed() {
		t.Fatal("owned transaction must be committed and terminal")
	}
	// The engine installs a fresh boundary for the next mutation.
	if m.execEng.MutationSet() == ms {
		t.Fatal("engine still references the committed boundary")
	}
	if m.execEng.MutationSet().State != execution.MutationPending {
		t.Fatalf("fresh boundary state = %q, want pending", m.execEng.MutationSet().State)
	}
}

// TestMutationSet_CommittedTransactionCannotRollback is #2: after a successful
// hotfix, neither the engine-level rollback nor the set's own rollback can
// undo the committed file.
func TestMutationSet_CommittedTransactionCannotRollback(t *testing.T) {
	m, ms := runSuccessfulHotfix(t, "index.html", hotfixOrig, hotfixFixed)
	onDisk, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) == hotfixOrig {
		t.Fatal("precondition: file was not mutated")
	}

	if errs := m.execEng.RollbackTransaction(); len(errs) != 0 {
		t.Fatalf("engine rollback after commit returned errors: %v", errs)
	}
	onDisk, _ = os.ReadFile("index.html")
	if string(onDisk) == hotfixOrig {
		t.Fatal("engine rollback undid a committed hotfix")
	}

	if errs := ms.Rollback(); len(errs) != 0 {
		t.Fatalf("set rollback after commit returned errors: %v", errs)
	}
	if !ms.Committed() {
		t.Fatal("rollback flipped a committed set")
	}
	onDisk, _ = os.ReadFile("index.html")
	if string(onDisk) == hotfixOrig {
		t.Fatal("set rollback undid a committed hotfix")
	}
}

// TestMutationSet_FailedApplyRollsBack is #3: an apply that fails after the
// target was recorded into the boundary rolls back exactly that boundary and
// leaves the file unchanged.
func TestMutationSet_FailedApplyRollsBack(t *testing.T) {
	mock := &mockProvider{}
	m := hotfixTruthModel(t, mock)
	big := strings.Repeat("<!-- filler line -->\n", 6000) // > 50KB
	if err := os.WriteFile("big.html", []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	// An ambiguous-snippet patch against a large file deterministically fails
	// AFTER the target is recorded into the mutation boundary.
	task := &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "big.html", Status: "idle"}
	patch := &execution.Patch{ID: "hotfix-fail", File: "big.html", Modified: "<p>tiny snippet</p>", TaskID: 1, ContextID: m.sess.ContextID}
	stageHotfixTask(m, task)
	cmd := m.applyHotfixPatch(task, patch)
	ms := m.execEng.MutationSet()
	m.hotfixActive = true
	res := cmd()
	br, ok := res.(buildResultMsg)
	if !ok {
		t.Fatalf("apply returned %T, want buildResultMsg", res)
	}
	if br.exitCode == 0 {
		t.Fatal("precondition: apply unexpectedly succeeded")
	}
	if !strings.Contains(br.err.Error(), "ambiguous snippet") && !strings.Contains(br.err.Error(), "patch") {
		t.Fatalf("apply should fail at patch resolution, got: %v", br.err)
	}
	m.Update(res)
	if !ms.RolledBack() {
		t.Fatalf("mutation boundary state = %q, want rolled_back", ms.State)
	}
	if len(ms.Transaction.Snapshots) != 0 {
		t.Fatalf("snapshots remain staged after rollback: %d", len(ms.Transaction.Snapshots))
	}
	onDisk, err := os.ReadFile("big.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != big {
		t.Fatal("failed apply mutated the file")
	}
}

// TestMutationSet_VerificationFailureRollsBack is #4: when the deterministic
// verification gate fails inside the apply, the mutation is restored and the
// owned boundary is rolled back.
func TestMutationSet_VerificationFailureRollsBack(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```html\n" + hotfixFixed + "```",
		TokenInput:  12,
		TokenOutput: 24,
	}}}
	m := hotfixTruthModel(t, mock)
	// The compile-gate verifier would invoke the real Go toolchain; pin a
	// deterministic always-failing gate instead.
	failing := execution.NewVerifier(".")
	failing.SetCustomSteps([]execution.VerificationStep{{Name: "syntax", Command: "false", Optional: false}})
	m.execEng.Patches.SetVerifier(failing)
	if err := os.WriteFile("index.html", []byte(hotfixOrig), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ms, br := completeHotfixApply(t, m, mock, "Remove extra text from @index.html")
	if br.exitCode == 0 {
		t.Fatal("precondition: apply unexpectedly passed verification")
	}
	if !ms.RolledBack() {
		t.Fatalf("mutation boundary state = %q, want rolled_back", ms.State)
	}
	onDisk, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != hotfixOrig {
		t.Fatalf("verification failure did not restore the file:\n%s", onDisk)
	}
}

// TestMutationSet_CancellationBeforeApplyDoesNotMutate is #6: cancelling the
// operation before the worker runs means the apply never touches disk and the
// owned boundary is clean.
func TestMutationSet_CancellationBeforeApplyDoesNotMutate(t *testing.T) {
	mock := &mockProvider{}
	m := hotfixTruthModel(t, mock)
	if err := os.WriteFile("index.html", []byte(hotfixOrig), 0o644); err != nil {
		t.Fatal(err)
	}
	task := &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Status: "idle"}
	patch := &execution.Patch{ID: "hotfix-cancel", File: "index.html", Modified: hotfixFixed, IsFullRewrite: true}
	stageHotfixTask(m, task)
	cmd := m.applyHotfixPatch(task, patch)
	ms := m.execEng.MutationSet()
	// Cancel the apply operation's context before the worker runs: the apply
	// deadline is derived from it and fast-fails without touching disk.
	m.activeOp.Cancel()
	m.hotfixActive = true
	res := cmd()
	m.Update(res)
	onDisk, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != hotfixOrig {
		t.Fatalf("cancellation before apply mutated the file:\n%s", onDisk)
	}
	if !ms.Terminal() {
		t.Fatalf("boundary not terminal after cancelled apply: %q", ms.State)
	}
	if len(ms.Transaction.Snapshots) != 0 {
		t.Fatalf("snapshots remain staged after cancelled apply: %d", len(ms.Transaction.Snapshots))
	}
}

// TestMutationSet_CancellationAfterApplyRollsBack is #5: a mutation that was
// applied but whose terminal outcome is cancellation is rolled back through
// the same authoritative boundary the UI terminal invokes (RollbackTransaction).
func TestMutationSet_CancellationAfterApplyRollsBack(t *testing.T) {
	m := hotfixTruthModel(t, &mockProvider{})
	if err := os.WriteFile("index.html", []byte(hotfixOrig), 0o644); err != nil {
		t.Fatal(err)
	}
	// The hotfix apply owns a fresh boundary (same call the terminal makes).
	m.execEng.BeginTransaction()
	ms := m.execEng.MutationSet()
	if err := m.authorizeBuildExecution([]string{"index.html"}, true); err != nil {
		t.Fatalf("authorization failed: %v", err)
	}
	patch := &execution.Patch{ID: "hotfix-applied", File: "index.html", Modified: hotfixFixed, IsFullRewrite: true}
	if err := m.execEng.Patches.Apply(patch); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	onDisk, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) == hotfixOrig {
		t.Fatal("precondition: apply did not mutate the file")
	}
	// The cancellation terminal invokes RollbackTransaction on the owned set.
	if errs := m.execEng.RollbackTransaction(); len(errs) != 0 {
		t.Fatalf("rollback errors: %v", errs)
	}
	if !ms.RolledBack() {
		t.Fatalf("boundary state = %q, want rolled_back", ms.State)
	}
	onDisk, _ = os.ReadFile("index.html")
	if string(onDisk) != hotfixOrig {
		t.Fatalf("cancelled mutation was not rolled back:\n%s", onDisk)
	}
}

// TestMutationSet_LaterFailureCannotRollbackCommittedHotfix is #7 — the core
// regression: a later operation's failure rolls back only its own boundary and
// can never undo an earlier committed hotfix.
func TestMutationSet_LaterFailureCannotRollbackCommittedHotfix(t *testing.T) {
	m, ms1 := runSuccessfulHotfix(t, "index.html", hotfixOrig, hotfixFixed)

	// A subsequent failing mutation on a different file.
	big := strings.Repeat("<!-- filler line -->\n", 6000)
	if err := os.WriteFile("big.html", []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	task := &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "big.html", Status: "idle"}
	patch := &execution.Patch{ID: "hotfix-fail-2", File: "big.html", Modified: "<p>tiny</p>", TaskID: 1, ContextID: m.sess.ContextID}
	stageHotfixTask(m, task)
	cmd := m.applyHotfixPatch(task, patch)
	ms2 := m.execEng.MutationSet()
	if ms2 == ms1 {
		t.Fatal("second mutation must own a distinct boundary")
	}
	m.hotfixActive = true
	res := cmd()
	m.Update(res)

	if !ms1.Committed() {
		t.Fatalf("first boundary state = %q, want committed", ms1.State)
	}
	if !ms2.RolledBack() {
		t.Fatalf("second boundary state = %q, want rolled_back", ms2.State)
	}
	onDisk1, _ := os.ReadFile("index.html")
	if string(onDisk1) == hotfixOrig {
		t.Fatal("later failure undid the committed hotfix — index.html rolled back")
	}
	if !strings.Contains(string(onDisk1), "<h1>Home</h1>") {
		t.Fatalf("committed hotfix content lost:\n%s", onDisk1)
	}
	onDisk2, _ := os.ReadFile("big.html")
	if string(onDisk2) != big {
		t.Fatal("failing mutation mutated big.html")
	}
}

// TestMutationSet_TwoSequentialSuccessesRemainCommitted is #8: two sequential
// successful hotfixes both remain committed; no transaction stays staged.
func TestMutationSet_TwoSequentialSuccessesRemainCommitted(t *testing.T) {
	_, ms1 := runSuccessfulHotfix(t, "a.html", hotfixOrig, hotfixFixed)
	_, ms2 := runSuccessfulHotfix(t, "b.html", hotfixOrig, hotfixFixed)
	if !ms1.Committed() || !ms2.Committed() {
		t.Fatalf("boundaries = %q / %q, want both committed", ms1.State, ms2.State)
	}
	if ms1 == ms2 {
		t.Fatal("two mutations must own distinct boundaries")
	}
	onDisk1, _ := os.ReadFile("a.html")
	if string(onDisk1) == hotfixOrig {
		t.Fatal("first mutation was not preserved")
	}
	onDisk2, _ := os.ReadFile("b.html")
	if string(onDisk2) == hotfixOrig {
		t.Fatal("second mutation was not preserved")
	}
	if len(ms1.Transaction.Snapshots) != 0 || len(ms2.Transaction.Snapshots) != 0 {
		t.Fatal("committed boundaries still hold staged snapshots")
	}
}

// TestMutationSet_TransactionOwnershipIsSingleOwner is #10: the engine and the
// PatchManager must agree on ONE boundary, and terminal outcomes relink both to
// the same fresh boundary.
func TestMutationSet_TransactionOwnershipIsSingleOwner(t *testing.T) {
	m := hotfixTruthModel(t, &mockProvider{})
	m.execEng.BeginTransaction()
	if m.execEng.MutationSet() != m.execEng.Patches.MutationSet() {
		t.Fatal("engine and PatchManager hold different mutation boundaries")
	}
	m.execEng.CommitTransaction()
	if m.execEng.MutationSet() == nil || m.execEng.Patches.MutationSet() == nil {
		t.Fatal("fresh boundary missing after commit")
	}
	if m.execEng.MutationSet() != m.execEng.Patches.MutationSet() {
		t.Fatal("engine and PatchManager desynced after commit")
	}
	m.execEng.RollbackTransaction()
	if m.execEng.MutationSet() != m.execEng.Patches.MutationSet() {
		t.Fatal("engine and PatchManager desynced after rollback")
	}
}

// TestMutationSet_NoStagedTransactionAfterTerminal is #11/#12: no snapshot
// survives a terminal success or a terminal rollback.
func TestMutationSet_NoStagedTransactionAfterTerminal(t *testing.T) {
	_, ms := runSuccessfulHotfix(t, "index.html", hotfixOrig, hotfixFixed)
	if len(ms.Transaction.Snapshots) != 0 {
		t.Fatalf("staged snapshots after success: %d", len(ms.Transaction.Snapshots))
	}
	if errs := ms.Rollback(); len(errs) != 0 {
		t.Fatalf("rollback after commit errored: %v", errs)
	}
	if len(ms.Transaction.Snapshots) != 0 {
		t.Fatalf("staged snapshots after committed-rollback attempt: %d", len(ms.Transaction.Snapshots))
	}
}

// TestMutationSet_NoDoubleCommitNoDoubleRollback is #13/#14: the engine-level
// terminal calls are safe to repeat and never re-run on a terminal boundary.
func TestMutationSet_NoDoubleCommitNoDoubleRollback(t *testing.T) {
	m := hotfixTruthModel(t, &mockProvider{})
	m.execEng.BeginTransaction()
	m.execEng.CommitTransaction()
	m.execEng.CommitTransaction() // second commit must be a safe no-op
	if m.execEng.MutationSet() == nil {
		t.Fatal("no boundary after double commit")
	}
	m.execEng.RollbackTransaction()
	m.execEng.RollbackTransaction() // second rollback must be a safe no-op
	if m.execEng.MutationSet() == nil {
		t.Fatal("no boundary after double rollback")
	}
}

// TestMutationSet_MutationEvidenceSemanticsPreserved is #15: the per-target
// outcomes recorded into the boundary keep the existing MutationEvidence
// semantics (changed ⇒ ApplyExecutedChanged; failed ⇒ no success). The hotfix
// harness disables the compile-gate verifier (SetVerifier(nil)), so the
// evidence must NOT claim a verification that did not run — the old fabricated
// VerificationRun/Passed=true for changed outcomes is gone.
func TestMutationSet_MutationEvidenceSemanticsPreserved(t *testing.T) {
	_, ms := runSuccessfulHotfix(t, "index.html", hotfixOrig, hotfixFixed)
	ev := ms.OutcomeFor("index.html")
	if ev != execution.OutcomeChanged {
		t.Fatalf("outcome for index.html = %q, want changed", ev)
	}
	var rec *execution.MutationEvidence
	for i := range ms.Outcomes {
		if ms.Outcomes[i].File == "index.html" {
			rec = &ms.Outcomes[i]
			break
		}
	}
	if rec == nil {
		t.Fatal("no evidence record for index.html in the boundary")
	}
	if !rec.ApplyExecutedChanged() {
		t.Fatalf("evidence does not prove an executed mutation: %+v", rec)
	}
	// No verifier was attached to this boundary's PatchManager — the evidence
	// must never claim a verification pass (INVARIANT 6: verification truth).
	if rec.Verify() {
		t.Fatalf("evidence claims verification passed without a verifier gate: %+v", rec)
	}
	if !rec.Outcome.MutationSucceeded() {
		t.Fatalf("changed outcome must MutationSucceeded: %+v", rec)
	}

	// Failed apply records the failed outcome with the same semantics.
	mock := &mockProvider{}
	m2 := hotfixTruthModel(t, mock)
	big := strings.Repeat("<!-- filler line -->\n", 6000)
	if err := os.WriteFile("big.html", []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	task := &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "big.html", Status: "idle"}
	patch := &execution.Patch{ID: "hotfix-ev-fail", File: "big.html", Modified: "<p>tiny</p>", TaskID: 1, ContextID: m2.sess.ContextID}
	stageHotfixTask(m2, task)
	cmd := m2.applyHotfixPatch(task, patch)
	ms2 := m2.execEng.MutationSet()
	m2.hotfixActive = true
	res := cmd()
	m2.Update(res)
	if ev2 := ms2.OutcomeFor("big.html"); ev2 != execution.OutcomeApplyFailed {
		t.Fatalf("failed outcome = %q, want apply_failed", ev2)
	}
	for i := range ms2.Outcomes {
		if ms2.Outcomes[i].File == "big.html" {
			if ms2.Outcomes[i].Outcome.MutationSucceeded() {
				t.Fatalf("failed outcome must not MutationSucceeded: %+v", ms2.Outcomes[i])
			}
		}
	}
}

// TestMutationSet_ExecutionProofSemanticsPreserved is #16: the Phase 8
// ExecutionProof contract survives the transaction fix unchanged — a
// successful hotfix proves apply + filesystem + verification.
func TestMutationSet_ExecutionProofSemanticsPreserved(t *testing.T) {
	m, _ := runSuccessfulHotfix(t, "index.html", hotfixOrig, hotfixFixed)
	p := m.lastExecutionProof
	if !p.ApplyExecuted || !p.FilesystemChanged {
		t.Fatalf("proof must show apply executed + filesystem changed: %+v", p)
	}
	if !p.VerificationPassed {
		t.Fatalf("proof must show verification passed: %+v", p)
	}
	if !p.Successful() {
		t.Fatalf("proof must be Successful(): %+v", p)
	}
	if p.ProviderInvocations != 1 {
		t.Fatalf("provider invocations = %d, want 1", p.ProviderInvocations)
	}
}

// TestMutationSet_SingleFileRegression via the real input dispatch is the
// Phase-8 baseline: "$hot Remove extra text from @index.html" still resolves
// one explicit target, produces one provider invocation, and applies with
// evidence — the transaction fix changes none of it.
func TestMutationSet_SingleFileRegression(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content:     "```html\n" + hotfixFixed + "```",
		TokenInput:  2860,
		TokenOutput: 2048,
	}}}
	m := hotfixTruthModel(t, mock)
	if err := os.WriteFile("index.html", []byte(hotfixOrig), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := m.handleHotfixCmd("Remove extra text from @index.html")
	if cmd == nil {
		t.Fatal("handleHotfixCmd returned nil")
	}
	msgs := runBuildCmdsFiltered(t, cmd)
	var hp hotfixProposalMsg
	for _, msg := range msgs {
		if p, ok := msg.(hotfixProposalMsg); ok {
			hp = p
		}
	}
	if hp.Err != nil {
		t.Fatalf("generation failed: %v", hp.Err)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider invoked %d times, want exactly 1", mock.callCount)
	}
	if len(m.sess.CurrentTasks) != 1 || m.sess.CurrentTasks[0].Target != "index.html" {
		t.Fatalf("explicit target not resolved: %+v", m.sess.CurrentTasks)
	}
	upd, _ := m.Update(hp)
	m = upd.(*model)
	proposal := m.pendingProposals[0]
	applyMsg := m.applyProposalCmd(proposal)()
	mr, ok := applyMsg.(mutationResultMsg)
	if !ok {
		t.Fatalf("expected mutationResultMsg, got %T", applyMsg)
	}
	if mr.err != nil {
		t.Fatalf("apply failed: %v", mr.err)
	}
	if mr.evidence == nil || !mr.evidence.ApplyExecutedChanged() {
		t.Fatalf("evidence does not prove the mutation: %+v", mr.evidence)
	}
	onDisk, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) == hotfixOrig {
		t.Fatal("filesystem was not mutated")
	}
}

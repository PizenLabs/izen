package autonomy

// ── DURABLE CROSS-PROCESS PROPOSAL RECOVERY ────────────────────────────────
//
// These tests prove the round trip through the REAL durable store, not through
// an in-memory object. The original in-memory owner is discarded, the store is
// closed and reopened from disk, and a brand-new Driver instance reads the
// proposal back out of ledger.ndjson and resumes the task.
//
// A test that called RestoreProposal with an object it still held would prove
// nothing about recovery: the point is that the only surviving copy of the
// authoritative proposal state is the on-disk journal.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/proposal"
)

const recoveryObjective = "redesign this project help me"

// reopenLedger closes a store and opens a NEW one over the same directory, so
// the only surviving state is whatever replay can reconstruct from
// ledger.ndjson. That is the "newly started process".
func reopenLedger(t *testing.T, old *durable.TaskStore) *durable.TaskStore {
	t.Helper()
	root := old.WorkDir()
	if err := old.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
	return openLedger(t, root)
}

// TestProposalSurvivesProcessRestartThroughTheDurableStore is the acceptance
// test for cross-process recovery: the proposal state is written, the original
// owner is destroyed, a new owner is constructed over the same durable store,
// and the restored proposal resumes the task at the SAME revision with the SAME
// human decisions and the SAME acceptance obligations.
func TestProposalSurvivesProcessRestartThroughTheDurableStore(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	store := openLedger(t, root)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	first := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt",
		WithLedger(store, "session-recovery"))

	if _, err := first.Run(context.Background(), recoveryObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	parked := first.Boundary()
	if parked == nil || parked.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("no adaptive proposal boundary: %+v", parked)
	}
	// The original in-memory owner is the ONLY place the proposal state lived.
	// Take a copy of what the ledger must be able to reproduce, then drop it.
	wantRevision := parked.ProposalRevision
	wantFingerprint := parked.ProposalFingerprint
	wantID := parked.ProposalID
	first = nil

	// A new process: a NEW store handle over the same journal, and a NEW driver.
	fresh := reopenLedger(t, store)
	store = fresh
	second := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt",
		WithLedger(store, "session-recovery"))

	restored, err := second.RestoreProposalFromLedger(recoveryObjective)
	if err != nil {
		t.Fatalf("RestoreProposalFromLedger: %v", err)
	}
	if !restored {
		t.Fatal("a durable proposal snapshot was recorded but nothing was restored")
	}
	c := second.proposalState.Contract()
	if c.Revision != wantRevision {
		t.Fatalf("restored revision = %d, want %d (durable store did not restore the proposal identity)", c.Revision, wantRevision)
	}
	if string(c.ID) != wantID {
		t.Fatalf("restored proposal id = %q, want %q", c.ID, wantID)
	}
	if second.proposalState.Fingerprint() != wantFingerprint {
		t.Fatalf("restored fingerprint = %q, want %q", second.proposalState.Fingerprint(), wantFingerprint)
	}

	// The restored proposal re-parks at the same revision, awaiting the same
	// decision, and executes nothing until it is answered.
	if _, err := second.Run(context.Background(), recoveryObjective); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	resumed := second.Boundary()
	if resumed == nil || resumed.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("the resumed run did not re-park at the adaptive proposal: %+v", resumed)
	}
	if resumed.ProposalRevision != wantRevision {
		t.Fatalf("re-parked revision = %d, want %d (the durable revision was not carried)", resumed.ProposalRevision, wantRevision)
	}
	if len(resumed.ProposalQuestions) != 1 || resumed.ProposalQuestions[0].ID != "scope" {
		t.Fatalf("re-parked questions = %+v, want the unanswered one", resumed.ProposalQuestions)
	}
	if p.calls != 0 {
		t.Fatalf("provider was called %d times before a decision, on a restored proposal", p.calls)
	}
}

// TestRestoredProposalCompilesAndReachesTheExistingApprovalGate proves the
// restoration continues the work, not just the record: the answer the human
// gave in the FIRST process is restored with its provenance, compiles into the
// existing execution contract, and the mutation still has to pass the existing
// approval gate in the SECOND one.
func TestRestoredProposalCompilesAndReachesTheExistingApprovalGate(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	store := openLedger(t, root)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	first := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt",
		WithLedger(store, "session-restore-2"))
	if _, err := first.Run(context.Background(), recoveryObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := first.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("no adaptive boundary: %+v", b)
	}
	// The human answers, the proposal compiles, and the run parks at the real
	// approval gate.
	if _, err := first.ResumeAdaptiveProposal(context.Background(), b.ProposalFingerprint, []ProposalDecisionInput{
		{QuestionID: "scope", OptionID: "evidence-bound"},
	}); err != nil {
		t.Fatalf("ResumeAdaptiveProposal: %v", err)
	}
	if first.Boundary() == nil || first.Boundary().Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("acceptance did not reach the approval gate: %+v", first.Boundary())
	}
	first = nil

	store = reopenLedger(t, store)
	second := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt",
		WithLedger(store, "session-restore-2"))
	restored, err := second.RestoreProposalFromLedger(recoveryObjective)
	if err != nil {
		t.Fatalf("RestoreProposalFromLedger: %v", err)
	}
	if !restored {
		t.Fatal("the accepted decision was not restored from the ledger")
	}
	// A restored proposal is STATE, never authority: nothing is compiled, no
	// mutation is admitted, and no approval is carried across the boundary.
	if second.compiledProposal != nil {
		t.Fatalf("restoring a proposal compiled it into an execution contract: %+v", second.compiledProposal)
	}
	if second.State() == autonomy.RuntimeExecuting {
		t.Fatal("restoring a proposal resumed execution")
	}
	// The accepted human decision and its provenance survived the restart.
	c := second.proposalState.Contract()
	d := c.DecisionFor("scope")
	if d == nil {
		t.Fatal("the restored proposal lost the human decision")
	}
	if d.Provenance != proposal.ProvenanceHuman {
		t.Fatalf("restored decision provenance = %s, want human", d.Provenance)
	}
	if d.OptionID != "evidence-bound" {
		t.Fatalf("restored decision = %q, want the answer the human actually gave", d.OptionID)
	}

	// The restored decision set is complete, so the resumed run continues into
	// execution — and still stops at the existing approval gate.
	if _, err := second.Run(context.Background(), recoveryObjective); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	if second.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human (the restored work must still be authorized)", second.State())
	}
	if second.Boundary() == nil || second.Boundary().Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("the restored proposal bypassed the approval gate: %+v", second.Boundary())
	}
	if second.compiledProposal == nil || second.compiledProposal.Revision < 2 {
		t.Fatalf("compiled proposal = %+v, want the restored revision >= 2", second.compiledProposal)
	}
	// Acceptance is not authorization: nothing was written without approval.
	if html := readTarget(t, root, "index.html"); !strings.Contains(html, "Old Title") {
		t.Fatalf("the restored run mutated the workspace before approval: %q", html)
	}
}

// TestRestoreRefusesTamperedAndForeignSnapshots is the fail-closed half: a
// snapshot whose contents, provenance, identity or ownership cannot be
// established is refused rather than adopted.
func TestRestoreRefusesTamperedAndForeignSnapshots(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, payload map[string]any)
		wantSub string
	}{
		{
			name: "tampered fingerprint",
			mutate: func(_ *testing.T, p map[string]any) {
				p["fingerprint"] = "0bad0bad0bad0bad"
			},
			wantSub: "fingerprint",
		},
		{
			name: "empty snapshot body",
			mutate: func(_ *testing.T, p map[string]any) {
				p["snapshot"] = ""
			},
			wantSub: "no snapshot body",
		},
		{
			name: "undecodable body",
			mutate: func(_ *testing.T, p map[string]any) {
				p["snapshot"] = `{"objective":"truncated`
			},
			wantSub: "undecodable",
		},
		{
			name: "contract without a revision",
			mutate: func(t *testing.T, p map[string]any) {
				var c proposal.Contract
				if err := json.Unmarshal([]byte(p["snapshot"].(string)), &c); err != nil {
					t.Fatal(err)
				}
				c.Revision = 0
				raw, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				p["snapshot"] = string(raw)
				p["revision"] = float64(0)
				// Re-stamp the fingerprint so the ONLY defect is the revision:
				// this isolates the structural refusal from the tamper refusal.
				p["fingerprint"] = c.Fingerprint()
			},
			wantSub: "revision",
		},
		{
			name: "payload index disagrees with its contract",
			mutate: func(_ *testing.T, p map[string]any) {
				p["proposal_id"] = "some-other-proposal"
			},
			wantSub: "names proposal",
		},
		{
			name: "stale revision behind an older snapshot",
			mutate: func(_ *testing.T, p map[string]any) {
				p["revision"] = float64(7)
			},
			wantSub: "names revision",
		},
		{
			name: "identity from a different objective",
			mutate: func(t *testing.T, p map[string]any) {
				var c proposal.Contract
				if err := json.Unmarshal([]byte(p["snapshot"].(string)), &c); err != nil {
					t.Fatal(err)
				}
				// Mutate payload AND body so they agree: this isolates the
				// identity check from the payload-consistency check.
				c.ID = "run-1:deadbeefdead"
				raw, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				p["snapshot"] = string(raw)
				p["proposal_id"] = string(c.ID)
				p["fingerprint"] = c.Fingerprint()
			},
			wantSub: "this objective",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := orchestrationWebWorkspace(t)
			store := openLedger(t, root)
			p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
			d := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt",
				WithLedger(store, "session-tamper"))
			if _, err := d.Run(context.Background(), recoveryObjective); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if b := d.Boundary(); b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
				t.Fatalf("no adaptive boundary: %+v", b)
			}
			mutateLedgerPayload(t, store.LedgerPath(), durable.EventProposalSnapshot, tc.mutate)

			fresh := reopenLedger(t, store)
			d2 := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt",
				WithLedger(fresh, "session-tamper"))
			restored, err := d2.RestoreProposalFromLedger(recoveryObjective)
			if restored || err == nil {
				t.Fatalf("restored=%v err=%v, want a refusal containing %q", restored, err, tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("refusal = %v, want it to name %q", err, tc.wantSub)
			}
			// The refusal is loud on the authoritative path too: the run does
			// not quietly proceed as though nothing had been recorded.
			if _, rerr := d2.Run(context.Background(), recoveryObjective); rerr == nil {
				t.Fatal("Run accepted an objective whose durable proposal cannot be trusted")
			}
		})
	}
}

// TestRestoreRefusesASnapshotForAnotherObjective binds a snapshot to exactly one
// objective: a durable task that belongs to a different intent can never adopt
// someone else's proposal.
func TestRestoreRefusesASnapshotForAnotherObjective(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	store := openLedger(t, root)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	d := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt",
		WithLedger(store, "session-foreign"))
	if _, err := d.Run(context.Background(), recoveryObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}

	fresh := reopenLedger(t, store)
	d2 := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt",
		WithLedger(fresh, "session-foreign"))
	// The same workspace, a DIFFERENT objective: there is no durable task for
	// it, so there is nothing to adopt.
	restored, err := d2.RestoreProposalFromLedger("modernise the stylesheet only")
	if err != nil {
		t.Fatalf("RestoreProposalFromLedger: %v", err)
	}
	if restored {
		t.Fatal("a proposal recorded for one objective was restored for another")
	}
}

// TestRestoreRefusesADecisionWithoutHumanLineage is the authorization
// boundary: a snapshot that claims a decision the journal never witnessed is
// refused, because a snapshot is not evidence that a human decided anything.
func TestRestoreRefusesADecisionWithoutHumanLineage(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	store := openLedger(t, root)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	d := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt",
		WithLedger(store, "session-provenance"))
	if _, err := d.Run(context.Background(), recoveryObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if _, err := d.ResumeAdaptiveProposal(context.Background(), b.ProposalFingerprint, []ProposalDecisionInput{
		{QuestionID: "scope", OptionID: "evidence-bound"},
	}); err != nil {
		t.Fatalf("ResumeAdaptiveProposal: %v", err)
	}
	// Remove every HUMAN_DECISION record while leaving the snapshot in place.
	// The proposal snapshot alone must not be enough to restore the decision.
	if !dropLedgerEvents(t, store.LedgerPath(), durable.EventHumanDecision) {
		t.Fatal("no HUMAN_DECISION record was removed — the fixture is not exercising the boundary")
	}

	fresh := reopenLedger(t, store)
	d2 := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt",
		WithLedger(fresh, "session-provenance"))
	restored, err := d2.RestoreProposalFromLedger(recoveryObjective)
	if restored || err == nil {
		t.Fatalf("restored=%v err=%v, want a refusal for a decision with no human lineage", restored, err)
	}
	if !strings.Contains(err.Error(), "HUMAN_DECISION") {
		t.Fatalf("refusal = %v, want it to name the missing human lineage", err)
	}
}

// TestRestoreSkipsARetiredTask keeps a finished objective finished: a snapshot
// beside a terminal durable task is not resumed.
func TestRestoreSkipsARetiredTask(t *testing.T) {
	root, _, adapter, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	store := openLedger(t, root)
	d := NewDriver(adapter, nil, WithLedger(store, "session-retired"))
	objective := "change bar to qux @note.txt"
	term, err := d.Run(context.Background(), objective)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated early: %+v", term)
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if st, _ := store.State(d.ledgerTaskID(objective)); !st.Status.Terminal() {
		t.Fatalf("status = %s, want a retired task", st.Status)
	}
	// Record a proposal snapshot as though one had been parked beside it, then
	// retire it: the retired task is authoritative and the snapshot is stale.
	if err := store.RecordCustomEvent(d.ledgerTaskID(objective), durable.EventProposalSnapshot, map[string]any{
		"proposal_id": "stale", "revision": float64(1), "fingerprint": "x", "snapshot": "{}",
	}); err != nil {
		t.Fatalf("record snapshot: %v", err)
	}

	fresh := reopenLedger(t, store)
	d2 := NewDriver(adapter, nil, WithLedger(fresh, "session-retired"))
	restored, err := d2.RestoreProposalFromLedger(objective)
	if err != nil {
		t.Fatalf("RestoreProposalFromLedger: %v", err)
	}
	if restored {
		t.Fatal("a retired objective's proposal was resumed")
	}
}

// ── ledger rewriting helpers (test-only) ───────────────────────────────────

// mutateLedgerPayload rewrites the payload of every event of the given type in
// the journal on disk. It is how a tampered line is simulated without having to
// guess at the bytes.
func mutateLedgerPayload(t *testing.T, path string, typ durable.EventType, mutate func(*testing.T, map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	changed := false
	for i, line := range lines {
		var ev durable.LedgerEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.EventType != typ {
			continue
		}
		mutate(t, ev.Payload)
		out, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		lines[i] = string(out)
		changed = true
	}
	if !changed {
		t.Fatalf("no %s event found in %s to mutate", typ, path)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// dropLedgerEvents removes every event of the given type from the journal on
// disk and reports whether any were present.
func dropLedgerEvents(t *testing.T, path string, typ durable.EventType) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	removed := false
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev durable.LedgerEvent
		if err := json.Unmarshal([]byte(line), &ev); err == nil && ev.EventType == typ {
			removed = true
			continue
		}
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return removed
}

// TestCustomEventReadBackSurvivesReplay pins the store side of the round trip:
// the lineage a fresh store reconstructs from disk is the lineage the live
// store appended, and the bound is reported rather than hidden.
func TestCustomEventReadBackSurvivesReplay(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	store := openLedger(t, root)
	if err := store.RecordCustomEvent("t1", durable.EventHumanDecision, map[string]any{"action": "approve"}); err != nil {
		t.Fatalf("record human decision: %v", err)
	}
	if err := store.RecordCustomEvent("t1", durable.EventProposalSnapshot, map[string]any{"revision": 2}); err != nil {
		t.Fatalf("record snapshot: %v", err)
	}
	if got := store.CustomEvents("t1"); len(got) != 2 {
		t.Fatalf("live read-back = %d events, want 2", len(got))
	}
	if got := store.CustomEvents("t1", durable.EventHumanDecision); len(got) != 1 {
		t.Fatalf("filtered read-back = %d, want 1", len(got))
	}
	if store.CustomEventsTruncated("t1") {
		t.Fatal("a two-event journal reported a truncated window")
	}

	fresh := reopenLedger(t, store)
	after := fresh.CustomEvents("t1", durable.EventHumanDecision)
	if len(after) != 1 || after[0].Payload["action"] != "approve" {
		t.Fatalf("replayed read-back = %+v, want the appended HUMAN_DECISION", after)
	}
	if got := fresh.CustomEvents("t1", durable.EventProposalSnapshot); len(got) != 1 || got[0].Payload["revision"] != float64(2) {
		t.Fatalf("replayed snapshot = %+v, want revision 2", got)
	}
	// A task with no lineage is an absence, not an error.
	if got := fresh.CustomEvents("never-existed"); got != nil {
		t.Fatalf("unknown task returned %+v", got)
	}
	if got := fresh.CustomEvents(""); got != nil {
		t.Fatalf("empty task id returned %+v", got)
	}
}

// TestCustomEventRetentionIsBounded proves the retention window cannot grow
// without limit and that its bound is honestly reported.
func TestCustomEventRetentionIsBounded(t *testing.T) {
	root := t.TempDir()
	store := openLedger(t, root)
	const written = 520 // one past the retention bound
	for i := 0; i < written; i++ {
		if err := store.RecordCustomEvent("bounded", durable.EventHumanDecision, map[string]any{"n": i}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	kept := store.CustomEvents("bounded")
	if len(kept) > 512 {
		t.Fatalf("retained %d events, want at most the retention bound", len(kept))
	}
	if !store.CustomEventsTruncated("bounded") {
		t.Fatal("a journal past the retention bound did not report truncation")
	}
	// The window keeps the TAIL, which is what a resume reads.
	last, ok := kept[len(kept)-1].Payload["n"].(int)
	if !ok {
		last = int(kept[len(kept)-1].Payload["n"].(float64))
	}
	if last != written-1 {
		t.Fatalf("the retention window dropped the newest event: %+v", kept[len(kept)-1])
	}
}

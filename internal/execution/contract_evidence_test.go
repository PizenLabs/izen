package execution

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── Contract identity derivation ────────────────────────────────────────────

// TestContractIdentityDeterministicAndDivergenceForking proves the identity
// rules: equal intents derive equal ContractIDs; ANY material change (prompt,
// target set, strategy, context digest) forks a different ContractID.
func TestContractIdentityDeterministicAndDivergenceForking(t *testing.T) {
	base := ExecuteRequest{
		Prompt:   "change bar to qux",
		Strategy: &strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation},
	}
	key := deriveContractKey(base, "digest-a", []string{"note.txt"})
	id1 := contractID(sealContractIdentity("", key))
	id2 := contractID(sealContractIdentity("", key))
	if id1 != id2 || id1.IsZero() {
		t.Fatalf("equal intents must derive one deterministic identity: %s vs %s", id1, id2)
	}
	if !strings.HasPrefix(id1.String(), "ct-") {
		t.Fatalf("contract identity %q must be content-addressed with the ct- prefix", id1)
	}

	cases := []struct {
		name string
		mut  func(k *contractIdentityKey)
	}{
		{"prompt-changed", func(k *contractIdentityKey) { k.Prompt = "change bar to baz" }},
		{"strategy-changed", func(k *contractIdentityKey) { k.Strategy = string(strategy.MultiFilePlanning) }},
		{"context-digest-changed", func(k *contractIdentityKey) { k.ContextDigest = "digest-b" }},
		{"target-appended", func(k *contractIdentityKey) { k.Targets = []string{"note.txt", "other.txt"} }},
		{"target-renamed", func(k *contractIdentityKey) { k.Targets = []string{"note2.txt"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forked := key
			tc.mut(&forked)
			if got := contractID(sealContractIdentity("", forked)); got == id1 {
				t.Fatalf("material change (%s) must fork a NEW ContractID, got %s again", tc.name, got)
			}
		})
	}

	// Parent linkage is part of a recovery child's identity: a child can never
	// collide with its parent's ID even for identical payloads.
	if child := contractID(sealContractIdentity(id1.String(), key)); child == id1 {
		t.Fatal("recovery child identity collided with its parent's identity")
	}
}

// TestContractRegistryRetryKeepsContractIncrementsAttempt is THE identity
// separation invariant: retries of one intent keep the ContractID immutable
// while AttemptID increments deterministically 1, 2, 3, ….
func TestContractRegistryRetryKeepsContractIncrementsAttempt(t *testing.T) {
	r := NewContractRegistry()
	req := ExecuteRequest{
		Prompt:   "change bar to qux",
		Strategy: &strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation},
	}

	var firstID ContractID
	for wantAttempt := AttemptID(1); wantAttempt <= 3; wantAttempt++ {
		c, attempt, err := r.Resolve(req, "digest-a", []string{"note.txt"})
		if err != nil {
			t.Fatalf("resolve attempt %d: %v", wantAttempt, err)
		}
		if attempt != wantAttempt {
			t.Fatalf("attempt = %d, want %d (deterministic increment)", attempt, wantAttempt)
		}
		if firstID.IsZero() {
			firstID = c.ID()
		} else if c.ID() != firstID {
			t.Fatalf("retry rewrote ContractID: %s -> %s", firstID, c.ID())
		}
		if c.ParentID() != "" || len(c.CausalAncestry()) != 0 || c.RecoveryDepth() != 0 {
			t.Fatalf("root contract carries causal baggage: parent=%q ancestry=%v depth=%d",
				c.ParentID(), c.CausalAncestry(), c.RecoveryDepth())
		}
	}

	// The registry never rewrites an admitted contract's sealed fields.
	c := r.Contract(firstID)
	if c == nil || !r.Admitted(firstID) {
		t.Fatal("admitted contract missing from registry")
	}
	if c.Strategy() != string(strategy.TargetedMutation) || c.ContextDigest() != "digest-a" {
		t.Fatalf("sealed contract fields drifted: %+v", c)
	}
}

// TestContractRegistryParameterChangeForksNewContract proves that parameter
// drift necessarily instantiates a NEW ContractID — in-place modification of a
// contract under a changed intent is structurally impossible.
func TestContractRegistryParameterChangeForksNewContract(t *testing.T) {
	r := NewContractRegistry()
	profile := &strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation}
	req := ExecuteRequest{Prompt: "change bar to qux", Strategy: profile}

	c1, a1, err := r.Resolve(req, "digest-a", []string{"note.txt"})
	if err != nil || a1 != 1 {
		t.Fatalf("initial resolve: %v attempt=%d", err, a1)
	}

	// Strategy shift → new contract, fresh attempt numbering.
	stratShift := &strategy.ExecutionStrategyProfile{Strategy: strategy.MultiFilePlanning}
	c2, a2, err := r.Resolve(ExecuteRequest{Prompt: req.Prompt, Strategy: stratShift}, "digest-a", []string{"a.txt", "b.txt"})
	if err != nil {
		t.Fatalf("strategy-shift resolve: %v", err)
	}
	if c2.ID() == c1.ID() {
		t.Fatal("strategy shift reused the original ContractID — contracts must fork on material change")
	}
	if a2 != 1 {
		t.Fatalf("new contract attempt numbering must restart at 1, got %d", a2)
	}

	// Target change → new contract.
	c3, _, err := r.Resolve(req, "digest-a", []string{"other.txt"})
	if err != nil {
		t.Fatalf("target-change resolve: %v", err)
	}
	if c3.ID() == c1.ID() || c3.ID() == c2.ID() {
		t.Fatal("target change reused an existing ContractID")
	}

	// Context digest change → new contract (the frozen payload changed).
	c4, _, err := r.Resolve(req, "digest-b", []string{"note.txt"})
	if err != nil {
		t.Fatalf("digest-change resolve: %v", err)
	}
	if c4.ID() == c1.ID() {
		t.Fatal("context change reused the original ContractID")
	}
}

// ── Causal recovery modeling ────────────────────────────────────────────────

// TestCausalRecoveryAppendsNewLinkedContract proves recovery semantics: a
// recovery request instantiates a NEW contract whose causal back-pointer and
// ancestry reference the failed parent, while the parent stays byte-identical
// (append-only history).
func TestCausalRecoveryAppendsNewLinkedContract(t *testing.T) {
	r := NewContractRegistry()
	parentReq := ExecuteRequest{
		Prompt:   "change bar to qux",
		Strategy: &strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation},
	}
	parent, pAttempt, err := r.Resolve(parentReq, "digest-a", []string{"note.txt"})
	if err != nil {
		t.Fatalf("parent resolve: %v", err)
	}
	before := parent.ID()

	// Material change (strategy shift) + explicit causal pointer → append-only
	// recovery child.
	child, cAttempt, err := r.Resolve(ExecuteRequest{
		Prompt:     parentReq.Prompt,
		Targets:    []string{"note.txt"},
		Strategy:   &strategy.ExecutionStrategyProfile{Strategy: strategy.DirectDeterministic},
		RecoveryOf: before.String(),
	}, "digest-a", []string{"note.txt"})
	if err != nil {
		t.Fatalf("recovery resolve: %v", err)
	}
	if child.ID() == parent.ID() {
		t.Fatal("recovery must instantiate a NEW ContractID, never rewrite the failed one")
	}
	if child.ParentID() != parent.ID() {
		t.Fatalf("recovery back-pointer = %q, want parent %q", child.ParentID(), parent.ID())
	}
	anc := child.CausalAncestry()
	if len(anc) != 1 || anc[0] != parent.ID() {
		t.Fatalf("recovery ancestry = %v, want [%s]", anc, parent.ID())
	}
	if child.RecoveryDepth() != 1 {
		t.Fatalf("recovery depth = %d, want 1", child.RecoveryDepth())
	}
	if cAttempt != 1 {
		t.Fatalf("recovery contract attempt numbering must start at 1, got %d", cAttempt)
	}
	if !child.IsRecovery() || parent.IsRecovery() {
		t.Fatal("IsRecovery misclassified the causal chain")
	}

	// The failed parent is UNTOUCHED: same identity, same attempt counter.
	if r.Contract(before).ID() != before {
		t.Fatal("failed contract was rewritten during recovery")
	}
	if got := r.Attempts(parent.ID()); got != pAttempt {
		t.Fatalf("parent attempt counter changed during recovery: %d -> %d", pAttempt, got)
	}
}

// TestRecoveryChainBoundedAtAdmission proves the bounded recovery chain:
// automatic recovery deeper than MaxRecoveryChainDepth fails closed at
// admission, so infinite automatic recovery loops are structurally impossible.
func TestRecoveryChainBoundedAtAdmission(t *testing.T) {
	r := NewContractRegistry()
	root, _, err := r.Resolve(ExecuteRequest{
		Prompt:   "fix it",
		Strategy: &strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation},
	}, "digest-root", []string{"note.txt"})
	if err != nil {
		t.Fatalf("root resolve: %v", err)
	}

	current := root
	for depth := 1; depth <= MaxRecoveryChainDepth; depth++ {
		next, _, err := r.Resolve(ExecuteRequest{
			Prompt:     "fix it",
			Strategy:   &strategy.ExecutionStrategyProfile{Strategy: strategy.HumanClarification},
			RecoveryOf: current.ID().String(),
		}, "digest-root", []string{"note.txt"})
		if err != nil {
			t.Fatalf("recovery depth %d refused before the bound: %v", depth, err)
		}
		if next.RecoveryDepth() != depth {
			t.Fatalf("recovery depth = %d, want %d", next.RecoveryDepth(), depth)
		}
		current = next
	}

	// One more automatic recovery would exceed the bound: fail closed.
	_, _, err = r.Resolve(ExecuteRequest{
		Prompt:     "fix it",
		Strategy:   &strategy.ExecutionStrategyProfile{Strategy: strategy.HumanClarification},
		RecoveryOf: current.ID().String(),
	}, "digest-root", []string{"note.txt"})
	if !errors.Is(err, ErrRecoveryChainExhausted) {
		t.Fatalf("recovery beyond bound must fail closed with ErrRecoveryChainExhausted, got %v", err)
	}
}

// TestUnknownParentContractFailsClosed proves ancestry can never be invented:
// a RecoveryOf pointer to a contract the runtime never admitted fails closed.
func TestUnknownParentContractFailsClosed(t *testing.T) {
	r := NewContractRegistry()
	_, _, err := r.Resolve(ExecuteRequest{
		Prompt:     "fix it",
		Strategy:   &strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation},
		RecoveryOf: "ct-forgedforgedforg",
	}, "digest-x", []string{"note.txt"})
	if !errors.Is(err, ErrUnknownParentContract) {
		t.Fatalf("unknown parent must fail closed with ErrUnknownParentContract, got %v", err)
	}
}

// TestRecoveryPointerPureRetryStaysSameContract proves the retry/recovery
// split through the causal pointer: a pure retry carrying RecoveryOf with an
// unchanged derived identity stays under the SAME contract (attempt++), while
// only a material change appends a new causal step.
func TestRecoveryPointerPureRetryStaysSameContract(t *testing.T) {
	r := NewContractRegistry()
	base := ExecuteRequest{
		Prompt:   "change bar to qux",
		Strategy: &strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation},
	}
	parent, pAttempt, err := r.Resolve(base, "digest-a", []string{"note.txt"})
	if err != nil {
		t.Fatalf("initial: %v", err)
	}

	// Pure retry via the causal pointer: SAME contract, next attempt.
	retry := base
	retry.RecoveryOf = parent.ID().String()
	same, attempt, err := r.Resolve(retry, "digest-a", []string{"note.txt"})
	if err != nil {
		t.Fatalf("pure retry resolve: %v", err)
	}
	if same.ID() != parent.ID() || attempt != pAttempt+1 {
		t.Fatalf("pure retry forked identity or lost the attempt increment: contract=%s attempt=%d", same.ID(), attempt)
	}
}

// TestContractRegistryConcurrentResolve is the race-detector proof that
// identity accounting is race-free: concurrent resolutions of one intent
// produce a dense attempt sequence with one stable ContractID.
func TestContractRegistryConcurrentResolve(t *testing.T) {
	r := NewContractRegistry()
	req := ExecuteRequest{
		Prompt:   "concurrent intent",
		Strategy: &strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedReasoning},
	}

	const workers = 8
	var wg sync.WaitGroup
	ids := make([]ContractID, workers)
	attempts := make([]AttemptID, workers)
	errCh := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			c, a, err := r.Resolve(req, "digest-c", []string{"note.txt"})
			if err != nil {
				errCh <- err
				return
			}
			ids[w], attempts[w] = c.ID(), a
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent resolve: %v", err)
	}
	for w := 1; w < workers; w++ {
		if ids[w] != ids[0] {
			t.Fatalf("worker %d resolved a different ContractID: %s vs %s", w, ids[w], ids[0])
		}
	}
	seen := make(map[AttemptID]bool, workers)
	for _, a := range attempts {
		if seen[a] {
			t.Fatalf("duplicate AttemptID %d handed out concurrently", a)
		}
		seen[a] = true
	}
	if got := r.Attempts(ids[0]); got != workers {
		t.Fatalf("final attempt counter = %d, want %d", got, workers)
	}
}

// ── ExecutionEvidence ───────────────────────────────────────────────────────

// TestExecutionEvidenceImmutableSealedRecord proves the evidence primitive:
// every field is fixed at construction, slice inputs are deep-copied, and no
// caller-side mutation can rewrite sealed history.
func TestExecutionEvidenceImmutableSealedRecord(t *testing.T) {
	parentAnc := []ContractID{"ct-root00000000000"}
	contract := &ExecutionContract{
		id:       "ct-child00000000001",
		parent:   "ct-parent000000002",
		ancestry: parentAnc,
		depth:    1,
	}
	targets := []string{"a.txt", "b.txt"}
	outcomes := []MutationEvidence{{File: "a.txt", Outcome: OutcomeChanged, ApplyExecuted: true, FilesystemChanged: true}}
	start := time.Now().Add(-time.Second)
	end := time.Now()

	ev := sealEvidence(
		contract.id, 7, contract, "digest-z",
		EvidenceCommitted,
		MutationSetSummary{TransactionID: "ms-1", Targets: targets, FilesMutated: 1, ApplyExecuted: true},
		start, end,
	)

	// Caller-side mutation of the ORIGINAL slices must not affect the seal.
	targets[0] = "MUTATED"
	outcomes[0].File = "MUTATED"
	parentAnc[0] = "ct-MUTATED00000"

	if ev.AttemptID() != 7 || ev.Outcome() != EvidenceCommitted || ev.ContextDigest() != "digest-z" {
		t.Fatalf("evidence core fields drifted: %+v", ev)
	}
	if ev.Mutations().Targets[0] != "b.txt" && ev.Mutations().Targets[0] != "a.txt" {
		t.Fatalf("mutation summary aliased caller memory: %v", ev.Mutations().Targets)
	}
	if got := ev.CausalAncestry(); len(got) != 1 || got[0] != "ct-root00000000000" {
		t.Fatalf("ancestry aliased caller memory: %v", got)
	}
	// Mutating the returned copy must not affect the sealed record either.
	got := ev.CausalAncestry()
	got[0] = "ct-tamper0000000"
	if ev.CausalAncestry()[0] != "ct-root00000000000" {
		t.Fatal("CausalAncestry exposed mutable internal state")
	}
	ms := ev.Mutations()
	ms.Targets[0] = "tampered"
	if ev.Mutations().Targets[0] == "tampered" {
		t.Fatal("Mutations exposed mutable internal state")
	}
	if !ev.FinishedAt().Equal(end) || !ev.StartedAt().Equal(start) {
		t.Fatal("evidence time window drifted")
	}
	if !ev.Authoritative() {
		t.Fatal("committed untainted evidence must be authoritative")
	}

	// Nil receivers are safe and non-authoritative.
	var nilEv *ExecutionEvidence
	if nilEv.Authoritative() || nilEv.Outcome().Terminal() {
		t.Fatal("nil evidence must not project as authoritative truth")
	}
}

// TestEvidenceOutcomeMappingIsTotalAndFailClosed locks the outcome mapping:
// committed families map COMMITTED, cancellations map CANCELLED, and every
// failure vocabulary item maps FAILED. A terminal error NEVER coexists with
// COMMITTED (no soft-success signals), and ABORTED_OCC is never derived.
func TestEvidenceOutcomeMappingIsTotalAndFailClosed(t *testing.T) {
	committedFamily := []MutationOutcome{OutcomeChanged, OutcomeCreated, OutcomeNoChange, OutcomeCompleted}
	cancelledFamily := []MutationOutcome{OutcomeCancelled, OutcomeRejected}
	failedFamily := []MutationOutcome{
		OutcomePatchGenerationFailed, OutcomeArtifactRejected,
		OutcomeArtifactRetryableRejected, OutcomeTruncated, OutcomeApplyFailed,
		OutcomeVerifyFailed, OutcomeSkipped, OutcomeFailed, MutationOutcome("something_unheard_of"),
	}

	for _, o := range committedFamily {
		if got := evidenceOutcomeFor(o, nil); got != EvidenceCommitted {
			t.Errorf("%s without error => %s, want COMMITTED", o, got)
		}
		if got := evidenceOutcomeFor(o, errors.New("terminal error")); got != EvidenceFailed {
			t.Errorf("%s WITH terminal error => %s, want FAILED (no soft success)", o, got)
		}
	}
	for _, o := range cancelledFamily {
		if got := evidenceOutcomeFor(o, nil); got != EvidenceCancelled {
			t.Errorf("%s => %s, want CANCELLED", o, got)
		}
	}
	for _, o := range failedFamily {
		if got := evidenceOutcomeFor(o, nil); got != EvidenceFailed {
			t.Errorf("%s => %s, want FAILED", o, got)
		}
	}

	// NO_ARTIFACT is the deterministic zero-mutation COMPLETION outcome (the
	// only nil-error producer is the deterministic strategy branch): it
	// commits an empty mutation truthfully. With a terminal error it can
	// never be a soft success.
	if got := evidenceOutcomeFor(OutcomeNoArtifact, nil); got != EvidenceCommitted {
		t.Errorf("no_artifact completion => %s, want COMMITTED", got)
	}
	if got := evidenceOutcomeFor(OutcomeNoArtifact, errors.New("boom")); got != EvidenceFailed {
		t.Errorf("no_artifact with terminal error => %s, want FAILED", got)
	}
}

// TestMutationSetSummaryTaintRules locks the partial-truth taint elimination:
// rolled-back partial writes on a failed attempt are TAINTED; durable commits
// are clean; unverified applies are tainted.
func TestMutationSetSummaryTaintRules(t *testing.T) {
	applied := MutationEvidence{File: "a.txt", Outcome: OutcomeChanged, ApplyExecuted: true, FilesystemChanged: true}
	untouched := MutationEvidence{File: "b.txt", Outcome: OutcomeNoArtifact}

	t.Run("partial-mutations-on-failure-are-tainted", func(t *testing.T) {
		s := summarizeMutationSet("ms-1", []string{"a.txt"}, []MutationEvidence{applied}, OutcomeApplyFailed, false, false)
		if !s.Tainted || s.FilesMutated != 0 {
			t.Fatalf("failed apply with applied writes must be tainted with zero durable files: %+v", s)
		}
	})
	t.Run("verify-failure-after-apply-is-tainted", func(t *testing.T) {
		s := summarizeMutationSet("ms-1", []string{"a.txt"}, []MutationEvidence{applied}, OutcomeVerifyFailed, true, false)
		if !s.Tainted {
			t.Fatalf("apply that never passed its gate must be tainted: %+v", s)
		}
	})
	t.Run("clean-commit-is-untainted-and-counts-files", func(t *testing.T) {
		s := summarizeMutationSet("ms-2", []string{"a.txt"}, []MutationEvidence{applied}, OutcomeChanged, false, false)
		if s.Tainted || s.FilesMutated != 1 || !s.ApplyExecuted {
			t.Fatalf("committed mutation miscounted: %+v", s)
		}
	})
	t.Run("rolled-back-write-is-not-durable-truth", func(t *testing.T) {
		rb := MutationEvidence{File: "c.txt", Outcome: OutcomeApplyFailed, ApplyExecuted: true, FilesystemChanged: false}
		s := summarizeMutationSet("ms-3", []string{"c.txt"}, []MutationEvidence{rb}, OutcomeApplyFailed, false, false)
		if s.FilesMutated != 0 {
			t.Fatalf("rolled-back write counted as durable truth: %+v", s)
		}
	})
	t.Run("no-apply-no-taint", func(t *testing.T) {
		s := summarizeMutationSet("", []string{"b.txt"}, []MutationEvidence{untouched}, OutcomePatchGenerationFailed, false, false)
		if s.Tainted || s.ApplyExecuted {
			t.Fatalf("attempt that never applied must stay untainted: %+v", s)
		}
	})
}

// ── End-to-end executor wiring ──────────────────────────────────────────────

// execFixture builds a scripted runtime over one target file, wired with a
// trivial verifier and a valid authorization token (the full production gate
// chain).
type execFixture struct {
	root     string
	x        *RuntimeExecutor
	provider *mockProvider
}

func newExecFixture(t *testing.T) *execFixture {
	t.Helper()
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	f := &execFixture{root: root, provider: &mockProvider{}}
	f.x = NewRuntimeExecutor(root, config.Default(), f.provider, nil, "")
	f.x.SetVerifier(trivialVerifier(root))
	f.x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	return f
}

// readOnlyReq builds a read-only reasoning request: it terminates immediately
// with sealed evidence (no approval gate).
func (f *execFixture) readOnlyReq(id, prompt string) ExecuteRequest {
	return ExecuteRequest{
		RequestID: id,
		Mode:      "ask",
		Prompt:    prompt,
		Target:    "note.txt",
		Strategy: &strategy.ExecutionStrategyProfile{
			Strategy:      strategy.TargetedReasoning,
			ModelRequired: true,
		},
	}
}

// mutationReq builds a targeted-mutation request (stops at the approval gate
// when its artifact is valid).
func (f *execFixture) mutationReq(id, prompt string) ExecuteRequest {
	return ExecuteRequest{
		RequestID: id,
		Mode:      "build",
		Prompt:    prompt,
		Target:    "note.txt",
		Strategy: &strategy.ExecutionStrategyProfile{
			Strategy:      strategy.TargetedMutation,
			ModelRequired: true,
		},
	}
}

// deterministicReq builds a zero-model deterministic request.
func (f *execFixture) deterministicReq(id, prompt string) ExecuteRequest {
	return ExecuteRequest{
		RequestID: id,
		Mode:      "build",
		Prompt:    prompt,
		Target:    "note.txt",
		Strategy: &strategy.ExecutionStrategyProfile{
			Strategy:      strategy.DirectDeterministic,
			Deterministic: true,
		},
	}
}

// TestExecutorRetriesKeepContractIncrementAttempt drives REAL executions
// through the RuntimeExecutor: two identical submissions resolve to ONE
// ContractID with AttemptIDs 1 and 2, both sealing terminal evidence whose
// identity matches the stamped proof.
func TestExecutorRetriesKeepContractIncrementAttempt(t *testing.T) {
	f := newExecFixture(t)
	f.provider.responses = []*ai.Response{{Content: "analysis: replace bar with qux"}, {Content: "analysis: replace bar with qux"}}

	var ids [2]string
	var attempts [2]uint32
	for i := 0; i < 2; i++ {
		res, err := f.x.Execute(context.Background(), f.readOnlyReq("retry-contract", "change bar to qux"))
		if err != nil || res.Err != nil {
			t.Fatalf("execute %d: %v / %v", i, err, res.Err)
		}
		ev := res.Evidence
		if ev == nil {
			t.Fatalf("execute %d sealed NO terminal evidence", i)
		}
		if ev.Outcome() != EvidenceCommitted {
			t.Fatalf("read-only completion outcome = %s, want COMMITTED", ev.Outcome())
		}
		ids[i] = ev.ContractID().String()
		attempts[i] = uint32(ev.AttemptID())
		if attempts[i] != uint32(i+1) {
			t.Fatalf("execute %d attempt = %d, want %d", i, attempts[i], i+1)
		}
		if res.Proof.ContractID != ids[i] || res.Proof.AttemptID != attempts[i] {
			t.Fatalf("proof identity disagrees with sealed evidence: proof(%s,%d) evidence(%s,%d)",
				res.Proof.ContractID, res.Proof.AttemptID, ids[i], attempts[i])
		}
	}
	if ids[0] != ids[1] || ids[0] == "" {
		t.Fatalf("retries must share one immutable ContractID: %s vs %s", ids[0], ids[1])
	}
}

// TestExecutorParameterChangeForksContractThroughRealExecutions proves the
// executor derives identity from actual request content: a prompt change
// forks a new contract even though nothing else moved.
func TestExecutorParameterChangeForksContractThroughRealExecutions(t *testing.T) {
	f := newExecFixture(t)
	f.provider.responses = []*ai.Response{{Content: "a"}, {Content: "b"}}

	r1, err := f.x.Execute(context.Background(), f.readOnlyReq("fork-a", "change bar to qux"))
	if err != nil || r1.Err != nil {
		t.Fatalf("execute a: %v / %v", err, r1.Err)
	}
	r2, err := f.x.Execute(context.Background(), f.readOnlyReq("fork-b", "change bar to baz instead"))
	if err != nil || r2.Err != nil {
		t.Fatalf("execute b: %v / %v", err, r2.Err)
	}
	if r1.Evidence.ContractID() == r2.Evidence.ContractID() {
		t.Fatal("parameter change kept the same ContractID — identity derivation is broken")
	}
}

// TestExecutorApprovalGateHoldsEvidenceUntilTermination proves the approval
// gate is NOT a termination: no evidence exists while held, and Approve/Reject
// resolve the SAME contract attempt.
func TestExecutorApprovalGateHoldsEvidenceUntilTermination(t *testing.T) {
	f := newExecFixture(t)
	f.provider.responses = []*ai.Response{{Content: sampleReplace}}

	res, err := f.x.Execute(context.Background(), f.mutationReq("gate-hold", "change bar to qux"))
	if err != nil || res.PendingPatchID == "" {
		t.Fatalf("expected approval-gate hold: %v / %q", err, res.PendingPatchID)
	}
	if res.Evidence != nil {
		t.Fatal("approval-held execution must NOT carry terminal evidence yet")
	}

	approved, err := f.x.Approve(context.Background(), res.PendingPatchID)
	if err != nil || approved.Err != nil {
		t.Fatalf("approve: %v / %v", err, approved.Err)
	}
	if approved.Evidence == nil {
		t.Fatal("approved execution must seal terminal evidence")
	}
	if approved.Evidence.ContractID().String() != res.Proof.ContractID ||
		approved.Evidence.AttemptID() != AttemptID(res.Proof.AttemptID) {
		t.Fatalf("approval resolved a DIFFERENT contract attempt: gate(%s,%d) approve(%s,%d)",
			res.Proof.ContractID, res.Proof.AttemptID,
			approved.Evidence.ContractID(), approved.Evidence.AttemptID())
	}
	if approved.Evidence.Outcome() != EvidenceCommitted {
		t.Fatalf("approved commit outcome = %s, want COMMITTED", approved.Evidence.Outcome())
	}
	if approved.Evidence.Mutations().FilesMutated != 1 || approved.Evidence.Mutations().Tainted {
		t.Fatalf("committed mutation summary wrong: %+v", approved.Evidence.Mutations())
	}

	// A rejection terminates as CANCELLED evidence for its own attempt.
	f.provider.responses = append(f.provider.responses, &ai.Response{Content: sampleReplace})
	res2, err := f.x.Execute(context.Background(), f.mutationReq("gate-reject", "change bar to qux"))
	if err != nil || res2.PendingPatchID == "" {
		t.Fatalf("expected second hold: %v / %q", err, res2.PendingPatchID)
	}
	rejected, err := f.x.Reject(context.Background(), res2.PendingPatchID, "human said no")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Evidence == nil || rejected.Evidence.Outcome() != EvidenceCancelled {
		t.Fatalf("rejected execution outcome = %v, want CANCELLED evidence", rejected.Evidence)
	}
	if rejected.Evidence.Authoritative() {
		t.Fatal("cancelled evidence must never be authoritative")
	}
}

// TestExecutorFailureEvidenceConveysFailureAndTaint drives a failing
// execution through the runtime and requires the sealed evidence to convey
// FAILED explicitly. The provider is exhausted, so the model stage fails with
// zero applies (untainted FAILED evidence — taint must not be speculative).
func TestExecutorFailureEvidenceConveysFailureAndTaint(t *testing.T) {
	f := newExecFixture(t)
	// No scripted responses: the provider errors on the first invocation.
	f.provider.responses = nil

	res, err := f.x.Execute(context.Background(), f.mutationReq("fail-ev", "change bar to qux"))
	if err == nil && res.Err == nil {
		t.Fatal("expected a failing execution")
	}
	if res.Evidence == nil {
		t.Fatal("failing execution MUST seal terminal evidence conveying failure")
	}
	if res.Evidence.Outcome() != EvidenceFailed {
		t.Fatalf("failure outcome = %s, want FAILED (explicit failure, no soft success)", res.Evidence.Outcome())
	}
	if res.Evidence.Authoritative() {
		t.Fatal("failed evidence must strictly block authoritative projection")
	}
	if res.Evidence.Mutations().Tainted {
		t.Fatalf("no apply ran; taint must not be raised speculatively: %+v", res.Evidence.Mutations())
	}
}

// TestExecutorRecoveryChainThroughRealExecutions drives a full causal chain
// through the RuntimeExecutor: failed contract → recovery request with
// RecoveryOf → NEW contract with explicit back-pointer and ancestry, bounded
// at MaxRecoveryChainDepth with ErrRecoveryChainExhausted surfacing as a
// fail-closed admission rejection.
func TestExecutorRecoveryChainThroughRealExecutions(t *testing.T) {
	f := newExecFixture(t)
	f.provider.responses = []*ai.Response{{Content: "analysis of the failure"}}

	first, err := f.x.Execute(context.Background(), f.readOnlyReq("chain-root", "change bar to qux"))
	if err != nil || first.Err != nil {
		t.Fatalf("root execute: %v / %v", err, first.Err)
	}
	rootID := first.Evidence.ContractID()

	// Recovery with a material strategy change (read-only → deterministic
	// zero-model) → append-only child contract.
	rec := f.deterministicReq("chain-rec", "change bar to qux")
	rec.RecoveryOf = rootID.String()
	second, err := f.x.Execute(context.Background(), rec)
	if err != nil || second.Err != nil {
		t.Fatalf("recovery execute: %v / %v", err, second.Err)
	}
	if second.Evidence.ParentContractID() != rootID {
		t.Fatalf("recovery back-pointer = %q, want %q", second.Evidence.ParentContractID(), rootID)
	}
	anc := second.Evidence.CausalAncestry()
	if len(anc) != 1 || anc[0] != rootID {
		t.Fatalf("causal ancestry = %v, want [%s]", anc, rootID)
	}
	if second.Evidence.ContractID() == rootID {
		t.Fatal("recovery reused the failed ContractID — history rewritten in place")
	}

	// Chain past the bound fails closed at admission.
	current := second.Evidence.ContractID()
	for depth := 2; depth <= MaxRecoveryChainDepth; depth++ {
		nxt := f.deterministicReq("chain-deep", "change bar to qux")
		nxt.RecoveryOf = current.String()
		res, execErr := f.x.Execute(context.Background(), nxt)
		if execErr != nil {
			t.Fatalf("bounded recovery depth %d refused early: %v", depth, execErr)
		}
		current = res.Evidence.ContractID()
	}
	exhausted := f.deterministicReq("chain-over", "change bar to qux")
	exhausted.RecoveryOf = current.String()
	if _, execErr := f.x.Execute(context.Background(), exhausted); !errors.Is(execErr, ErrRecoveryChainExhausted) {
		t.Fatalf("unbounded automatic recovery must fail closed, got %v", execErr)
	}

	// Unknown parent fails closed too.
	forged := f.readOnlyReq("chain-forge", "change bar to qux")
	forged.RecoveryOf = "ct-nonexistent00000"
	if _, execErr := f.x.Execute(context.Background(), forged); !errors.Is(execErr, ErrUnknownParentContract) {
		t.Fatalf("forged lineage must fail closed, got %v", execErr)
	}
}

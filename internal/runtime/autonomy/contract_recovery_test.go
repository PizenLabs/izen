package autonomy

// ── PHASE 15: CONTRACT RECOVERY CIRCUIT BREAKER ──────────────────────────────
//
// Phase 14 made ErrZeroArtifactsParsed repromptable. That was correct and it is
// incomplete: a repromptable outcome with no bound is a spend loop wearing a
// recovery policy as a disguise. A model that has settled on prose keeps settling
// on prose, and every attempt is a real billed provider call that learns nothing.
//
// These tests drive the real loop over a provider that answers every attempt with
// prose, and assert on the LOOP STATE. The properties worth pinning:
//
//  1. The bound is real. A model that never honours the contract terminates, and
//     it terminates quickly — not after the run-level attempt budget.
//  2. The terminal state is UNSUBSTANTIATED, with ErrContractRecoveryExhausted
//     named. Not "completed" (nothing was proven) and not "aborted" (nothing
//     failed in a way a retry would fix).
//  3. The workspace is byte-identical. Prose never reaches a file.
//  4. A model that DOES honour the contract on the second attempt still succeeds.
//     A breaker that fires on a recovering model is a breaker that is wrong.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/protocol"
)

// proseThenArtifact is a model that misses the contract once and then complies.
// The second response is the bounded SEARCH/REPLACE envelope for sampleOriginal.
var proseThenArtifact = &ai.Response{Content: sampleReplace}

// alwaysProse builds `n` identical prose responses, so a test can supply exactly
// as many attempts as it wants to exercise.
func alwaysProse(n int) []*ai.Response {
	out := make([]*ai.Response, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, &ai.Response{Content: proseResponse})
	}
	return out
}

// TestPhase15_ContractRecoveryStopsAtTheBound is the primary acceptance test: a
// model that never speaks the artifact contract must terminate, must not loop,
// and must not reach a completion.
func TestPhase15_ContractRecoveryStopsAtTheBound(t *testing.T) {
	root, mock, a, _ := testHarness(t, alwaysProse(8))
	d := NewDriver(a, nil)

	term, err := d.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("a model that never honoured the artifact contract completed the objective")
	}
	if term == nil || term.State != autonomy.RuntimeUnsubstantiated {
		t.Fatalf("termination = %+v, want %s", term, autonomy.RuntimeUnsubstantiated)
	}

	// The reason names the breaker, so the human is told this was a contract
	// problem rather than a mysterious stall.
	if !strings.Contains(term.Reason, execution.ErrContractRecoveryExhausted.Error()) {
		t.Fatalf("reason %q does not name %v", term.Reason, execution.ErrContractRecoveryExhausted)
	}

	// The bound is what stopped it: initial attempt + MaxContractRecoveryAttempts
	// re-prompts. A run that kept going would be spending against the invoice.
	used, limit, exhausted := d.ContractRecoveryState()
	if limit != MaxContractRecoveryAttempts {
		t.Fatalf("limit = %d, want %d", limit, MaxContractRecoveryAttempts)
	}
	if used != MaxContractRecoveryAttempts {
		t.Fatalf("contract recoveries spent = %d, want exactly the bound %d", used, MaxContractRecoveryAttempts)
	}
	if !exhausted {
		t.Fatal("the breaker did not latch")
	}
	if calls := mock.calls(); calls > MaxContractRecoveryAttempts+1 {
		t.Fatalf("provider calls = %d, want at most initial+%d re-prompts", calls, MaxContractRecoveryAttempts)
	}

	// Nothing was written. The whole point of the artifact boundary.
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("prose reached the workspace: %q", got)
	}
}

// TestPhase15_ContractRecoveryPreservesTheSameArtifactContract pins that the
// breaker bounds the ATTEMPTS, not the CONTRACT. A creation is never relabelled a
// patch, and the re-prompt travels the same artifact shape as the original
// attempt.
func TestPhase15_ContractRecoveryPreservesTheSameArtifactContract(t *testing.T) {
	_, mock, a, _ := testHarness(t, alwaysProse(4))
	d := NewDriver(a, nil)
	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	requests := mock.recordedRequests()
	if len(requests) < 2 {
		t.Fatalf("recorded %d request(s), want at least one recovery attempt", len(requests))
	}
	// Every attempt is dispatched under the same interaction contract. A recovery
	// that changed it would be a re-scope, and a re-scope is a different decision
	// the matrix makes for other reasons.
	var shape protocol.InteractionContract
	for i, req := range requests {
		if req.Contract == nil || req.Contract.Contract == "" {
			continue
		}
		if shape == "" {
			shape = req.Contract.Contract
			continue
		}
		if req.Contract.Contract != shape {
			t.Fatalf("attempt %d drifted to contract %q (first was %q)", i, req.Contract.Contract, shape)
		}
	}
	if shape == "" {
		t.Fatal("no attempt carried an interaction contract")
	}
	// Every attempt also re-states the same artifact contract on the SYSTEM
	// channel, which is where the Phase 15 instruction lives. A re-prompt that
	// dropped it would be re-prompting into the same ambiguity that caused the
	// miss.
	for i, req := range requests {
		if !strings.Contains(req.System, "[ARTIFACT CONTRACT") {
			t.Fatalf("attempt %d was dispatched without the strict artifact contract instruction", i)
		}
	}
}

// TestPhase15_ContractRecoveryDoesNotFireOnAComplyingModel is the negative half,
// and it is the one that makes the breaker trustworthy. A model that misses once
// and then complies must still complete its work: a breaker that fires on a
// recovering model is a breaker that is simply wrong.
func TestPhase15_ContractRecoveryDoesNotFireOnAComplyingModel(t *testing.T) {
	responses := []*ai.Response{
		{Content: proseResponse},
		proseThenArtifact,
	}
	root, _, a, _ := testHarness(t, responses)
	d := NewDriver(a, nil)

	term, err := d.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	used, _, exhausted := d.ContractRecoveryState()
	if exhausted {
		t.Fatalf("the breaker fired on a model that complied on attempt 2 (used=%d)", used)
	}
	if used > MaxContractRecoveryAttempts {
		t.Fatalf("used %d recoveries for a single contract miss", used)
	}
	// Whether the mutation itself lands is the executor's business; what this
	// test owns is that the run was not terminated by the contract breaker.
	if term != nil && term.State == autonomy.RuntimeUnsubstantiated &&
		strings.Contains(term.Reason, execution.ErrContractRecoveryExhausted.Error()) {
		t.Fatalf("a complying model was terminated by the breaker: %+v", term)
	}
	if got := readTarget(t, root, "note.txt"); got == proseResponse {
		t.Fatal("prose was written to the workspace")
	}
}

// TestPhase15_ContractRecoveryCounterIsPerLifecycle pins the reset. A breaker
// that carried its counter across runs would let a second objective inherit the
// first one's exhausted budget and terminate before it had made a single attempt.
func TestPhase15_ContractRecoveryCounterIsPerLifecycle(t *testing.T) {
	_, _, a, _ := testHarness(t, alwaysProse(8))
	d := NewDriver(a, nil)
	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	used, _, exhausted := d.ContractRecoveryState()
	if !exhausted || used == 0 {
		t.Fatalf("first run did not exhaust the breaker: used=%d exhausted=%t", used, exhausted)
	}
	// A second lifecycle starts from a zero budget. A cancelled context makes that
	// observable without spending anything: the run terminates before its first
	// dispatch, so the counter it reports IS the value the new lifecycle began
	// with.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Run(ctx, "change bar to qux @note.txt"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	used, _, exhausted = d.ContractRecoveryState()
	if used != 0 || exhausted {
		t.Fatalf("the breaker counter survived into a new lifecycle: used=%d exhausted=%t", used, exhausted)
	}
}

// TestPhase15_ContractRecoveryReasonNeverCarriesModelBytes pins Recovery
// Isolation: the terminal reason names the target and the failure class, never a
// byte of the generation the model produced. The reason travels into a human
// boundary and eventually onto a screen; echoing prose there would put the
// discarded text back in front of the reader.
func TestPhase15_ContractRecoveryReasonNeverCarriesModelBytes(t *testing.T) {
	_, _, a, _ := testHarness(t, alwaysProse(6))
	d := NewDriver(a, nil)
	term, err := d.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term == nil {
		t.Fatal("expected a termination")
	}
	for _, leak := range []string{"I have updated the file", "Here is a summary"} {
		if strings.Contains(term.Reason, leak) {
			t.Fatalf("terminal reason leaked discarded model bytes: %q", term.Reason)
		}
	}
	if !strings.Contains(term.Reason, "note.txt") {
		t.Fatalf("terminal reason must name the target it was spent on: %q", term.Reason)
	}
}

// TestPhase15_ContractRecoverySentinelIsDistinguishable pins the sentinels apart.
// "The model will not speak the contract" and "the objective was not proven" are
// different diagnoses, and collapsing them would make the first unretryable and
// the second undiagnosable.
func TestPhase15_ContractRecoverySentinelIsDistinguishable(t *testing.T) {
	if !errors.Is(execution.ErrContractRecoveryExhausted, execution.ErrContractRecoveryExhausted) {
		t.Fatal("sentinel is not comparable to itself")
	}
	if errors.Is(execution.ErrContractRecoveryExhausted, execution.ErrZeroArtifactsParsed) {
		t.Fatal("the exhaustion sentinel must not alias the per-attempt rejection")
	}
	if execution.ErrContractRecoveryExhausted.Error() == execution.ErrZeroArtifactsParsed.Error() {
		t.Fatal("the two sentinels must carry distinguishable text")
	}
}

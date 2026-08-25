// Tier 2 cross-phase chaos & integration suite.
//
// Where the phase lock suites freeze individual invariants, this suite
// stress-tests the WHOLE runtime engine (Phase 0 authority boundary through
// Phase 3 OCC) under hostile operating conditions and proves the invariants
// hold JOINTLY:
//
//  1. OCC high-concurrency race — 100 concurrent mutating execution
//     contracts over one overlapping target geometry, with an out-of-band
//     hostile writer injecting mid-execution modifications. Zero partial
//     writes may ever persist, 100% of conflicting executions must terminate
//     as tainted ABORTED_OCC evidence, and the workspace must remain
//     corruption-free under the race detector.
//
//  2. Bounded recovery-chain exhaustion — continuous deterministic failures
//     drive automatic causal recovery loops; the chain MUST halt strictly at
//     MaxRecoveryChainDepth with ErrRecoveryChainExhausted, refusing before
//     any execution stage, while parent back-pointers preserve the complete
//     acyclic causal lineage.
//
//  3. Context derivation memory stability — 1,000 Derive() steps plus
//     interleaved state-integrity recovery probes must not accumulate
//     historical snapshot nodes (GC must reclaim every released ancestor)
//     and seal-digest verification latency must stay O(1) in lineage depth.
//
//  4. Evidence-ledger projection isolation — a randomized interleaved stream
//     of 500 COMMITTED / FAILED / CANCELLED / ABORTED_OCC events must project
//     exclusively through the gate: ONLY committed untainted evidence mutates
//     target projection state; every other outcome blocks fail-closed, even
//     while readers observe the ledger mid-stream.
//
// The suite lives in the EXTERNAL test package on purpose: it drives only
// EXPORTED APIs of the runtime, so weakening any exported contract fails
// here regardless of in-package test edits. All cross-goroutine traffic uses
// race-safe primitives (mutexes, atomics, channels, wait groups); assertions
// are absolute — no skips, no ignored errors.
package architecture_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/presentation"
)

// ── shared chaos fixtures ──────────────────────────────────────────────────

const (
	chaosTargetFile     = "chaos_target.txt"
	chaosOriginal       = "alpha\nbeta\ngamma\n"
	chaosMutated        = "alpha\nzeta\ngamma\n"
	chaosSearchReplace  = "<<<<<<< SEARCH\nbeta\n=======\nzeta\n>>>>>>>"
	chaosMutationPrompt = "change beta to zeta in @" + chaosTargetFile
	chaosExternalPrefix = "external out-of-band edit #"
)

// chaosExternalEditRe matches exactly the complete states the hostile
// out-of-band writer produces. Anything else persisted to the target after
// the OCC storm is a partial-write leak or corruption.
var chaosExternalEditRe = regexp.MustCompile(`^external out-of-band edit #[0-9]+\n$`)

// chaosScriptedProvider is a race-safe ai.Provider fake serving one fixed
// SEARCH/REPLACE artifact for the first `responses` invocations and failing
// deterministically thereafter (or always, when responses is 0).
type chaosScriptedProvider struct {
	mu        sync.Mutex
	err       error
	responses int
	callCount int
}

func (p *chaosScriptedProvider) Name() string { return "chaos-mock" }

func (p *chaosScriptedProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callCount++
	if p.err != nil || p.callCount > p.responses {
		return nil, fmt.Errorf("chaosScriptedProvider: deterministic failure")
	}
	return &ai.Response{Content: chaosSearchReplace}, nil
}

func (p *chaosScriptedProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("streaming not supported by chaosScriptedProvider")
}

func (p *chaosScriptedProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callCount
}

func chaosWriteTarget(t *testing.T, root, content string) {
	t.Helper()
	path := filepath.Join(root, chaosTargetFile)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write target fixture: %v", err)
	}
}

func chaosReadTarget(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, chaosTargetFile))
	if err != nil {
		t.Fatalf("read target fixture: %v", err)
	}
	return string(data)
}

// chaosSilenceRuntimeLogs muffles the runtime's operational logging for the
// duration of one test (the OCC verifier logs every baseline/verify sweep;
// hundreds of concurrent sweeps would flood the test output).
func chaosSilenceRuntimeLogs(t *testing.T) {
	t.Helper()
	prev := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(prev) })
}

func chaosNewExecutor(root string, cfg *config.Config, provider ai.Provider) *execution.RuntimeExecutor {
	x := execution.NewRuntimeExecutor(root, cfg, provider, nil, "")
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:       authorization.NewAuthorizationID(),
		IssuedAt: time.Now(),
	})
	return x
}

// chaosFrozenMutationIntent builds an integrity-sealed targeted mutation the
// way IntentGateway.Gate would: prompt + workspace + referenced-file channels
// frozen and sealed at intent creation, bounded targeted-mutation profile
// attached. It returns the sealed digest of the admitted context snapshot so
// callers can hold terminal evidence to exactly that identity.
func chaosFrozenMutationIntent(root string, seq int) (execution.ExecuteRequest, string) {
	profile := strategy.ExecutionStrategyProfile{
		Strategy:       strategy.TargetedMutation,
		ModelRequired:  true,
		StrategyReason: "tier2 chaos lock: bounded targeted mutation",
	}
	snapshot := execution.FreezeContext("", []execution.ContextChannel{
		{Kind: execution.ContextKindUserPrompt, Name: "prompt", Content: chaosMutationPrompt},
		{Kind: execution.ContextKindEnvironment, Name: "workspace", Content: root},
		{Kind: execution.ContextKindReferencedFile, Name: chaosTargetFile},
	})
	return execution.ExecuteRequest{
		RequestID: fmt.Sprintf("chaos-occ-%03d", seq),
		Mode:      "build",
		Prompt:    chaosMutationPrompt,
		Targets:   []string{chaosTargetFile},
		Strategy:  &profile,
		Context:   snapshot,
	}, snapshot.Digest()
}

// ── SCENARIO 1 — OCC high-concurrency race ─────────────────────────────────

// TestChaos_OCC_HighConcurrencyRace drives 100 concurrent goroutines, each
// admitting an independent RuntimeExecutor with its own mutating execution
// contract over the SAME overlapping target geometry, then races them all
// through the approval gate while a hostile out-of-band writer injects
// mid-execution modifications. Because the first out-of-band write lands
// AFTER every admission baseline was captured (barrier-synchronized) and
// every subsequent write diverges, 100% of the conflicting executions must
// abort with tainted ABORTED_OCC evidence, zero partial writes may persist,
// every terminal evidence record must carry the admitted context snapshot's
// sealed ContextDigest (never an empty omission on the approval path), and
// the final workspace byte-state must be a COMPLETE state — verified
// under the race detector.
func TestChaos_OCC_HighConcurrencyRace(t *testing.T) {
	chaosSilenceRuntimeLogs(t)

	const concurrency = 100
	root := t.TempDir()
	chaosWriteTarget(t, root, chaosOriginal)
	cfg := config.Default()

	type chaosAttempt struct {
		executor   *execution.RuntimeExecutor
		pendingID  string
		wantDigest string
		setupErr   error
		approveRes *execution.ExecutionResult
		approveErr error
	}
	attempts := make([]chaosAttempt, concurrency)

	// Pre-build authorization tokens on the test goroutine; workers only
	// consume them.
	auths := make([]*authorization.MutationAuthorization, concurrency)
	for i := range auths {
		auths[i] = &authorization.MutationAuthorization{
			ID:       authorization.NewAuthorizationID(),
			IssuedAt: time.Now(),
		}
	}

	// ── Phase A: 100 concurrent mutating ExecutionContracts. Every OCC
	// baseline is captured in this window, before any out-of-band write. ──
	var admitted sync.WaitGroup
	admitted.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(i int) {
			defer admitted.Done()
			provider := &chaosScriptedProvider{responses: 1}
			x := execution.NewRuntimeExecutor(root, cfg, provider, nil, "")
			x.SetAuthorization(auths[i])
			req, admittedDigest := chaosFrozenMutationIntent(root, i)
			res, err := x.Execute(context.Background(), req)
			switch {
			case err != nil:
				attempts[i].setupErr = fmt.Errorf("execute: %w", err)
			case res.Err != nil:
				attempts[i].setupErr = fmt.Errorf("terminal execute failure: %w", res.Err)
			case res.PendingPatchID == "":
				attempts[i].setupErr = errors.New("mutating intent did not HOLD at the approval gate")
			default:
				attempts[i].executor = x
				attempts[i].pendingID = res.PendingPatchID
				attempts[i].wantDigest = admittedDigest
			}
		}(i)
	}
	admitted.Wait()

	for i := range attempts {
		if attempts[i].setupErr != nil {
			t.Fatalf("contract %03d never reached the approval gate: %v", i, attempts[i].setupErr)
		}
	}

	// ── Phase B: hostile out-of-band writer. The FIRST write completes
	// before the approval barrier opens, so every captured baseline is
	// guaranteed stale; every following write diverges further. ──
	firstDivergence := make(chan struct{})
	writerStopped := make(chan struct{})
	stopWriter := make(chan struct{})
	var failedWrites atomic.Int64
	go func() {
		defer close(writerStopped)
		seq := 0
		write := func() {
			seq++
			content := fmt.Sprintf("%s%d\n", chaosExternalPrefix, seq)
			if err := os.WriteFile(filepath.Join(root, chaosTargetFile), []byte(content), 0o644); err != nil {
				failedWrites.Add(1)
			}
		}
		write()
		close(firstDivergence)
		for {
			select {
			case <-stopWriter:
				return
			default:
				write()
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()
	<-firstDivergence

	// ── Phase C: 100 concurrent approvals against the contended target. ──
	var aborted atomic.Int64
	var resolved sync.WaitGroup
	resolved.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(i int) {
			defer resolved.Done()
			res, err := attempts[i].executor.Approve(context.Background(), attempts[i].pendingID)
			attempts[i].approveRes = res
			attempts[i].approveErr = err
			if err != nil && errors.Is(err, execution.ErrWorkspaceStateConflict) {
				aborted.Add(1)
			}
		}(i)
	}
	resolved.Wait()
	close(stopWriter)
	<-writerStopped

	if n := failedWrites.Load(); n != 0 {
		t.Fatalf("hostile writer observed %d failed out-of-band writes — the fixture lost disk control", n)
	}
	if got := aborted.Load(); got != concurrency {
		t.Fatalf("only %d/%d conflicting executions aborted as OCC conflicts — the gate leaked commits under contention", got, concurrency)
	}

	// ── Absolute per-execution invariants: 100% ABORTED_OCC, tainted,
	// non-authoritative, zero durable mutation, consumed approval surface,
	// exact conflict telemetry. ──
	for i := range attempts {
		a := attempts[i]
		if a.approveErr == nil || !errors.Is(a.approveErr, execution.ErrWorkspaceStateConflict) {
			t.Fatalf("contract %03d: want OCC-conflict abort, got err=%v", i, a.approveErr)
		}
		if a.approveRes == nil || a.approveRes.Evidence == nil {
			t.Fatalf("contract %03d: OCC abort sealed no evidence", i)
		}
		ev := a.approveRes.Evidence
		if ev.Outcome() != execution.EvidenceAbortedOCC {
			t.Fatalf("contract %03d: outcome = %q, want ABORTED_OCC", i, ev.Outcome())
		}
		if !ev.Outcome().Terminal() {
			t.Fatalf("contract %03d: ABORTED_OCC must be terminal", i)
		}
		m := ev.Mutations()
		if !m.Tainted {
			t.Fatalf("contract %03d: abort evidence is not tainted — projectors would keep tentative state", i)
		}
		if m.ApplyExecuted || m.FilesMutated != 0 {
			t.Fatalf("contract %03d: abort claims applied work: %+v", i, m)
		}
		if ev.Authoritative() {
			t.Fatalf("contract %03d: OCC-abort evidence projects as authoritative success", i)
		}
		// Hard identity assertion: terminal evidence emitted on the approval
		// path MUST carry a valid, non-empty ContextDigest matching exactly
		// the admitted context snapshot's sealed digest — never an omission.
		if ev.ContextDigest() == "" {
			t.Fatalf("contract %03d: approval-path evidence emitted with EMPTY ContextDigest", i)
		}
		if want := a.wantDigest; ev.ContextDigest() != want {
			t.Fatalf("contract %03d: evidence ContextDigest %q does not match the admitted snapshot digest %q",
				i, ev.ContextDigest(), want)
		}
		if len(ev.ContextDigest()) != 64 {
			t.Fatalf("contract %03d: ContextDigest is not a raw sha256 hex string: %q", i, ev.ContextDigest())
		}
		if ev.AttemptID() != 1 || ev.ContractID().IsZero() {
			t.Fatalf("contract %03d: abort evidence lost its identity: id=%q attempt=%d",
				i, ev.ContractID(), ev.AttemptID())
		}
		if proj := presentation.ProjectEvidence(ev); proj.Granted() {
			t.Fatalf("contract %03d: presentation gate granted authority for ABORTED_OCC: %+v", i, proj)
		}
		occ := a.executor.OCC().Metrics()
		if occ.Snapshots != 1 || occ.Verifications != 1 || occ.Mismatches != 1 || occ.ConflictsFound != 1 {
			t.Fatalf("contract %03d: OCC telemetry wrong: %+v", i, occ)
		}
		if pending := a.executor.PendingPatchIDs(); len(pending) != 0 {
			t.Fatalf("contract %03d: aborted approval surface survived: %v", i, pending)
		}
	}

	// ── Global invariant: zero partial writes, zero corruption. The only
	// bytes that ever reached the target belong to the out-of-band writer —
	// a complete state. ──
	final := chaosReadTarget(t, root)
	if !chaosExternalEditRe.MatchString(final) &&
		final != chaosOriginal && final != chaosMutated {
		t.Fatalf("PARTIAL WRITE persisted through the OCC storm: %q", final)
	}
}

// ── SCENARIO 2 — bounded recovery-chain exhaustion ─────────────────────────

// TestChaos_RecoveryChainExhaustion drives automatic causal recovery through
// CONTINUOUS deterministic execution failures: every chain step fails its
// model invocation, seals FAILED evidence, and spawns the next recovery
// contract. The chain must halt strictly at MaxRecoveryChainDepth (=4) with
// ErrRecoveryChainExhausted — refused before any execution stage, with zero
// side effects and no evidence sealed for the refused attempt — while every
// admitted contract maintains the complete, acyclic, append-only causal
// lineage through explicit parent back-pointers.
func TestChaos_RecoveryChainExhaustion(t *testing.T) {
	if execution.MaxRecoveryChainDepth != 4 {
		t.Fatalf("MaxRecoveryChainDepth drifted: %d, want 4", execution.MaxRecoveryChainDepth)
	}

	root := t.TempDir()
	chaosWriteTarget(t, root, chaosOriginal)
	provider := &chaosScriptedProvider{} // responses=0: every model invocation fails deterministically
	x := chaosNewExecutor(root, config.Default(), provider)

	chainPrompt := func(step int) string {
		return fmt.Sprintf("chaos recovery pass %02d: change beta to zeta in @%s", step, chaosTargetFile)
	}
	submit := func(requestID string, step int, recoveryOf string) (*execution.ExecutionResult, error) {
		return x.Execute(context.Background(), execution.ExecuteRequest{
			RequestID: requestID,
			Mode:      "ask",
			Prompt:    chainPrompt(step),
			Targets:   []string{chaosTargetFile},
			Strategy: &strategy.ExecutionStrategyProfile{
				Strategy:      strategy.TargetedReasoning,
				ModelRequired: true,
			},
			RecoveryOf: recoveryOf,
		})
	}

	rootRes, rootErr := submit("chaos-rec-root", 0, "")
	if rootErr == nil || rootRes == nil || rootRes.Err == nil {
		t.Fatal("fixture lost its deterministic model outage — the root execution must fail")
	}
	parent := rootRes.Evidence
	if parent == nil || parent.Outcome() != execution.EvidenceFailed {
		t.Fatalf("root failure did not seal FAILED evidence: %+v", parent)
	}
	rootID := parent.ContractID()

	lineage := []execution.ContractID{rootID}
	for depth := 1; depth <= execution.MaxRecoveryChainDepth; depth++ {
		frozenAttempts := x.Contracts().Attempts(parent.ContractID())

		res, execErr := submit(fmt.Sprintf("chaos-rec-%02d", depth), depth, parent.ContractID().String())
		if execErr == nil && (res == nil || res.Err == nil) {
			t.Fatalf("recovery depth %d completed — the deterministic failure loop broke", depth)
		}
		if res == nil || res.Evidence == nil {
			t.Fatalf("recovery depth %d sealed no evidence", depth)
		}
		ev := res.Evidence
		if ev.Outcome() != execution.EvidenceFailed {
			t.Fatalf("recovery depth %d conveyed failure as %q — partial-truth leak", depth, ev.Outcome())
		}
		if want := parent.ContractID(); ev.ParentContractID() != want {
			t.Fatalf("depth %d back-pointer = %q, want %q", depth, ev.ParentContractID(), want)
		}
		if !chaosSameLineage(ev.CausalAncestry(), lineage) {
			t.Fatalf("depth %d ancestry = %v, want the complete causal lineage %v",
				depth, ev.CausalAncestry(), lineage)
		}
		if ev.ContractID().IsZero() || ev.AttemptID() != 1 {
			t.Fatalf("depth %d identity wrong: contract=%q attempt=%d", depth, ev.ContractID(), ev.AttemptID())
		}
		if ev.ContextDigest() == "" {
			t.Fatalf("depth %d evidence emitted with EMPTY ContextDigest", depth)
		}

		contract := x.Contracts().Contract(ev.ContractID())
		if contract == nil {
			t.Fatalf("depth %d contract never admitted to the registry", depth)
		}
		if contract.RecoveryDepth() != depth {
			t.Fatalf("depth %d contract reports RecoveryDepth %d", depth, contract.RecoveryDepth())
		}
		if contract.ParentID() != parent.ContractID() {
			t.Fatalf("depth %d registry back-pointer = %q, want %q",
				depth, contract.ParentID(), parent.ContractID())
		}
		if contract.ContextDigest() == "" || contract.ContextDigest() != ev.ContextDigest() {
			t.Fatalf("depth %d contract digest %q diverges from evidence digest %q — identity binding broken",
				depth, contract.ContextDigest(), ev.ContextDigest())
		}
		if got := x.Contracts().Attempts(parent.ContractID()); got != frozenAttempts {
			t.Fatalf("failed parent %q attempt counter moved during recovery: %d -> %d",
				parent.ContractID(), frozenAttempts, got)
		}
		for _, anc := range ev.CausalAncestry() {
			if anc == ev.ContractID() {
				t.Fatalf("depth %d ancestry contains its own identity — circular lineage", depth)
			}
		}

		lineage = append(lineage, ev.ContractID())
		parent = ev
	}

	// Chain exhausted: BOTH a material-drift recovery and a pure re-submission
	// of the deepest request must fail closed at depth MaxRecoveryChainDepth+1.
	exhaustionCases := []struct {
		name    string
		request string
		step    int
	}{
		{"parameter-drift", "chaos-rec-over-drift", 99},
		{"exact-resubmission", fmt.Sprintf("chaos-rec-over-retry-%02d", execution.MaxRecoveryChainDepth), execution.MaxRecoveryChainDepth},
	}
	for _, tc := range exhaustionCases {
		beforeCalls := provider.calls()
		res, execErr := submit(tc.request, tc.step, parent.ContractID().String())
		if !errors.Is(execErr, execution.ErrRecoveryChainExhausted) {
			t.Fatalf("%s: exhausted chain executed anyway: %v", tc.name, execErr)
		}
		if res == nil || res.Evidence != nil {
			t.Fatalf("%s: refused recovery sealed evidence — the refusal must precede every execution stage", tc.name)
		}
		if res.Proof == nil || res.Proof.Outcome != execution.OutcomeFailed {
			t.Fatalf("%s: refusal must terminate as OutcomeFailed, got %+v", tc.name, res)
		}
		if afterCalls := provider.calls(); afterCalls != beforeCalls {
			t.Fatalf("%s: refused recovery crossed the provider %d time(s) — zero-side-effect violated",
				tc.name, afterCalls-beforeCalls)
		}
	}

	// Causal-lineage integrity: walking parent back-pointers from the deepest
	// contract must reach the root in exactly MaxRecoveryChainDepth hops with
	// no cycles and no dangling links.
	visited := make(map[execution.ContractID]bool, len(lineage))
	cursor := x.Contracts().Contract(lineage[len(lineage)-1])
	if cursor == nil {
		t.Fatal("deepest recovery contract vanished from the registry")
	}
	hops := 0
	for {
		if visited[cursor.ID()] {
			t.Fatal("circular back-pointer detected in the recovery lineage")
		}
		visited[cursor.ID()] = true
		parentID := cursor.ParentID()
		if parentID.IsZero() {
			break
		}
		cursor = x.Contracts().Contract(parentID)
		if cursor == nil {
			t.Fatalf("dangling back-pointer to unadmitted contract %q", parentID)
		}
		hops++
		if hops > execution.MaxRecoveryChainDepth {
			t.Fatal("back-pointer walk exceeded the recovery chain bound")
		}
	}
	if cursor.ID() != rootID {
		t.Fatalf("lineage walk terminated at %q, want the failed root %q", cursor.ID(), rootID)
	}
	if hops != execution.MaxRecoveryChainDepth || len(visited) != execution.MaxRecoveryChainDepth+1 {
		t.Fatalf("causal lineage incomplete: hops=%d visited=%d, want %d/%d",
			hops, len(visited), execution.MaxRecoveryChainDepth, execution.MaxRecoveryChainDepth+1)
	}
	if pending := x.PendingPatchIDs(); len(pending) != 0 {
		t.Fatalf("recovery loop leaked approval surface: %v", pending)
	}
}

func chaosSameLineage(got, want []execution.ContractID) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ── SCENARIO 3 — context derivation memory stability ───────────────────────

const (
	chaosDerivations      = 1000
	chaosLatencyBatches   = 7
	chaosLatencyPerBatch  = 200
	chaosLatencyRatioCap  = 20
	chaosLatencyFloor     = 25 * time.Millisecond
	chaosReclaimDeadline  = 15 * time.Second
	chaosIntegrityCadence = 250
)

// chaosSealChannels returns a FIXED-SIZE frozen payload: identical inputs so
// a snapshot's Verify cost is byte-for-byte comparable regardless of which
// point of the lineage it sits at.
func chaosSealChannels(payload string) []execution.ContextChannel {
	return []execution.ContextChannel{
		{Kind: execution.ContextKindUserPrompt, Name: "prompt", Content: chaosMutationPrompt},
		{Kind: execution.ContextKindEvidence, Name: "ledger", Content: payload},
	}
}

// chaosDeriveLineage derives n descendant snapshots, retaining STRONGLY only
// the final tail; every released ancestor is tracked through a weak pointer
// so the test can prove GC reclaimed it. Interleaved integrity probes
// simulate state-recovery steps: periodically a throwaway node is corrupted
// in place and must fail closed with ErrContextIntegrity.
func chaosDeriveLineage(
	t *testing.T,
	root *execution.ContextSnapshot,
	n int,
	mkChannels func() []execution.ContextChannel,
) (*execution.ContextSnapshot, []weak.Pointer[execution.ContextSnapshot]) {
	t.Helper()
	historical := make([]weak.Pointer[execution.ContextSnapshot], 0, n)
	current := root
	for i := 1; i <= n; i++ {
		next := current.Derive(mkChannels())
		if err := next.Verify(); err != nil {
			t.Fatalf("derivation %d broke its seal: %v", i, err)
		}
		if next.Parent != current.ID {
			t.Fatalf("derivation %d parent link = %q, want %q", i, next.Parent, current.ID)
		}
		if want := "ctx-" + next.Digest()[:16]; next.ID != want {
			t.Fatalf("derivation %d content address = %q, want %q", i, next.ID, want)
		}
		if i%chaosIntegrityCadence == 0 {
			corrupt := current.Derive(mkChannels())
			corrupt.Channels[0].Content = "mid-loop state tamper"
			if !errors.Is(corrupt.Verify(), execution.ErrContextIntegrity) {
				t.Fatalf("state recovery at derivation %d accepted corrupted state", i)
			}
		}
		historical = append(historical, weak.Make(current))
		current = next
	}
	return current, historical
}

// chaosMinVerifyLatency returns the fastest of `batches` timed rounds of
// `perBatch` seal verifications. Taking the minimum makes the estimate robust
// against scheduler noise, so the O(1)-vs-O(depth) comparison below rests on
// real compute differences, not jitter.
func chaosMinVerifyLatency(snap *execution.ContextSnapshot, batches, perBatch int) (time.Duration, error) {
	best := time.Duration(1<<63 - 1)
	for range batches {
		start := time.Now()
		for range perBatch {
			if err := snap.Verify(); err != nil {
				return 0, err
			}
		}
		if elapsed := time.Since(start); elapsed < best {
			best = elapsed
		}
	}
	return best, nil
}

// TestChaos_ContextDerivationMemoryLeak runs 1,000 Derive() steps with
// interleaved state-integrity recovery probes and proves two properties:
//
//  1. Memory stability — every released historical snapshot node is
//     reclaimable: after GC, ALL 1,000 weak-tracked ancestors are collected
//     while only the retained tail survives, so lineage history cannot bloat
//     the heap (snapshots link by VALUE, never by live pointer).
//
//  2. O(1) seal verification — the unexported digest verification latency of
//     a snapshot 1,000 generations deep stays within a tight factor of an
//     identical-payload root snapshot: Verify reseals only the node's own
//     payload and must never walk (or rehash) the lineage.
func TestChaos_ContextDerivationMemoryLeak(t *testing.T) {
	payload := strings.Repeat("fidelity-payload-", 2800) // fixed ~46.5KB frozen channel
	mkChannels := func() []execution.ContextChannel { return chaosSealChannels(payload) }

	root := execution.FreezeContext("", mkChannels())
	if err := root.Verify(); err != nil {
		t.Fatalf("root snapshot failed its own seal: %v", err)
	}

	// The derivation runs in its own function so stale stack references to
	// released ancestors die with the frame before collection is asserted.
	tail, historical := chaosDeriveLineage(t, root, chaosDerivations, mkChannels)

	if err := tail.Verify(); err != nil {
		t.Fatalf("retained tail failed its seal after %d derivations: %v", chaosDerivations, err)
	}
	if len(tail.Digest()) != 64 {
		t.Fatalf("sealed digest is not a raw sha256 hex string: %q", tail.Digest())
	}

	// O(1): deep-lineage verification latency must track the shallow control.
	shallow := execution.FreezeContext("", mkChannels())
	shallowLatency, err := chaosMinVerifyLatency(shallow, chaosLatencyBatches, chaosLatencyPerBatch)
	if err != nil {
		t.Fatalf("shallow control verification failed: %v", err)
	}
	deepLatency, err := chaosMinVerifyLatency(tail, chaosLatencyBatches, chaosLatencyPerBatch)
	if err != nil {
		t.Fatalf("deep-tail verification failed: %v", err)
	}
	threshold := shallowLatency * chaosLatencyRatioCap
	if threshold < chaosLatencyFloor {
		threshold = chaosLatencyFloor
	}
	if deepLatency > threshold {
		t.Fatalf("seal verification is NOT O(1) in lineage depth: depth-0 best=%v, depth-%d best=%v (threshold %v)",
			shallowLatency, chaosDerivations, deepLatency, threshold)
	}

	// Reclamation: force collection and demand EVERY historical node be
	// gathered while the strongly-referenced tail survives.
	runtime.GC()
	runtime.GC()
	deadline := time.Now().Add(chaosReclaimDeadline)
	live := -1
	for {
		live = 0
		for _, wp := range historical {
			if wp.Value() != nil {
				live++
			}
		}
		if live == 0 || !time.Now().Before(deadline) {
			break
		}
		runtime.GC()
		time.Sleep(5 * time.Millisecond)
	}
	if live != 0 {
		t.Fatalf("GC failed to reclaim %d/%d historical snapshot nodes — derivation memory bloated", live, chaosDerivations)
	}
	if weak.Make(tail).Value() == nil {
		t.Fatal("strongly-referenced tail was reclaimed — collector misbehavior")
	}
	if err := tail.Verify(); err != nil {
		t.Fatalf("retained tail no longer verifiable after collection: %v", err)
	}
}

// ── SCENARIO 4 — evidence-ledger projection isolation ──────────────────────

type chaosLedgerEvent struct {
	id      execution.ContractID
	ev      *execution.ExecutionEvidence
	granted bool
	outcome execution.ExecutionOutcome
	tainted bool
}

// chaosExpectedBlockReason maps an outcome class onto the substring that MUST
// appear in a blocked projection's deterministic BlockReason.
func chaosExpectedBlockReason(outcome execution.ExecutionOutcome, tainted bool) string {
	switch {
	case tainted && outcome.Committed():
		return "taint"
	case outcome == execution.EvidenceCancelled:
		return "cancelled"
	case outcome == execution.EvidenceAbortedOCC:
		return "optimistic-concurrency"
	default:
		return "failed"
	}
}

// TestChaos_EvidenceLedgerProjectionIsolation interleaves a deterministic
// pseudo-random stream of 500 COMMITTED / FAILED / CANCELLED / ABORTED_OCC
// evidence records into the EvidenceLedger while 8 concurrent readers poll
// authoritative projections mid-stream. The gate must permit ONLY committed
// untainted evidence to mutate the target projection state; every other
// outcome (including tainted-committed partial truth) blocks fail-closed with
// a deterministic reason, and no reader — at ANY interleaving point — may
// obtain authority for blocked evidence.
func TestChaos_EvidenceLedgerProjectionIsolation(t *testing.T) {
	const streamLen = 500
	rng := rand.New(rand.NewSource(20260825))

	outcomes := []execution.ExecutionOutcome{
		execution.EvidenceCommitted,
		execution.EvidenceFailed,
		execution.EvidenceCancelled,
		execution.EvidenceAbortedOCC,
	}

	stream := make([]chaosLedgerEvent, 0, streamLen)
	classCounts := make(map[execution.ExecutionOutcome]int, len(outcomes))
	committedUntainted, committedTainted := 0, 0
	base := time.Now()
	for i := range streamLen {
		outcome := outcomes[rng.Intn(len(outcomes))]
		tainted := outcome.Committed() && rng.Intn(2) == 0
		id := execution.ContractID(fmt.Sprintf("ct-c2ledger-%04d", i))
		filesMutated := 0
		if outcome.Committed() && !tainted {
			filesMutated = 1
		}
		ev := execution.SealFromScalars(execution.SealEvidenceScalars{
			ContractID:    id,
			AttemptID:     1,
			ContextDigest: fmt.Sprintf("c2-digest-%04d", i),
			Outcome:       outcome.String(),
			Mutations: execution.MutationSetSummary{
				TransactionID: fmt.Sprintf("ms-c2-%04d", i),
				Targets:       []string{chaosTargetFile},
				FilesMutated:  filesMutated,
				Tainted:       tainted,
			},
			StartedAt:  base,
			FinishedAt: base.Add(time.Duration(i) * time.Microsecond),
		})
		if ev == nil {
			t.Fatalf("scalar seal refused canonical outcome %q", outcome)
		}
		stream = append(stream, chaosLedgerEvent{
			id:      id,
			ev:      ev,
			granted: outcome.Committed() && !tainted,
			outcome: outcome,
			tainted: tainted,
		})
		classCounts[outcome]++
		if outcome.Committed() {
			if tainted {
				committedTainted++
			} else {
				committedUntainted++
			}
		}
	}
	for _, o := range outcomes {
		if classCounts[o] == 0 {
			t.Fatalf("interleaved stream missed outcome class %q — coverage vacuous", o)
		}
	}
	if committedUntainted == 0 || committedTainted == 0 {
		t.Fatalf("taint coverage degenerate: untainted=%d tainted=%d", committedUntainted, committedTainted)
	}

	// Fail-closed reconstruction: a non-vocabulary outcome can never become
	// evidence, and nil evidence can never enter the ledger.
	if bogus := execution.SealFromScalars(execution.SealEvidenceScalars{
		ContractID: "ct-c2bogus000001", Outcome: "DETONATED",
	}); bogus != nil {
		t.Fatal("non-vocabulary outcome reconstructed as evidence")
	}

	ledger := presentation.NewEvidenceLedger()
	var ledgerMu sync.RWMutex

	// Concurrent mid-stream readers: at ANY interleaving point, authority may
	// surface exclusively for committed untainted truth.
	violations := make(chan string, 64)
	stopReaders := make(chan struct{})
	var readers sync.WaitGroup
	for r := range 8 {
		readers.Add(1)
		go func(seed int64) {
			defer readers.Done()
			prng := rand.New(rand.NewSource(seed))
			for {
				select {
				case <-stopReaders:
					return
				default:
				}
				target := stream[prng.Intn(streamLen)].id
				ledgerMu.RLock()
				proj, ok := ledger.AuthoritativeFor(target)
				latest := ledger.Latest(target)
				ledgerMu.RUnlock()
				switch {
				case ok && (latest == nil || !latest.Authoritative() || !proj.Granted()):
					select {
					case violations <- fmt.Sprintf("authority surfaced without committed untainted truth for %s", target):
					default:
					}
				case !ok && latest != nil && presentation.ProjectEvidence(latest).Granted():
					select {
					case violations <- fmt.Sprintf("gate blocked granted evidence for %s", target):
					default:
					}
				}
			}
		}(int64(r))
	}

	// Interleave the stream: gate verdict computed per event, target
	// projection state mutated ONLY on granted verdicts, ledger appended.
	projected := make(map[execution.ContractID]presentation.EvidenceProjection, streamLen)
	distinctBlockReasons := make(map[string]bool, 4)
	grantedCount := 0
	for i, item := range stream {
		proj := presentation.ProjectEvidence(item.ev)
		if proj.Granted() != item.granted {
			t.Fatalf("event %d (%s tainted=%v): gate verdict granted=%v diverges from expected granted=%v",
				i, item.outcome, item.tainted, proj.Granted(), item.granted)
		}
		if proj.Granted() {
			if item.tainted || !item.outcome.Committed() || proj.Mutations.Tainted {
				t.Fatalf("event %d: gate granted non-authoritative truth: %+v", i, proj)
			}
			if _, entered := projected[item.id]; entered {
				t.Fatalf("event %d: duplicate grant for %s", i, item.id)
			}
			projected[item.id] = proj
			grantedCount++
		} else {
			if proj.BlockReason == "" {
				t.Fatalf("event %d (%s): blocked without a deterministic block reason", i, item.outcome)
			}
			if want := chaosExpectedBlockReason(item.outcome, item.tainted); !strings.Contains(proj.BlockReason, want) {
				t.Fatalf("event %d (%s tainted=%v): block reason %q lacks the %q classification",
					i, item.outcome, item.tainted, proj.BlockReason, want)
			}
			distinctBlockReasons[proj.BlockReason] = true
			if _, entered := projected[item.id]; entered {
				t.Fatalf("event %d: BLOCKED evidence previously mutated projection state for %s", i, item.id)
			}
		}
		ledgerMu.Lock()
		ledger.Record(item.ev)
		ledgerMu.Unlock()
	}

	close(stopReaders)
	readers.Wait()
	close(violations)
	for v := range violations {
		t.Error(v)
	}

	if grantedCount == 0 || grantedCount == streamLen {
		t.Fatalf("degenerate projection stream: granted=%d of %d", grantedCount, streamLen)
	}
	if len(projected) != grantedCount {
		t.Fatalf("target projection state holds %d entries for %d granted events", len(projected), grantedCount)
	}
	if len(distinctBlockReasons) < 3 {
		t.Fatalf("blocked projections collapsed into %d distinct reasons — classification lost", len(distinctBlockReasons))
	}

	// Final sweep: the ledger's authority view must mirror the gate for every
	// contract, and stored records must be the immutable originals.
	for _, item := range stream {
		ledgerMu.RLock()
		proj, ok := ledger.AuthoritativeFor(item.id)
		latest := ledger.Latest(item.id)
		ledgerMu.RUnlock()
		if latest != item.ev {
			t.Fatalf("ledger rewrote or dropped the immutable record for %s", item.id)
		}
		if ok != item.granted || proj.Granted() != item.granted {
			t.Fatalf("ledger authority for %s = %v, want %v (outcome %s tainted=%v)",
				item.id, ok, item.granted, item.outcome, item.tainted)
		}
		if ok && proj.ContractID != item.id {
			t.Fatalf("authoritative projection for %s carries foreign identity %s", item.id, proj.ContractID)
		}
	}
}

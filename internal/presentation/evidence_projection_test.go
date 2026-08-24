package presentation

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── Scripted runtime (real executions — no fabricated evidence) ────────────

type scriptedProvider struct {
	mu        sync.Mutex
	responses []string
	callCount int
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.callCount >= len(p.responses) {
		return nil, errors.New("scripted: exhausted")
	}
	c := p.responses[p.callCount]
	p.callCount++
	return &ai.Response{Content: c}, nil
}

func (p *scriptedProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("scripted: no stream")
}

// runExecution executes one REAL runtime execution against a temp workspace
// and returns its sealed terminal evidence. outcome selects the flow:
// "committed" (read-only completion), "failed" (provider exhausted).
func runExecution(t *testing.T, prompt string, fail bool) (*execution.ExecutionEvidence, *events.Bus) {
	t.Helper()
	root := t.TempDir()
	if err := osWriteTarget(root, "note.txt", "foo\nbar\nbaz\n"); err != nil {
		t.Fatalf("write target: %v", err)
	}
	prov := &scriptedProvider{}
	if !fail {
		prov.responses = []string{"analysis: replace bar with qux"}
	}
	bus := events.NewBus(events.DefaultBufferSize)
	x := execution.NewRuntimeExecutor(root, config.Default(), prov, bus, "")
	req := execution.ExecuteRequest{
		RequestID: "proj-1",
		Mode:      "ask",
		Prompt:    prompt,
		Targets:   []string{"note.txt"},
		Strategy: &strategy.ExecutionStrategyProfile{
			Strategy:      strategy.TargetedReasoning,
			ModelRequired: !fail,
		},
	}
	res, err := x.Execute(context.Background(), req)
	if fail && err == nil && res.Err == nil {
		t.Fatal("expected the scripted execution to fail")
	}
	if !fail && (err != nil || res.Err != nil) {
		t.Fatalf("expected committed execution: %v / %v", err, res.Err)
	}
	if res.Evidence == nil {
		t.Fatal("runtime sealed NO terminal evidence")
	}
	return res.Evidence, bus
}

func osWriteTarget(root, name, content string) error {
	return os.WriteFile(root+"/"+name, []byte(content), 0o644)
}

// TestProjectEvidenceOnlyCommittedUntaintedIsAuthoritative locks the
// single-source-of-truth gate over REAL runtime evidence: ONLY a COMMITTED
// untainted evidence projects as authoritative; every other outcome strictly
// blocks.
func TestProjectEvidenceOnlyCommittedUntaintedIsAuthoritative(t *testing.T) {
	committed, _ := runExecution(t, "change bar to qux", false)
	p := ProjectEvidence(committed)
	if !p.Granted() || p.Authority != AuthorityGranted {
		t.Fatalf("committed untainted evidence blocked: %+v", p)
	}
	if !p.Outcome.Committed() || p.ContractID.IsZero() || p.AttemptID != 1 {
		t.Fatalf("projection lost identity/outcome: %+v", p)
	}
	if p.ContextDigest == "" {
		t.Fatal("projection lost the Phase 1 context digest")
	}
	if p.BlockReason != "" {
		t.Fatalf("granted projection carries a block reason: %q", p.BlockReason)
	}

	failed, _ := runExecution(t, "explode deterministically", true)
	fp := ProjectEvidence(failed)
	if fp.Granted() || fp.Authority != AuthorityBlocked {
		t.Fatalf("failed evidence projected as success: %+v", fp)
	}
	if fp.BlockReason == "" {
		t.Fatal("blocked projection must explain itself")
	}

	t.Run("cancelled-blocks", func(t *testing.T) {
		ev := sealCancelledEvidence()
		cp := ProjectEvidence(ev)
		if cp.Granted() {
			t.Fatalf("cancelled evidence projected as success: %+v", cp)
		}
	})
	t.Run("aborted-occ-blocks", func(t *testing.T) {
		ev := sealOCCEvidence()
		op := ProjectEvidence(ev)
		if op.Granted() {
			t.Fatalf("OCC-aborted evidence projected as success: %+v", op)
		}
	})
	t.Run("tainted-committed-blocks", func(t *testing.T) {
		ev := sealTaintedCommittedEvidence()
		tp := ProjectEvidence(ev)
		if tp.Granted() {
			t.Fatalf("tainted evidence projected as success — intermediate state treated as truth: %+v", tp)
		}
	})
	t.Run("nil-evidence-blocks", func(t *testing.T) {
		var nilEv *execution.ExecutionEvidence
		if ProjectEvidence(nilEv).Granted() {
			t.Fatal("nil evidence must block")
		}
	})
}

// TestEvidenceLedgerAppendOnlyAndGateChecked locks the queue-projector
// semantics over real runtime output: records append per contract, later
// attempts supersede earlier ones for authority reads, and AuthoritativeFor
// refuses blocked evidence.
func TestEvidenceLedgerAppendOnlyAndGateChecked(t *testing.T) {
	l := NewEvidenceLedger()

	// No record yet.
	if _, ok := l.AuthoritativeFor("ct-missing0000001"); ok {
		t.Fatal("empty ledger granted authority for an unknown contract")
	}

	// A failed attempt is recorded but never grants authority.
	failed, _ := runExecution(t, "fail this exact prompt", true)
	l.Record(failed)
	if _, ok := l.AuthoritativeFor(failed.ContractID()); ok {
		t.Fatal("ledger granted authority from FAILED evidence")
	}

	// A retry that commits grants authority for the SAME contract.
	committed, _ := runExecution(t, "succeed on retry", false)

	// Force the ledger scenario where the contract already carries failed
	// history: record the commit under ITS OWN contract id instead.
	l.Record(committed)
	p, ok := l.AuthoritativeFor(committed.ContractID())
	if !ok || !p.Granted() || p.AttemptID != committed.AttemptID() {
		t.Fatalf("committed attempt not projected: ok=%v %+v", ok, p)
	}

	// Tainted later attempt revokes success again (latest attempt wins).
	l.Record(sealTaintedCommittedEvidenceWithID(committed.ContractID()))
	if _, ok := l.AuthoritativeFor(committed.ContractID()); ok {
		t.Fatal("ledger granted authority from TAINTED evidence")
	}

	// Nil / identity-less records are refused at the door.
	before := len(l.order)
	l.Record(nil)
	var nilEv *execution.ExecutionEvidence
	l.Record(nilEv)
	l.Record(sealTaintedCommittedEvidenceWithID(""))
	if got := len(l.order); got != before {
		t.Fatalf("ledger stored %d extra contracts, want 0 (nil/zero-ID refused)", got-before)
	}

	// Latest returns the raw immutable evidence (observability path).
	if ev := l.Latest(committed.ContractID()); ev == nil {
		t.Fatal("latest evidence missing")
	}
}

// TestProjectEvidenceCarriesCausalLineage proves recovery lineage survives
// into projections so downstream consumers can audit causal chains.
func TestProjectEvidenceCarriesCausalLineage(t *testing.T) {
	ev := sealLineageEvidence()
	p := ProjectEvidence(ev)
	if p.ParentContractID != "ct-parent000000002" {
		t.Fatalf("projection lost the causal back-pointer: %q", p.ParentContractID)
	}
	if len(p.CausalAncestry) != 2 || p.CausalAncestry[0] != "ct-root00000000003" {
		t.Fatalf("projection lost the ancestry chain: %v", p.CausalAncestry)
	}
	if p.Granted() {
		t.Fatal("failed lineage fixture must stay blocked")
	}
}

// TestEvidenceEventPayloadProjectsIdentically proves the canonical
// execution.evidence BUS EVENT projects to exactly the same verdict as the
// in-process evidence object — downstream queue consumers reading only the
// event stream derive identical truth.
func TestEvidenceEventPayloadProjectsIdentically(t *testing.T) {
	root := t.TempDir()
	if err := osWriteTarget(root, "note.txt", "foo\nbar\nbaz\n"); err != nil {
		t.Fatalf("write target: %v", err)
	}
	bus := events.NewBus(events.DefaultBufferSize)
	x := execution.NewRuntimeExecutor(root, config.Default(), &scriptedProvider{responses: []string{"analysis"}}, bus, "")

	collected := make(chan events.ExecutionEvidencePayload, 8)
	sub := bus.Subscribe(events.EventExecutionEvidence, func(ev events.DomainEvent) {
		if p, ok := ev.Payload().(events.ExecutionEvidencePayload); ok {
			collected <- p
		}
	})
	defer sub.Cancel()

	req := execution.ExecuteRequest{
		RequestID: "bus-1",
		Mode:      "ask",
		Prompt:    "project me identically",
		Targets:   []string{"note.txt"},
		Strategy: &strategy.ExecutionStrategyProfile{
			Strategy:      strategy.TargetedReasoning,
			ModelRequired: true,
		},
	}
	res, err := x.Execute(context.Background(), req)
	if err != nil || res.Err != nil {
		t.Fatalf("execute: %v / %v", err, res.Err)
	}

	select {
	case payload := <-collected:
		if payload.Outcome != string(execution.EvidenceCommitted) {
			t.Fatalf("event outcome = %q, want COMMITTED", payload.Outcome)
		}
		if payload.ContractID != res.Evidence.ContractID().String() ||
			payload.AttemptID != uint32(res.Evidence.AttemptID()) ||
			payload.ContextDigest != res.Evidence.ContextDigest() {
			t.Fatalf("event identity diverged from sealed evidence: %+v vs %s/%d",
				payload, res.Evidence.ContractID(), res.Evidence.AttemptID())
		}
		if payload.Tainted {
			t.Fatal("committed event must not be tainted")
		}
		if payload.FinishedAt.Before(payload.StartedAt) {
			t.Fatalf("event time window inverted: %v > %v", payload.StartedAt, payload.FinishedAt)
		}
		if payload.RequestID != "bus-1" {
			t.Fatalf("event lost the request correlation ID: %q", payload.RequestID)
		}

		// The event payload must project identically to the in-process record.
		fromEvent := ProjectEvidence(execution.SealFromScalars(execution.SealEvidenceScalars{
			ContractID:    execution.ContractID(payload.ContractID),
			AttemptID:     execution.AttemptID(payload.AttemptID),
			Parent:        execution.ContractID(payload.ParentContractID),
			Ancestry:      payload.CausalAncestry,
			ContextDigest: payload.ContextDigest,
			Outcome:       payload.Outcome,
			Mutations: execution.MutationSetSummary{
				Targets:      payload.Targets,
				Tainted:      payload.Tainted,
				FilesMutated: payload.FilesMutated,
			},
			StartedAt:  payload.StartedAt,
			FinishedAt: payload.FinishedAt,
		}))
		direct := ProjectEvidence(res.Evidence)
		if fromEvent.Granted() != direct.Granted() || fromEvent.Authority != direct.Authority ||
			fromEvent.Outcome != direct.Outcome {
			t.Fatalf("event-derived projection diverged: event(%+v) vs evidence(%+v)", fromEvent, direct)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no execution.evidence event observed on the bus")
	}
}

// ── Evidence fixtures assembled through the exported scalar vocabulary ──────

func sealCancelledEvidence() *execution.ExecutionEvidence {
	return execution.SealFromScalars(execution.SealEvidenceScalars{
		ContractID: "ct-cancel000000001",
		AttemptID:  1,
		Outcome:    string(execution.EvidenceCancelled),
	})
}

func sealOCCEvidence() *execution.ExecutionEvidence {
	return execution.SealFromScalars(execution.SealEvidenceScalars{
		ContractID: "ct-occ000000000001",
		AttemptID:  1,
		Outcome:    string(execution.EvidenceAbortedOCC),
	})
}

func sealTaintedCommittedEvidence() *execution.ExecutionEvidence {
	return execution.SealFromScalars(execution.SealEvidenceScalars{
		ContractID: "ct-taint0000000001",
		AttemptID:  2,
		Outcome:    string(execution.EvidenceCommitted),
		Mutations:  execution.MutationSetSummary{Tainted: true, FilesMutated: 1},
	})
}

func sealTaintedCommittedEvidenceWithID(id execution.ContractID) *execution.ExecutionEvidence {
	return execution.SealFromScalars(execution.SealEvidenceScalars{
		ContractID: id,
		AttemptID:  3,
		Outcome:    string(execution.EvidenceCommitted),
		Mutations:  execution.MutationSetSummary{Tainted: true, FilesMutated: 1},
	})
}

func sealLineageEvidence() *execution.ExecutionEvidence {
	return execution.SealFromScalars(execution.SealEvidenceScalars{
		ContractID: "ct-child0000000001",
		AttemptID:  1,
		Parent:     "ct-parent000000002",
		Ancestry:   []string{"ct-root00000000003", "ct-parent000000002"},
		Outcome:    string(execution.EvidenceFailed),
	})
}

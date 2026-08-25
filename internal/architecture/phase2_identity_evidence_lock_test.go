// Phase 2 contract-identity & evidence lock suite.
//
// These tests mechanically freeze the Phase 2 invariants:
//
//	ContractID (unique execution intent) and AttemptID (specific invocation
//	attempt) are strictly separated first-class primitives. Retries increment
//	AttemptID and can never alter ContractID; parameter/strategy changes fork
//	a new ContractID by construction (content-addressed identity).
//
//	Recovery is an APPEND-ONLY causal step: a recovery contract carries an
//	explicit back-pointer plus the full ancestry of its failed parent, failed
//	contract history is never rewritten, and the chain is bounded
//	(MaxRecoveryChainDepth) with fail-closed exhaustion at admission.
//
//	ExecutionEvidence is the immutable authoritative terminal record. Only
//	COMMITTED untainted evidence projects as authoritative success;
//	FAILED / ABORTED_OCC / CANCELLED / tainted strictly block projection.
//
// Structural guards parse production sources with go/parser + go/ast
// (whitespace/comment immune); behavioral guards drive only EXPORTED package
// APIs so weakening an exported contract fails here even if in-package tests
// are edited.
package architecture

import (
	"context"
	"errors"
	"go/ast"
	"go/token"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/presentation"
)

// ── GUARD 2.1 — identity primitives are immutable value types ──────────────

// TestPhase2IdentityPrimitivesHaveNoExportedMutableState pins the immutability
// of ExecutionContract and ExecutionEvidence at BOTH layers: the parsed ASTs
// must expose zero exported struct fields (all state unexported, read-only via
// methods), no Set*/Mutate* methods may exist on either type, and the compiled
// types must agree. Exported mutable fields would let callers rewrite contract
// history or forge evidence after the fact.
func TestPhase2IdentityPrimitivesHaveNoExportedMutableState(t *testing.T) {
	root := repoRoot(t)

	type prim struct {
		typeName string
		file     string
	}
	for _, p := range []prim{
		{"ExecutionContract", "internal/execution/contract.go"},
		{"ExecutionEvidence", "internal/execution/evidence.go"},
	} {
		f, _ := parseFile(t, filepath.Join(root, p.file))
		var st *ast.StructType
		found := false
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if ok && ts.Name.Name == p.typeName {
					s, ok := ts.Type.(*ast.StructType)
					if !ok {
						t.Fatalf("architecture: %s must remain a struct", p.typeName)
					}
					st = s
					found = true
				}
			}
		}
		if !found || st == nil {
			t.Fatalf("architecture: %s must exist in %s", p.typeName, p.file)
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if name.IsExported() {
					t.Fatalf("architecture: %s exposes exported field %q — primitive state MUST be unexported so history cannot be rewritten in place", p.typeName, name.Name)
				}
			}
		}

		// No setter/mutator methods anywhere in the same file.
		f2, _ := parseFile(t, filepath.Join(root, p.file))
		for _, decl := range f2.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Recv.List == nil {
				continue
			}
			recvType := ""
			if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
				if id, ok := star.X.(*ast.Ident); ok {
					recvType = id.Name
				}
			} else if id, ok := fn.Recv.List[0].Type.(*ast.Ident); ok {
				recvType = id.Name
			}
			if recvType != p.typeName {
				continue
			}
			n := fn.Name.Name
			for _, prefix := range []string{"Set", "Mutate", "Rewrite", "Reparent"} {
				if len(n) > len(prefix) && n[:len(prefix)] == prefix {
					t.Fatalf("architecture: %s carries mutator method %q — primitives are immutable by construction", p.typeName, n)
				}
			}
		}

		// The compiled type must agree: zero exported fields.
		typ := reflect.TypeOf(execution.ExecutionContract{})
		if p.typeName == "ExecutionEvidence" {
			typ = reflect.TypeOf(execution.ExecutionEvidence{})
		}
		for i := 0; i < typ.NumField(); i++ {
			if sf := typ.Field(i); sf.PkgPath == "" {
				t.Fatalf("architecture: compiled %s exposes exported field %q", p.typeName, sf.Name)
			}
		}
	}
}

// TestPhase2ContractIDAndAttemptIDAreDistinctNamedTypes locks the identity
// separation at the type level: ContractID and AttemptID are DISTINCT defined
// types (never aliases, never raw strings), so one can never be silently
// substituted for the other in logs, proofs or evidence.
func TestPhase2ContractIDAndAttemptIDAreDistinctNamedTypes(t *testing.T) {
	ct := reflect.TypeOf(execution.ContractID(""))
	at := reflect.TypeOf(execution.AttemptID(0))
	if ct.Name() != "ContractID" || ct.Kind() != reflect.String {
		t.Fatalf("ContractID must be a named string type, got %q/%v", ct.Name(), ct.Kind())
	}
	if at.Name() != "AttemptID" || at.Kind() != reflect.Uint32 {
		t.Fatalf("AttemptID must be a named uint32 type, got %q/%v", at.Name(), at.Kind())
	}
	if ct == at {
		t.Fatal("ContractID and AttemptID collapsed into one type — identity separation violated")
	}
}

// ── GUARD 2.2 — the registry is the SOLE attempt authority ─────────────────

// TestPhase2AttemptCountersWrittenOnlyInRegistryModule scans every production
// file of internal/execution for writes to an `attempts` map/index and demands
// they live exclusively in contract.go — the registry under its lock is the
// ONLY place an AttemptID ever increments.
func TestPhase2AttemptCountersWrittenOnlyInRegistryModule(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "execution")
	found := 0
	for _, rel := range goFilesUnder(dir) {
		f, fset := parseFile(t, filepath.Join(dir, rel))
		scanWrites := func(lhs []ast.Expr, pos token.Pos) {
			for _, l := range lhs {
				idx, ok := l.(*ast.IndexExpr)
				if !ok {
					continue
				}
				sel, ok := idx.X.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "attempts" {
					continue
				}
				found++
				relFile := filepath.ToSlash(filepath.Join("internal", "execution", rel))
				if relFile != "internal/execution/contract.go" {
					t.Errorf("architecture: %s:%d writes an attempts counter outside contract.go — the registry is the sole attempt authority",
						relFile, fset.Position(pos).Line)
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.AssignStmt:
				scanWrites(stmt.Lhs, stmt.Pos())
			case *ast.IncDecStmt:
				scanWrites([]ast.Expr{stmt.X}, stmt.Pos())
			}
			return true
		})
	}
	if found == 0 {
		t.Fatal("architecture: no attempt-counter writes found — lock vacuous")
	}
}

// ── GUARD 2.3 — evidence sealing choke point & outcome vocabulary ──────────

// TestPhase2EvidenceSealedOnlyAtFinalizeResultChokePoint pins the emission
// topology structurally: sealTerminalEvidence exists, refuses approval-held
// results, and is invoked from finalizeResult (the function EVERY terminal
// return path already crosses); the raw constructor stays unexported so
// evidence cannot be assembled outside the runtime.
func TestPhase2EvidenceSealedOnlyAtFinalizeResultChokePoint(t *testing.T) {
	root := repoRoot(t)
	const executorFile = "internal/execution/executor.go"
	f, _ := parseFile(t, filepath.Join(root, executorFile))

	sealFn := findFuncDecl(f, "sealTerminalEvidence")
	if sealFn == nil {
		t.Fatal("architecture: RuntimeExecutor.sealTerminalEvidence must exist as the evidence choke point")
	}
	sealCallsHeld, sealCallsFinalize := 0, 0
	heldRef := false
	ast.Inspect(sealFn.Body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "PendingPatchID" {
			heldRef = true
		}
		return true
	})
	if !heldRef {
		t.Fatal("architecture: sealTerminalEvidence must refuse approval-held executions (PendingPatchID guard) — a held gate is not a termination")
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "sealTerminalEvidence" {
				switch fn.Name.Name {
				case "finalizeResult":
					sealCallsFinalize++
				default:
					sealCallsHeld++
				}
			}
			return true
		})
	}
	if sealCallsFinalize != 1 {
		t.Fatalf("architecture: finalizeResult must be the single sealTerminalEvidence call site, got %d", sealCallsFinalize)
	}
	if sealCallsHeld != 0 {
		t.Fatalf("architecture: sealTerminalEvidence called from %d non-finalizeResult site(s) — evidence is born ONLY at the terminal choke point", sealCallsHeld)
	}

	// The runtime constructor is unexported: no caller can fabricate evidence.
	if _, ok := reflect.TypeOf(execution.RuntimeExecutor{}).MethodByName("SealEvidence"); ok {
		t.Fatal("architecture: SealEvidence must stay unexported — evidence is born inside the runtime only")
	}

	// The result carries the sealed record for downstream consumption.
	resTyp := reflect.TypeOf(execution.ExecutionResult{})
	evField, ok := resTyp.FieldByName("Evidence")
	if !ok || evField.Type != reflect.TypeOf((*execution.ExecutionEvidence)(nil)) {
		t.Fatal("architecture: ExecutionResult.Evidence (*ExecutionEvidence) missing — projectors consume it as the sole authoritative artifact")
	}
}

// TestPhase2AbortedOccReservedForPhase3 proves ABORTED_OCC is part of the
// canonical vocabulary but is NEVER derived by the current baseline: it appears
// in evidence.go only as its const declaration, never as a return value inside
// the outcome mapper — Phase 3 OCC verification will introduce the producer.
func TestPhase2AbortedOccReservedForPhase3(t *testing.T) {
	root := repoRoot(t)
	f, fset := parseFile(t, filepath.Join(root, "internal", "execution", "evidence.go"))

	mapper := findFuncDecl(f, "evidenceOutcomeFor")
	if mapper == nil {
		t.Fatal("architecture: evidenceOutcomeFor must exist as the total outcome mapping")
	}
	ast.Inspect(mapper.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "EvidenceAbortedOCC" {
			t.Errorf("architecture: evidenceOutcomeFor derives ABORTED_OCC (line %d) — the OCC abort producer belongs to Phase 3 source-hash verification, not this baseline",
				fset.Position(id.Pos()).Line)
		}
		return true
	})

	// The vocabulary member itself must exist and be terminal-but-blocking.
	if string(execution.EvidenceAbortedOCC) != "ABORTED_OCC" {
		t.Fatalf("canonical label drifted: %q", execution.EvidenceAbortedOCC)
	}
	if !execution.EvidenceAbortedOCC.Terminal() || execution.EvidenceAbortedOCC.Committed() {
		t.Fatal("ABORTED_OCC must be terminal and never committed")
	}
}

// ── Behavioral: identity separation through REAL executions ────────────────

// TestPhase2AttemptIncrementNeverMutatesContractID drives two identical real
// executions and proves: SAME ContractID across retries, AttemptIDs 1 then 2,
// and that tampering with accessor-returned copies cannot rewrite the sealed
// lineage of subsequent evidence.
func TestPhase2AttemptIncrementNeverMutatesContractID(t *testing.T) {
	root := t.TempDir()
	lockWriteTarget(t, root, lockOriginal)
	provider := &lockScriptedProvider{responses: 2}
	x := execution.NewRuntimeExecutor(root, config.Default(), provider, nil, "")

	req := execution.ExecuteRequest{
		RequestID: "phase2-retry",
		Mode:      "ask",
		Prompt:    lockMutationPrompt,
		Targets:   []string{lockTargetFile},
		Strategy: &strategy.ExecutionStrategyProfile{
			Strategy:      strategy.TargetedReasoning,
			ModelRequired: true,
		},
	}

	var ids [2]execution.ContractID
	var attempts [2]execution.AttemptID
	for i := 0; i < 2; i++ {
		res, err := x.Execute(context.Background(), req)
		if err != nil || res.Err != nil {
			t.Fatalf("attempt %d execute: %v / %v", i+1, err, res.Err)
		}
		ev := res.Evidence
		if ev == nil {
			t.Fatalf("attempt %d sealed no evidence", i+1)
		}
		ids[i], attempts[i] = ev.ContractID(), ev.AttemptID()

		// Tamper attempt: mutate everything the accessors handed back. The
		// next attempt's evidence must still carry the pristine identity.
		anc := ev.CausalAncestry()
		if len(anc) > 0 {
			anc[0] = "ct-tampered0000001"
		}
		ms := ev.Mutations()
		ms.Targets = []string{"tampered.txt"}
	}
	if ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("retry mutated ContractID: %s -> %s", ids[0], ids[1])
	}
	if attempts[0] != 1 || attempts[1] != 2 {
		t.Fatalf("attempts = [%d,%d], want [1,2] (deterministic increments)", attempts[0], attempts[1])
	}
	if provider.calls() != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls())
	}
}

// TestPhase2ParameterChangeForksNewContractThroughRealExecutions proves a
// material parameter change forks the identity even when nothing else moved.
func TestPhase2ParameterChangeForksNewContractThroughRealExecutions(t *testing.T) {
	root := t.TempDir()
	lockWriteTarget(t, root, lockOriginal)
	provider := &lockScriptedProvider{responses: 2}
	x := execution.NewRuntimeExecutor(root, config.Default(), provider, nil, "")

	exec := func(prompt string) *execution.ExecutionEvidence {
		res, err := x.Execute(context.Background(), execution.ExecuteRequest{
			RequestID: "phase2-fork-" + prompt,
			Mode:      "ask",
			Prompt:    prompt,
			Targets:   []string{lockTargetFile},
			Strategy: &strategy.ExecutionStrategyProfile{
				Strategy:      strategy.TargetedReasoning,
				ModelRequired: true,
			},
		})
		if err != nil || res.Err != nil {
			t.Fatalf("execute %q: %v / %v", prompt, err, res.Err)
		}
		if res.Evidence == nil {
			t.Fatalf("execute %q sealed no evidence", prompt)
		}
		return res.Evidence
	}

	a := exec(lockMutationPrompt)
	b := exec(lockMutationPrompt + " with extra context")
	if a.ContractID() == b.ContractID() {
		t.Fatal("parameter change reused the ContractID — identity derivation broken")
	}
}

// TestPhase2RecoveryBackLinksBoundedFailClosed drives the causal chain through
// the EXPORTED executor API from OUTSIDE the execution package: every recovery
// appends a new contract with an explicit back-pointer and full ancestry, the
// failed parent's attempt counter stays frozen, and both bound exhaustion and
// forged parentage fail closed at admission.
func TestPhase2RecoveryBackLinksBoundedFailClosed(t *testing.T) {
	root := t.TempDir()
	lockWriteTarget(t, root, lockOriginal)
	provider := &lockScriptedProvider{responses: 1}
	x := execution.NewRuntimeExecutor(root, config.Default(), provider, nil, "")

	readOnly := func(id string) execution.ExecuteRequest {
		return execution.ExecuteRequest{
			RequestID: id,
			Mode:      "ask",
			Prompt:    lockMutationPrompt,
			Targets:   []string{lockTargetFile},
			Strategy: &strategy.ExecutionStrategyProfile{
				Strategy:      strategy.TargetedReasoning,
				ModelRequired: true,
			},
		}
	}
	deterministic := func(id string) execution.ExecuteRequest {
		return execution.ExecuteRequest{
			RequestID: id,
			Mode:      "build",
			Prompt:    lockMutationPrompt,
			Targets:   []string{lockTargetFile},
			Strategy: &strategy.ExecutionStrategyProfile{
				Strategy:      strategy.DirectDeterministic,
				Deterministic: true,
			},
		}
	}

	first, err := x.Execute(context.Background(), readOnly("p2-root"))
	if err != nil || first.Err != nil {
		t.Fatalf("root: %v / %v", err, first.Err)
	}
	parent := first.Evidence
	parentAttempts := x.Contracts().Attempts(parent.ContractID())

	// Recovery step: material change + explicit causal pointer.
	rec := deterministic("p2-rec")
	rec.RecoveryOf = parent.ContractID().String()
	child, err := x.Execute(context.Background(), rec)
	if err != nil || child.Err != nil {
		t.Fatalf("recovery: %v / %v", err, child.Err)
	}
	if child.Evidence.ParentContractID() != parent.ContractID() {
		t.Fatalf("back-pointer = %q, want %q", child.Evidence.ParentContractID(), parent.ContractID())
	}
	anc := child.Evidence.CausalAncestry()
	if len(anc) != 1 || anc[0] != parent.ContractID() {
		t.Fatalf("ancestry = %v, want exactly the failed parent", anc)
	}
	if child.Evidence.ContractID() == parent.ContractID() {
		t.Fatal("recovery rewrote the failed ContractID in place")
	}
	if got := x.Contracts().Attempts(parent.ContractID()); got != parentAttempts {
		t.Fatalf("failed parent's attempt counter moved during recovery: %d -> %d", parentAttempts, got)
	}

	// Walk the chain to the bound…
	current := child.Evidence.ContractID()
	for depth := 2; depth <= execution.MaxRecoveryChainDepth; depth++ {
		nxt := deterministic("p2-deep")
		nxt.RecoveryOf = current.String()
		res, execErr := x.Execute(context.Background(), nxt)
		if execErr != nil {
			t.Fatalf("bounded depth %d refused early: %v", depth, execErr)
		}
		current = res.Evidence.ContractID()
	}
	// …then prove exhaustion fails closed BEFORE any execution stage.
	over := deterministic("p2-over")
	over.RecoveryOf = current.String()
	if _, execErr := x.Execute(context.Background(), over); !errors.Is(execErr, execution.ErrRecoveryChainExhausted) {
		t.Fatalf("exhausted chain executed anyway: %v", execErr)
	}

	// Forged lineage fails closed.
	forge := readOnly("p2-forge")
	forge.RecoveryOf = "ct-forgedforgedforg"
	if _, execErr := x.Execute(context.Background(), forge); !errors.Is(execErr, execution.ErrUnknownParentContract) {
		t.Fatalf("forged ancestry admitted: %v", execErr)
	}
}

// ── Behavioral: projection gating over real evidence ────────────────────────

// TestPhase2NonCommittedEvidenceBlocksAuthoritativeProjection drives REAL
// executions from outside the packages and proves the presentation gate:
// committed evidence projects; failed evidence strictly blocks; the ledger
// never yields authority for blocked records.
func TestPhase2NonCommittedEvidenceBlocksAuthoritativeProjection(t *testing.T) {
	newExec := func(t *testing.T, responses int) *execution.RuntimeExecutor {
		t.Helper()
		root := t.TempDir()
		lockWriteTarget(t, root, lockOriginal)
		x := execution.NewRuntimeExecutor(root, config.Default(), &lockScriptedProvider{responses: responses}, nil, "")
		return x
	}
	reasoningReq := func(id string) execution.ExecuteRequest {
		return execution.ExecuteRequest{
			RequestID: id,
			Mode:      "ask",
			Prompt:    lockMutationPrompt,
			Targets:   []string{lockTargetFile},
			Strategy: &strategy.ExecutionStrategyProfile{
				Strategy:      strategy.TargetedReasoning,
				ModelRequired: true,
			},
		}
	}

	t.Run("committed-grants", func(t *testing.T) {
		x := newExec(t, 1)
		res, err := x.Execute(context.Background(), reasoningReq("p2-ok"))
		if err != nil || res.Err != nil {
			t.Fatalf("execute: %v / %v", err, res.Err)
		}
		p := presentation.ProjectEvidence(res.Evidence)
		if !p.Granted() || p.Outcome != execution.EvidenceCommitted {
			t.Fatalf("committed evidence blocked: %+v", p)
		}
	})

	t.Run("failed-blocks", func(t *testing.T) {
		x := newExec(t, 0) // provider exhausted → deterministic failure
		res, _ := x.Execute(context.Background(), reasoningReq("p2-bad"))
		if res.Evidence == nil {
			t.Fatal("failed execution sealed no evidence")
		}
		if res.Evidence.Outcome() != execution.EvidenceFailed {
			t.Fatalf("failure conveyed as %q — partial-truth leak", res.Evidence.Outcome())
		}
		p := presentation.ProjectEvidence(res.Evidence)
		if p.Granted() {
			t.Fatalf("failed evidence projected as authoritative success: %+v", p)
		}

		l := presentation.NewEvidenceLedger()
		l.Record(res.Evidence)
		if _, ok := l.AuthoritativeFor(res.Evidence.ContractID()); ok {
			t.Fatal("ledger granted authority for FAILED evidence")
		}
	})
}

// TestPhase2TaintedPartialMutationsNeverProjectAsSuccess proves the taint rule
// end-to-end at the gate: reconstructed tainted-committed scalars — exactly
// what an audit sink would replay — are refused authority by BOTH the pure
// gate and the ledger.
func TestPhase2TaintedPartialMutationsNeverProjectAsSuccess(t *testing.T) {
	ev := execution.SealFromScalars(execution.SealEvidenceScalars{
		ContractID:    "ct-p2taint00000001",
		AttemptID:     2,
		ContextDigest: "deadbeef",
		Outcome:       string(execution.EvidenceCommitted),
		Mutations: execution.MutationSetSummary{
			TransactionID: "ms-9",
			Targets:       []string{lockTargetFile},
			Tainted:       true,
		},
	})
	if ev == nil {
		t.Fatal("scalar reconstruction refused a valid vocabulary outcome")
	}
	if ev.Authoritative() {
		t.Fatal("tainted evidence reports Authoritative()")
	}
	p := presentation.ProjectEvidence(ev)
	if p.Granted() || p.Mutations.Tainted != true {
		t.Fatalf("gate projected tainted intermediate state as truth: %+v", p)
	}
	l := presentation.NewEvidenceLedger()
	l.Record(ev)
	if _, ok := l.AuthoritativeFor(ev.ContractID()); ok {
		t.Fatal("ledger granted authority for tainted evidence")
	}

	// Untampered twin sanity: identical scalars without taint DO project.
	clean := execution.SealFromScalars(execution.SealEvidenceScalars{
		ContractID:    "ct-p2clean00000001",
		AttemptID:     2,
		ContextDigest: "deadbeef",
		Outcome:       string(execution.EvidenceCommitted),
		Mutations: execution.MutationSetSummary{
			TransactionID: "ms-9",
			Targets:       []string{lockTargetFile},
			FilesMutated:  1,
		},
	})
	if cp := presentation.ProjectEvidence(clean); !cp.Granted() {
		t.Fatalf("clean committed evidence wrongly blocked: %+v", cp)
	}
}

// TestPhase2EvidenceEventIsTheSoleBusAuthority proves the canonical
// execution.evidence event fires exactly once per terminated execution and
// carries the complete scalar truth set (identity + lineage + digest +
// outcome + mutation summary + window), so downstream projectors need nothing
// else to derive authoritative state.
func TestPhase2EvidenceEventIsTheSoleBusAuthority(t *testing.T) {
	root := t.TempDir()
	lockWriteTarget(t, root, lockOriginal)
	bus := events.NewBus(events.DefaultBufferSize)
	x := execution.NewRuntimeExecutor(root, config.Default(), &lockScriptedProvider{responses: 1}, bus, "")

	var mu sync.Mutex
	evidenceEvents := 0
	var payload events.ExecutionEvidencePayload
	sub := bus.Subscribe(events.EventExecutionEvidence, func(ev events.DomainEvent) {
		mu.Lock()
		defer mu.Unlock()
		evidenceEvents++
		if p, ok := ev.Payload().(events.ExecutionEvidencePayload); ok {
			payload = p
		}
	})
	defer sub.Cancel()

	res, err := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "p2-event",
		Mode:      "ask",
		Prompt:    lockMutationPrompt,
		Targets:   []string{lockTargetFile},
		Strategy: &strategy.ExecutionStrategyProfile{
			Strategy:      strategy.TargetedReasoning,
			ModelRequired: true,
		},
	})
	if err != nil || res.Err != nil {
		t.Fatalf("execute: %v / %v", err, res.Err)
	}

	mu.Lock()
	defer mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for evidenceEvents == 0 && time.Now().Before(deadline) {
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
	}
	if evidenceEvents != 1 {
		t.Fatalf("execution.evidence fired %d times, want exactly 1 per termination", evidenceEvents)
	}
	if payload.Outcome != string(execution.EvidenceCommitted) ||
		payload.ContractID != res.Evidence.ContractID().String() ||
		payload.ContextDigest == "" || payload.RequestID != "p2-event" {
		t.Fatalf("evidence event lost authoritative facts: %+v", payload)
	}
	if payload.FinishedAt.Before(payload.StartedAt) {
		t.Fatal("evidence event time window inverted")
	}
}

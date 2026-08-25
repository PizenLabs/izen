// Phase 3 state-verification & OCC lock suite.
//
// These tests mechanically freeze the Phase 3 invariants:
//
//	The noop source-hash placeholder is eradicated: the production
//	authorization engine wires a REAL sha256 freshness verifier over the
//	declared mutation domain.
//
//	Baseline snapshotting is TARGET-SCOPED: the OCC engine never performs a
//	workspace-wide walk; it fingerprints exactly the contract's resolved
//	target geometry.
//
//	The pre-commit OCC gate runs in RuntimeExecutor.Approve BEFORE the apply
//	stage (before any final file write), and an ABORTED_OCC producer exists:
//	the abort is sealed as tainted, non-authoritative evidence while the
//	coarse outcome mapper stays free of the ABORTED_OCC derivation.
//
//	Out-of-band mid-execution modifications (LSP edits, external processes,
//	parallel tools) trigger ABORTED_OCC with ZERO partial writes — including
//	under genuine concurrency.
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
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/presentation"
)

// ── GUARD 3.1 — the noop verifier is eradicated ────────────────────────────

// TestPhase3NoopVerifierEradicated sweeps every production file of the
// authorization package for the historical noop placeholder. Its resurrection
// would silently disable the authorize-time freshness gate. The compiled
// wiring must additionally be REAL: the production constructor builds the
// sha256 verifier (structural) and denies a stale mutation domain at
// StepDependencyFreshness through the exported API (behavioral).
func TestPhase3NoopVerifierEradicated(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "authorization")

	for _, rel := range goFilesUnder(dir) {
		f, fset := parseFile(t, filepath.Join(dir, rel))
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "noopSourceHashVerifier" {
				t.Errorf("architecture: %s:%d resurrects noopSourceHashVerifier — the placeholder verifier model is abolished; the production sha256 OCC gate must stay wired",
					rel, fset.Position(id.Pos()).Line)
			}
			return true
		})
	}

	implF, _ := parseFile(t, filepath.Join(dir, "impl.go"))
	realVerifier := false
	ast.Inspect(implF, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "sha256SourceHashVerifier" {
			realVerifier = true
		}
		return true
	})
	if !realVerifier {
		t.Fatal("architecture: impl.go no longer declares the sha256SourceHashVerifier production type")
	}

	// Behavioral half, through EXPORTED APIs only: stale domain denied, fresh
	// domain authorized.
	ws := t.TempDir()
	p3WriteFile(t, ws, "main.go", "package main\n")
	p3WriteFile(t, filepath.Join(ws, ".izen", "checkpoints", "cp1"), "checkpoint.json", "{}")
	eng := authorization.NewProductionAuthorizationEngine(ws, func() workflow.WorkflowState { return workflow.StateBuilding })

	stale := p3MutationProposal("0000000000000000000000000000000000000000000000000000000000000000")
	if _, err := eng.Evaluate(stale, p3AuthorizedPlan(t), nil, p3CapSet(), budget.NewBudget(10, 500, 8000, 3, p3Minute(), 20), nil, false, true); err == nil {
		t.Fatal("production engine authorized a STALE source snapshot — the freshness gate is not real")
	} else {
		var denied *authorization.AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != authorization.StepDependencyFreshness {
			t.Fatalf("stale domain denied at the wrong step: %v", err)
		}
	}

	live, err := authorization.DomainSourceHash(ws, []string{"main.go"})
	if err != nil {
		t.Fatal(err)
	}
	auth, evalErr := eng.Evaluate(p3MutationProposal(live), p3AuthorizedPlan(t), nil, p3CapSet(),
		budget.NewBudget(10, 500, 8000, 3, p3Minute(), 20), nil, false, true)
	if evalErr != nil || auth == nil {
		t.Fatalf("production engine refused a FRESH source snapshot: %v / %v", evalErr, auth)
	}
}

// ── GUARD 3.2 — target-scoped baseline & pre-commit gate ordering ──────────

// TestPhase3OCCBaselineIsTargetScoped pins the performance boundary of the
// OCC engine structurally: occ.go must declare the baseline/verifier/conflict
// primitives and must NEVER contain a workspace-walk call (filepath.Walk /
// WalkDir / fs.WalkDir). Snapshotting is bounded to the declared targets.
func TestPhase3OCCBaselineIsTargetScoped(t *testing.T) {
	root := repoRoot(t)
	const occFile = "internal/execution/occ.go"
	f, fset := parseFile(t, filepath.Join(root, occFile))

	for _, typeName := range []string{"WorkspaceBaseline", "OCCVerifier", "WorkspaceStateConflict"} {
		found := false
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == typeName {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("architecture: %s must exist in %s — the OCC engine primitives drifted", typeName, occFile)
		}
	}

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Walk", "WalkDir":
			t.Errorf("architecture: %s:%d performs a workspace-wide walk — baseline snapshotting MUST stay strictly target-scoped",
				occFile, fset.Position(call.Pos()).Line)
		}
		return true
	})
}

// TestPhase3OCCGatePrecedesEveryWrite pins the commit-pipeline ordering
// structurally: inside RuntimeExecutor.Approve the VerifyAgainst call site
// must lexically precede the first ApplyContext call site — the OCC gate runs
// BEFORE any final file write, so an aborted attempt can never leave partial
// writes behind by construction.
func TestPhase3OCCGatePrecedesEveryWrite(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "internal", "execution", "executor.go"))

	approve := findFuncDecl(f, "Approve")
	if approve == nil {
		t.Fatal("architecture: RuntimeExecutor.Approve disappeared — the commit pipeline moved")
	}
	var verifyPos, applyPos token.Pos
	ast.Inspect(approve.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "VerifyAgainst":
			if verifyPos == token.NoPos {
				verifyPos = call.Pos()
			}
		case "ApplyContext":
			if applyPos == token.NoPos {
				applyPos = call.Pos()
			}
		}
		return true
	})
	if verifyPos == token.NoPos {
		t.Fatal("architecture: Approve lost its pre-commit VerifyAgainst call — the OCC commit gate was removed")
	}
	if applyPos == token.NoPos {
		t.Fatal("architecture: Approve lost its ApplyContext mutation stage — the lock inventory is stale")
	}
	if verifyPos > applyPos {
		t.Fatal("architecture: the OCC gate runs AFTER the apply stage — verification must precede every final file write")
	}
}

// ── GUARD 3.3 — ABORTED_OCC producer topology ──────────────────────────────

// TestPhase3AbortedOccProducerTopology locks where the ABORTED_OCC outcome may
// be born: the coarse outcome mapper stays free of it (Phase 2 continuity),
// the runtime's evidence seal produces it exclusively from the OccAborted
// flag, the flag is set on the Approve abort path, and OCC-aborted evidence is
// always forced tainted.
func TestPhase3AbortedOccProducerTopology(t *testing.T) {
	root := repoRoot(t)

	evF, _ := parseFile(t, filepath.Join(root, "internal", "execution", "evidence.go"))
	mapper := findFuncDecl(evF, "evidenceOutcomeFor")
	if mapper == nil {
		t.Fatal("architecture: evidenceOutcomeFor must remain the total coarse outcome mapping")
	}
	ast.Inspect(mapper.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "EvidenceAbortedOCC" {
			t.Errorf("architecture: evidenceOutcomeFor derived ABORTED_OCC — that outcome is born ONLY at the OCC commit gate")
		}
		return true
	})

	exF, _ := parseFile(t, filepath.Join(root, "internal", "execution", "executor.go"))
	seal := findFuncDecl(exF, "sealTerminalEvidence")
	if seal == nil {
		t.Fatal("architecture: sealTerminalEvidence must remain the evidence choke point")
	}
	producesAborted, forcesTaint := false, false
	ast.Inspect(seal.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "EvidenceAbortedOCC" {
			producesAborted = true
		}
		if assign, ok := n.(*ast.AssignStmt); ok {
			for _, lhs := range assign.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Tainted" {
					forcesTaint = true
				}
			}
		}
		return true
	})
	if !producesAborted {
		t.Fatal("architecture: sealTerminalEvidence no longer produces EvidenceAbortedOCC from Proof.OccAborted — the Phase 3 producer vanished")
	}
	if !forcesTaint {
		t.Fatal("architecture: sealTerminalEvidence no longer forces Tainted=true on OCC aborts")
	}

	approve := findFuncDecl(exF, "Approve")
	if approve == nil {
		t.Fatal("architecture: Approve vanished")
	}
	setsFlag := false
	scanOccAbortFlag := func(fn *ast.FuncDecl) {
		if fn == nil {
			return
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range assign.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "OccAborted" {
					setsFlag = true
				}
			}
			return true
		})
	}
	scanOccAbortFlag(approve)
	scanOccAbortFlag(findFuncDecl(exF, "abortOnStateConflict"))
	callsAbortHelper := false
	ast.Inspect(approve.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			callsAbortHelper = callsAbortHelper || fun.Name == "abortOnStateConflict"
		case *ast.SelectorExpr:
			callsAbortHelper = callsAbortHelper || fun.Sel.Name == "abortOnStateConflict"
		}
		return true
	})
	if !setsFlag || !callsAbortHelper {
		t.Fatal("architecture: the Approve OCC conflict path no longer seals Proof.OccAborted via abortOnStateConflict")
	}

	if string(execution.EvidenceAbortedOCC) != "ABORTED_OCC" {
		t.Fatalf("canonical label drifted: %q", execution.EvidenceAbortedOCC)
	}
}

// ── behavioral fixtures (exported-API only) ────────────────────────────────

const p3ExternalEdit = "external out-of-band edit\n"

func p3Minute() time.Duration { return time.Minute }

// p3FullContentProvider is a race-safe ai.Provider fake serving one fixed
// full-file artifact — used for creation-intent flows where no original
// content exists to anchor a SEARCH/REPLACE block.
type p3FullContentProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *p3FullContentProvider) Name() string { return "mock" }

func (p *p3FullContentProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return &ai.Response{Content: lockMutated}, nil
}

func (p *p3FullContentProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("streaming not supported by p3FullContentProvider")
}

func p3WriteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

func p3AuthorizedPlan(t *testing.T) artifact.Artifact {
	t.Helper()
	p := artifact.NewPlanArtifact([]string{"step1"}, "phase3-occ-lock")
	v := artifact.NewLifecycleTransitionValidator()
	if err := p.SetState(artifact.StateValidated, v); err != nil {
		t.Fatal(err)
	}
	if err := p.SetState(artifact.StateAuthorized, v); err != nil {
		t.Fatal(err)
	}
	return p
}

func p3MutationProposal(snapshotHash string) *authorization.MutationProposal {
	return &authorization.MutationProposal{
		IntentID:           artifact.NewArtifactID(artifact.ArtifactKindIntent),
		PlanID:             artifact.NewArtifactID(artifact.ArtifactKindPlan),
		TargetFiles:        []string{"main.go"},
		RequiredCaps:       authorization.CapFlagWrite | authorization.CapFlagPatch,
		EstimatedDelta:     budget.BudgetDelta{Files: 1, DiffLines: 10, Tokens: 100},
		SourceSnapshotHash: snapshotHash,
	}
}

func p3CapSet() *capability.CapabilitySet {
	cs := capability.NewCapabilitySet()
	cs.Grant(capability.CapabilityRead)
	cs.Grant(capability.CapabilityWrite)
	cs.Grant(capability.CapabilityPatch)
	cs.Grant(capability.CapabilityExecute)
	return cs
}

func p3Executor(t *testing.T, root string) (*execution.RuntimeExecutor, *lockScriptedProvider) {
	t.Helper()
	x := execution.NewRuntimeExecutor(root, config.Default(), &lockScriptedProvider{responses: 1}, nil, "")
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:       authorization.NewAuthorizationID(),
		IssuedAt: rightNow(),
	})
	return x, nil
}

// ── behavioral: clean-commit control ───────────────────────────────────────

// TestPhase3CleanCommitStillCommits is the positive control proving the OCC
// gate does not over-block: with NO out-of-band divergence the held patch
// commits normally with COMMITTED untainted evidence and the workspace shows
// the applied change.
func TestPhase3CleanCommitStillCommits(t *testing.T) {
	root := t.TempDir()
	lockWriteTarget(t, root, lockOriginal)
	x, _ := p3Executor(t, root)

	res, err := x.Execute(context.Background(), lockFrozenMutationIntent(root))
	if err != nil || res.PendingPatchID == "" {
		t.Fatalf("execute: %v / pending=%q", err, res.PendingPatchID)
	}
	// The Execute result is approval-held: no evidence exists until the gate
	// resolves.
	if res.Evidence != nil {
		t.Fatal("approval-held execution sealed evidence prematurely")
	}
	approveRes, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("clean commit blocked by the OCC gate: %v", err)
	}
	if got := lockReadTarget(t, root); got != lockMutated {
		t.Fatalf("clean commit did not apply: %q", got)
	}
	if approveRes.Evidence == nil || !approveRes.Evidence.Authoritative() || approveRes.Evidence.Outcome() != execution.EvidenceCommitted {
		t.Fatalf("clean commit evidence wrong: %+v", approveRes.Evidence)
	}
	if m := approveRes.Evidence.Mutations(); m.Tainted || m.FilesMutated != 1 {
		t.Fatalf("clean commit mutation summary wrong: %+v", m)
	}

	occ := x.OCC().Metrics()
	if occ.Verifications < 1 || occ.Snapshots < 1 {
		t.Fatalf("executor did not run the OCC engine on the commit path: %+v", occ)
	}
	if occ.Mismatches != 0 {
		t.Fatalf("clean run recorded mismatches: %+v", occ)
	}
	if occ.CacheHits < 1 {
		t.Fatalf("no fingerprint cache hit recorded for unchanged targets: %+v", occ)
	}
	if occ.VerifyDuration() < 0 || occ.SnapshotDuration() < 0 {
		t.Fatalf("negative OCC durations: %+v", occ)
	}
}

// ── behavioral: deterministic out-of-band conflicts ────────────────────────

// assertPhase3OccAbort collects the invariants EVERY OCC abort must satisfy:
// the sentinel error, the sealed ABORTED_OCC outcome, tainted non-authoritative
// evidence, zero durable mutations, and a consumed approval surface.
func assertPhase3OccAbort(t *testing.T, approveErr error, res *execution.ExecutionResult) {
	t.Helper()
	if approveErr == nil {
		t.Fatal("out-of-band divergence committed anyway — the OCC gate failed closed expectation")
	}
	if !errors.Is(approveErr, execution.ErrWorkspaceStateConflict) {
		t.Fatalf("abort error does not wrap ErrWorkspaceStateConflict: %v", approveErr)
	}
	if res.Evidence == nil {
		t.Fatal("OCC abort sealed no evidence")
	}
	if res.Evidence.Outcome() != execution.EvidenceAbortedOCC {
		t.Fatalf("abort outcome = %q, want ABORTED_OCC", res.Evidence.Outcome())
	}
	m := res.Evidence.Mutations()
	if !m.Tainted {
		t.Fatal("OCC abort evidence is not tainted — projectors would keep tentative state")
	}
	if m.ApplyExecuted || m.FilesMutated != 0 {
		t.Fatalf("abort claims applied work: %+v", m)
	}
	if res.Evidence.Authoritative() {
		t.Fatal("OCC abort evidence projects as authoritative success")
	}
	if p := presentation.ProjectEvidence(res.Evidence); p.Granted() {
		t.Fatalf("presentation gate granted authority for ABORTED_OCC evidence: %+v", p)
	}
}

// TestPhase3OutOfBandMidExecutionEditAbortsClean drives the core Phase 3
// scenario deterministically: the target is modified out-of-band AFTER the
// intent was admitted and HELD at the approval gate (mid-execution). Approve
// must abort before ANY write: the workspace keeps byte-for-byte the external
// writer's content and nothing else changes.
func TestPhase3OutOfBandMidExecutionEditAbortsClean(t *testing.T) {
	root := t.TempDir()
	lockWriteTarget(t, root, lockOriginal)
	x, _ := p3Executor(t, root)

	res, err := x.Execute(context.Background(), lockFrozenMutationIntent(root))
	if err != nil || res.PendingPatchID == "" {
		t.Fatalf("execute: %v / pending=%q", err, res.PendingPatchID)
	}

	// Out-of-band writer (LSP edit / formatter / parallel tool) strikes while
	// the proposal sits at the approval gate.
	p3WriteFile(t, root, lockTargetFile, p3ExternalEdit)

	approveRes, approveErr := x.Approve(context.Background(), res.PendingPatchID)
	assertPhase3OccAbort(t, approveErr, approveRes)

	if got := lockReadTarget(t, root); got != p3ExternalEdit {
		t.Fatalf("partial write leaked past the OCC abort: %q", got)
	}
	if pending := x.PendingPatchIDs(); len(pending) != 0 {
		t.Fatalf("aborted approval surface survived: %v", pending)
	}

	occ := x.OCC().Metrics()
	if occ.Mismatches != 1 || occ.ConflictsFound != 1 {
		t.Fatalf("conflict telemetry missing: %+v", occ)
	}
}

// TestPhase3OutOfBandDeletionAbortsClean proves the deletion flip aborts too,
// leaves the file deleted (no resurrection write) and adds nothing to the
// workspace.
func TestPhase3OutOfBandDeletionAbortsClean(t *testing.T) {
	root := t.TempDir()
	lockWriteTarget(t, root, lockOriginal)
	x, _ := p3Executor(t, root)

	res, err := x.Execute(context.Background(), lockFrozenMutationIntent(root))
	if err != nil || res.PendingPatchID == "" {
		t.Fatalf("execute: %v / pending=%q", err, res.PendingPatchID)
	}
	if err := os.Remove(filepath.Join(root, lockTargetFile)); err != nil {
		t.Fatal(err)
	}

	before := p3WorkspaceEntries(t, root)
	approveRes, approveErr := x.Approve(context.Background(), res.PendingPatchID)
	assertPhase3OccAbort(t, approveErr, approveRes)

	if after := p3WorkspaceEntries(t, root); len(before) != len(after) {
		t.Fatalf("aborted execution changed workspace membership: before=%v after=%v", before, after)
	}
	if _, err := os.Stat(filepath.Join(root, lockTargetFile)); !os.IsNotExist(err) {
		t.Fatal("aborted execution resurrected the deleted target")
	}
}

// TestPhase3CreationIntentCommitsWhenUncontended proves the baseline respects
// creation geometry: a target absent at admission that REMAINS absent commits
// normally (the gate protects admitted state, it does not forbid creation).
func TestPhase3CreationIntentCommitsWhenUncontended(t *testing.T) {
	root := t.TempDir()
	provider := &p3FullContentProvider{}
	x := execution.NewRuntimeExecutor(root, config.Default(), provider, nil, "")
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:       authorization.NewAuthorizationID(),
		IssuedAt: rightNow(),
	})

	res, err := x.Execute(context.Background(), lockFrozenMutationIntent(root))
	if err != nil || res.PendingPatchID == "" {
		t.Fatalf("execute: %v / pending=%q", err, res.PendingPatchID)
	}
	approveRes, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("uncontended creation intent blocked: %v", err)
	}
	if got := lockReadTarget(t, root); got != lockMutated {
		t.Fatalf("creation commit wrote wrong content: %q", got)
	}
	if approveRes.Evidence == nil || !approveRes.Evidence.Outcome().Committed() || approveRes.Evidence.Mutations().Tainted {
		t.Fatalf("creation evidence wrong: %+v", approveRes.Evidence)
	}
}

// ── behavioral: concurrency ────────────────────────────────────────────────

// TestPhase3ConcurrentOutBandWriterNeverPersistsPartialState races a hostile
// out-of-band writer against the whole execute→approve window, repeatedly, so
// interleavings land on both sides of the gate. The invariant under test: the
// final workspace is ALWAYS a complete state — either the executor's full
// committed truth or the writer's content — NEVER a partial mix, and every
// conflicted attempt terminates as tainted ABORTED_OCC evidence.
func TestPhase3ConcurrentOutBandWriterNeverPersistsPartialState(t *testing.T) {
	for i := 0; i < 12; i++ {
		root := t.TempDir()
		lockWriteTarget(t, root, lockOriginal)
		x, _ := p3Executor(t, root)

		res, err := x.Execute(context.Background(), lockFrozenMutationIntent(root))
		if err != nil || res.PendingPatchID == "" {
			t.Fatalf("iteration %d execute: %v / pending=%q", i, err, res.PendingPatchID)
		}

		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = os.WriteFile(filepath.Join(root, lockTargetFile), []byte(p3ExternalEdit), 0o644)
				}
			}
		}()

		approveRes, approveErr := x.Approve(context.Background(), res.PendingPatchID)
		close(stop)
		wg.Wait()

		got := lockReadTarget(t, root)
		// GLOBAL invariant: the persisted file is ALWAYS a complete state —
		// the writer's content, the executor's full committed truth, or the
		// rolled-back pre-mutation original. Any other bytes are a partial
		// write leak.
		switch got {
		case p3ExternalEdit, lockMutated, lockOriginal:
		default:
			t.Fatalf("iteration %d: PARTIAL WRITE persisted: %q", i, got)
		}

		switch {
		case errors.Is(approveErr, execution.ErrWorkspaceStateConflict):
			// OCC conflict path: zero writes by the executor — the disk holds
			// ONLY the external writer's content.
			if got != p3ExternalEdit {
				t.Fatalf("iteration %d: partial state after OCC abort: %q", i, got)
			}
			assertPhase3OccAbort(t, approveErr, approveRes)
		case approveErr != nil:
			// The racing writer landed inside the APPLY window (after the gate
			// verified clean): a deterministic apply failure with full
			// rollback. Never authoritative truth.
			if approveRes.Evidence == nil || approveRes.Evidence.Authoritative() {
				t.Fatalf("iteration %d: apply-failure evidence wrong: %+v", i, approveRes.Evidence)
			}
			if got == lockOriginal && !approveRes.Evidence.Mutations().Tainted {
				t.Fatalf("iteration %d: rolled-back apply not tainted: %+v", i, approveRes.Evidence.Mutations())
			}
		case got == p3ExternalEdit:
			// The writer overwrote after a legitimate commit: the executor
			// still terminated cleanly (the race window closed before the
			// gate saw it).
			if approveRes.Evidence == nil || !approveRes.Evidence.Outcome().Committed() {
				t.Fatalf("iteration %d: clean-path evidence wrong: %+v", i, approveRes.Evidence)
			}
		default:
			// Full committed truth.
			if approveRes.Evidence == nil || !approveRes.Evidence.Outcome().Committed() || approveRes.Evidence.Mutations().Tainted {
				t.Fatalf("iteration %d: commit evidence wrong: %+v", i, approveRes.Evidence)
			}
		}
	}
}

// p3WorkspaceEntries lists the workspace top-level entries (membership check).
func p3WorkspaceEntries(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

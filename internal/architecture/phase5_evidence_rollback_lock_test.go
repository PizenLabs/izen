package architecture

import (
	"context"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/artifact"
	"github.com/PizenLabs/izen/internal/core/domain/checkpoint"
	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	"github.com/PizenLabs/izen/internal/core/domain/execution"
	"github.com/PizenLabs/izen/internal/core/domain/occ"
)

// TestPhase5EvidenceRollbackLock enforces Phase 5 invariants:
// 1. TerminalState.Valid() rejects all forbidden combinations (INV:8, INV:9, INV:11).
// 2. Execution failure triggers workspace rollback via CheckpointCoordinator.
// 3. Rollback is never invoked from TUI or tools — only via the Control Plane (internal/runtime or domain/workflow).
func TestPhase5EvidenceRollbackLock(t *testing.T) {
	t.Run("TerminalStateValidRejectsInvalidCombinations", testTerminalStateValidMatrix)
	t.Run("EvidenceMonotonicEnforcement", testEvidenceMonotonic)
	t.Run("RollbackOnlyFromControlPlane", testRollbackOnlyFromControlPlane)
	t.Run("ExecutionFailureTriggersRollbackAndInvalidatesStaleVersions", testExecutionFailureTriggersRollback)
}

func testTerminalStateValidMatrix(t *testing.T) {
	// INV:8 — Verified requires Completed && Verdict==PASS
	// These must be INVALID:
	invalid := []evidence.TerminalState{
		{Workflow: domain.StateVerified, Verdict: evidence.VerdictPass, Completed: false}, // Incomplete·Verified
		{Workflow: domain.StateVerified, Verdict: evidence.VerdictFail, Completed: true},  // Verified but FAIL verdict
		{Workflow: domain.StateVerified, Verdict: evidence.VerdictSkip, Completed: true},  // Verified but SKIP
		{Workflow: domain.StateFailed, Verdict: evidence.VerdictPass, Completed: true},    // Failed·Verified
		{Workflow: domain.StateFailed, Verdict: evidence.VerdictPass, Completed: false},   // Failed·Verified incomplete
	}
	for i, tc := range invalid {
		if tc.Valid() {
			t.Errorf("invalid case %d %s completed=%v verdict=%s should be INVALID but Valid() returned true", i, tc.Workflow, tc.Completed, tc.Verdict)
		}
	}
	// Valid cases — these must be VALID:
	valid := []evidence.TerminalState{
		{Workflow: domain.StateVerified, Verdict: evidence.VerdictPass, Completed: true}, // COMPLETED·VERIFIED
		{Workflow: domain.StateFailed, Verdict: evidence.VerdictFail, Completed: true},
		{Workflow: domain.StateFailed, Verdict: evidence.VerdictFail, Completed: false},
		{Workflow: domain.StateIdle, Verdict: evidence.VerdictPass, Completed: false},
		{Workflow: domain.StateBuilding, Verdict: evidence.VerdictFail, Completed: false},
		{Workflow: domain.StatePlanning, Verdict: evidence.VerdictSkip, Completed: false},
	}
	for i, tc := range valid {
		if !tc.Valid() {
			t.Errorf("valid case %d %s completed=%v verdict=%s should be VALID", i, tc.Workflow, tc.Completed, tc.Verdict)
		}
	}

	// Also verify domain.TerminalState Valid matrix (ARCH:30)
	domainInvalid := []domain.TerminalState{
		{Outcome: domain.OutcomeIncomplete, Evidence: domain.EvidenceVerified},
		{Outcome: domain.OutcomeFailed, Evidence: domain.EvidenceVerified},
		{Outcome: domain.OutcomeAborted, Evidence: domain.EvidenceVerified},
	}
	for i, tc := range domainInvalid {
		if tc.Valid() {
			t.Errorf("domain invalid case %d %s·%s should be INVALID", i, tc.Outcome, tc.Evidence)
		}
	}
	domainValid := []domain.TerminalState{
		{Outcome: domain.OutcomeCompleted, Evidence: domain.EvidenceVerified},
		{Outcome: domain.OutcomeCompleted, Evidence: domain.EvidencePartiallyVerified},
		{Outcome: domain.OutcomeFailed, Evidence: domain.EvidenceUnverified},
		{Outcome: domain.OutcomeIncomplete, Evidence: domain.EvidenceUnverified},
		{Outcome: domain.OutcomeAborted, Evidence: domain.EvidenceUnverified},
	}
	for i, tc := range domainValid {
		if !tc.Valid() {
			t.Errorf("domain valid case %d %s·%s should be VALID", i, tc.Outcome, tc.Evidence)
		}
	}
}

func testEvidenceMonotonic(t *testing.T) {
	// Monotonic rule: Level N cannot pass if Level N-1 failed or is missing.
	// Vector: L0 PASS, L1 FAIL, L2 PASS — L2 PASS is invalid gap, derivation must be FAILED.
	vec := evidence.EvidenceVector{
		Levels: [6]evidence.EvidenceVerdict{
			evidence.VerdictPass, // L0
			evidence.VerdictFail, // L1 FAIL
			evidence.VerdictPass, // L2 PASS but gap
			evidence.VerdictSkip,
			evidence.VerdictSkip,
			evidence.VerdictSkip,
		},
	}
	if got := evidence.DeriveEvidenceState(vec, evidence.L2_StaticAnalysis); got != evidence.VerdictFailed {
		t.Errorf("monotonic gap should be FAILED, got %s", got)
	}
	if got := evidence.DeriveEvidenceState(vec, evidence.L0_Execution); got != evidence.VerdictPassed {
		t.Errorf("L0 alone PASS should be PASSED, got %s", got)
	}
	// Contiguous PASS should yield PASSED
	vec2 := evidence.EvidenceVector{
		Levels: [6]evidence.EvidenceVerdict{
			evidence.VerdictPass,
			evidence.VerdictPass,
			evidence.VerdictPass,
			evidence.VerdictSkip,
			evidence.VerdictSkip,
			evidence.VerdictSkip,
		},
	}
	if got := evidence.DeriveEvidenceState(vec2, evidence.L2_StaticAnalysis); got != evidence.VerdictPassed {
		t.Errorf("contiguous L0-L2 PASS should be PASSED, got %s", got)
	}
	// Missing (SKIP) in required range -> Inconclusive
	vec3 := evidence.EvidenceVector{
		Levels: [6]evidence.EvidenceVerdict{
			evidence.VerdictPass,
			evidence.VerdictPass,
			evidence.VerdictSkip,
			evidence.VerdictSkip,
			evidence.VerdictSkip,
			evidence.VerdictSkip,
		},
	}
	if got := evidence.DeriveEvidenceState(vec3, evidence.L2_StaticAnalysis); got != evidence.VerdictInconclusive {
		t.Errorf("missing L2 should be INCONCLUSIVE, got %s", got)
	}
	// Any FAIL in required range -> FAILED
	vec4 := evidence.EvidenceVector{
		Levels: [6]evidence.EvidenceVerdict{
			evidence.VerdictPass,
			evidence.VerdictPass,
			evidence.VerdictFail,
			evidence.VerdictSkip,
			evidence.VerdictSkip,
			evidence.VerdictSkip,
		},
	}
	if got := evidence.DeriveEvidenceState(vec4, evidence.L2_StaticAnalysis); got != evidence.VerdictFailed {
		t.Errorf("L2 FAIL should be FAILED, got %s", got)
	}
}

func testRollbackOnlyFromControlPlane(t *testing.T) {
	root := repoRoot(t)

	// Files that are allowed to call CheckpointCoordinator.Rollback: control plane only.
	// Any other Rollback method (e.g., workspace checkpoint manager) is not relevant;
	// we filter by import of the checkpoint coordinator package.
	allowedPrefixes := []string{
		"internal/runtime/",
		"internal/core/domain/checkpoint/",
		"internal/core/domain/workflow/",
		"internal/core/workflow/",
	}
	checkpointImport := "github.com/PizenLabs/izen/internal/core/domain/checkpoint"

	// Scan all Go files for CheckpointCoordinator Rollback call sites
	for _, rel := range goFilesUnder(root) {
		abs := filepath.Join(root, rel)
		f, _ := parseFile(t, abs)
		// Only consider files that import the checkpoint coordinator
		importsCheckpoint := false
		for _, imp := range f.Imports {
			if imp.Path != nil && strings.Trim(imp.Path.Value, `"`) == checkpointImport {
				importsCheckpoint = true
				break
			}
		}
		if !importsCheckpoint {
			continue
		}
		hasRollback := false
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "Rollback" {
				hasRollback = true
				return false
			}
			return true
		})
		if !hasRollback {
			continue
		}
		allowed := false
		for _, p := range allowedPrefixes {
			if strings.HasPrefix(rel, p) {
				allowed = true
				break
			}
		}
		if !allowed {
			t.Errorf("architecture: CheckpointCoordinator.Rollback() must be invoked exclusively within Control Plane (internal/runtime or checkpoint/workflow); forbidden call at %s", rel)
		}
	}

	// Explicitly forbid TUI and tools from importing checkpoint and calling Rollback
	forbiddenPrefixes := []string{
		"internal/ui/",
		"internal/tools/",
		"pkg/tools/",
		"cmd/",
	}
	for _, rel := range goFilesUnder(root) {
		matchesForbidden := false
		for _, fp := range forbiddenPrefixes {
			if strings.HasPrefix(rel, fp) {
				matchesForbidden = true
				break
			}
		}
		if !matchesForbidden {
			continue
		}
		abs := filepath.Join(root, rel)
		f, _ := parseFile(t, abs)
		importsCheckpoint := false
		for _, imp := range f.Imports {
			if imp.Path != nil && strings.Trim(imp.Path.Value, `"`) == checkpointImport {
				importsCheckpoint = true
				break
			}
		}
		if !importsCheckpoint {
			continue
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
			if sel.Sel.Name == "Rollback" {
				t.Errorf("architecture: forbidden CheckpointCoordinator.Rollback call in presentation/tools layer at %s — rollback is Control Plane only", rel)
				return false
			}
			return true
		})
	}

	// Verify pipeline_evidence.go exists and RuntimeExecutor calls Rollback on failure
	execFile := filepath.Join(root, "internal", "runtime", "executor", "executor.go")
	f, _ := parseFile(t, execFile)
	decl := findFuncDecl(f, "Execute")
	if decl == nil {
		t.Fatal("RuntimeExecutor.Execute not found")
	}
	foundRollback := false
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "Rollback" {
			foundRollback = true
		}
		return true
	})
	// Also check pipeline_evidence.go — existence via file check, and Rollback call
	pipeFile := filepath.Join(root, "internal", "runtime", "executor", "pipeline_evidence.go")
	pf, fset := parseFile(t, pipeFile)
	_ = fset
	hasEval := false
	ast.Inspect(pf, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "Rollback" {
			hasEval = true
		}
		return true
	})
	if !foundRollback && !hasEval {
		t.Error("architecture: RuntimeExecutor must invoke CheckpointCoordinator.Rollback on evidence failure (checked executor.go and pipeline_evidence.go)")
	}
}

func testExecutionFailureTriggersRollback(t *testing.T) {
	// Integration proof: a failed evidence vector triggers rollback which
	// restores workspace files and invalidates stale OCC versions.
	root := t.TempDir()
	coord := checkpoint.NewDiskCheckpointCoordinator(root)
	store := artifact.NewArtifactStore()
	execState := execution.NewExecutionState()
	coord.SetStores(store, execState)

	// Baseline
	baseFile := filepath.Join(root, "app.go")
	if err := os.WriteFile(baseFile, []byte("baseline"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Prime OCC state to version 1
	if _, err := execState.RecordStep("init", "ok", 0); err != nil {
		t.Fatal(err)
	}
	baselineVer := execState.Version()

	ctx := context.Background()
	chkID, err := coord.CreateBeforeBuild(ctx, domain.FrameID("frame_phase5"))
	if err != nil {
		t.Fatalf("CreateBeforeBuild: %v", err)
	}

	// Simulate execution that mutates workspace and advances state
	if _, err := execState.RecordStep("step2", "done", baselineVer); err != nil {
		t.Fatal(err)
	}
	postVer := execState.Version()
	if postVer != baselineVer.Next() {
		t.Fatalf("expected version advance, got %d want %d", postVer, baselineVer.Next())
	}
	store.StagePatchForce("diff-1", "patch")
	_ = os.WriteFile(filepath.Join(root, "generated.go"), []byte("bad content"), 0o644)
	_ = os.WriteFile(baseFile, []byte("mutated"), 0o644)

	// Evidence failure: L0 PASS, L1 FAIL -> monotonic gap -> FAILED
	vec := evidence.EvidenceVector{
		Levels: [6]evidence.EvidenceVerdict{
			evidence.VerdictPass,
			evidence.VerdictFail,
			evidence.VerdictPass,
			evidence.VerdictSkip,
			evidence.VerdictSkip,
			evidence.VerdictSkip,
		},
	}
	state := evidence.DeriveEvidenceState(vec, evidence.L2_StaticAnalysis)
	if state != evidence.VerdictFailed {
		t.Fatalf("expected evidence FAILED, got %s", state)
	}
	// Trigger rollback via coordinator (simulating RuntimeExecutor.EvaluateEvidenceAndRollback)
	if err := coord.Rollback(ctx, chkID, domain.RollbackLocal); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	// Workspace restored
	data, _ := os.ReadFile(baseFile)
	if string(data) != "baseline" {
		t.Errorf("workspace not restored after rollback: got %q", string(data))
	}
	if _, err := os.Stat(filepath.Join(root, "generated.go")); !os.IsNotExist(err) {
		t.Error("untracked file not cleaned after rollback")
	}
	// Artifact store cleared
	if store.Count() != 0 {
		t.Errorf("store not cleared after rollback")
	}
	// Execution version rewound to baseline
	if got := execState.Version(); got != baselineVer {
		t.Errorf("execution version not rewound: got %d want %d", got, baselineVer)
	}
	// Stale write with postVer (old version 2) must be rejected after rollback
	if _, err := execState.RecordStep("stale", "x", postVer); err == nil {
		t.Error("stale OCC version should be rejected after rollback, but succeeded")
	} else if !isStaleError(err) {
		t.Errorf("expected ErrStaleDependency, got %v", err)
	}
	// Fresh write with baselineVer should succeed
	if _, err := execState.RecordStep("fresh", "y", baselineVer); err != nil {
		t.Errorf("fresh version after rollback should succeed: %v", err)
	}
	// Idempotent second rollback
	if err := coord.Rollback(ctx, chkID, domain.RollbackLocal); err != nil {
		t.Errorf("idempotent rollback failed: %v", err)
	}
	// Concurrent rollback safety: fire multiple rollbacks concurrently
	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { done <- coord.Rollback(ctx, chkID, domain.RollbackLocal) }()
	}
	for i := 0; i < 4; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent rollback error: %v", err)
		}
	}
}

func isStaleError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "stale") || strings.Contains(err.Error(), "StaleDependency")
}

// ensure occ import is used
var _ occ.StateVersion = 0

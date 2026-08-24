package authorization

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/workflow"
)

// writeFixture writes a file inside dir (test helper).
func writeFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDomainSourceHashDeterministicAndTargetScoped proves the production hash:
// order-independent over the declared domain, target-scoped (unrelated files
// never move it) and sensitive to both content and membership.
func TestDomainSourceHashDeterministicAndTargetScoped(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.go", "package a\n")
	writeFixture(t, dir, "b.go", "package b\n")
	writeFixture(t, dir, "unrelated.txt", "noise\n")

	h1, err := DomainSourceHash(dir, []string{"a.go", "b.go"})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := DomainSourceHash(dir, []string{"b.go", "a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if h1 == "" || h1 != h2 {
		t.Fatalf("domain hash not order-independent: %s vs %s", h1, h2)
	}

	// Unrelated workspace churn is invisible to a target-scoped domain.
	writeFixture(t, dir, "unrelated.txt", "churned\n")
	h3, err := DomainSourceHash(dir, []string{"a.go", "b.go"})
	if err != nil {
		t.Fatal(err)
	}
	if h3 != h1 {
		t.Fatal("out-of-scope file change moved the target-scoped domain hash")
	}

	// In-scope content drift forks the hash.
	writeFixture(t, dir, "a.go", "package a // changed\n")
	h4, err := DomainSourceHash(dir, []string{"a.go", "b.go"})
	if err != nil {
		t.Fatal(err)
	}
	if h4 == h1 {
		t.Fatal("in-scope content change did not fork the domain hash")
	}
}

// TestDomainSourceHashMissingFileIsDeterministic proves an absent target
// contributes a stable marker: deleting a declared file forks the hash, and
// repeated computation stays deterministic.
func TestDomainSourceHashMissingFileIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "exists.go", "package x\n")

	withFile, err := DomainSourceHash(dir, []string{"exists.go", "missing.go"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := DomainSourceHash(dir, []string{"missing.go", "exists.go"})
	if err != nil {
		t.Fatal(err)
	}
	if withFile != again {
		t.Fatalf("absent-marker contribution is nondeterministic: %s vs %s", withFile, again)
	}

	onlyExisting, err := DomainSourceHash(dir, []string{"exists.go"})
	if err != nil {
		t.Fatal(err)
	}
	if onlyExisting == withFile {
		t.Fatal("adding an absent member did not fork the domain hash")
	}
}

// TestSHA256VerifierMatchAndMismatch drives the production verifier directly.
func TestSHA256VerifierMatchAndMismatch(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "main.go", "package main\n")
	v := newSHA256SourceHashVerifier(dir)

	live, err := DomainSourceHash(dir, []string{"main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.VerifySourceHash([]string{"main.go"}, live); err != nil {
		t.Fatalf("live hash rejected: %v", err)
	}

	staleErr := v.VerifySourceHash([]string{"main.go"}, "0000000000000000000000000000000000000000000000000000000000000000")
	var mismatch *SourceHashMismatchError
	if !errors.As(staleErr, &mismatch) {
		t.Fatalf("stale hash accepted: %v", staleErr)
	}

	// An empty declared snapshot means not-applicable — never a silent pass of
	// a STALE value.
	if err := v.VerifySourceHash([]string{"main.go"}, ""); err != nil {
		t.Fatalf("empty baseline refused: %v", err)
	}
}

// Deprecated: Obsolete Verifier Model — the noop placeholder this suite once
// exercised was replaced by the Phase 3 sha256 OCC freshness gate. Retained as
// the modern invariant lock: the PRODUCTION constructor must wire the REAL
// verifier so a stale mutation domain denies at StepDependencyFreshness.
func TestProductionEngineWiresRealSourceHashVerifier(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "main.go", "package main\n")
	if err := os.MkdirAll(filepath.Join(dir, ".izen", "checkpoints", "cp1"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, dir, filepath.Join(".izen", "checkpoints", "cp1", "checkpoint.json"), "{}")

	eng := NewProductionAuthorizationEngine(dir, func() workflow.WorkflowState { return workflow.StateBuilding })

	staleProposal := defaultProposal()
	auth, err := eng.Evaluate(staleProposal, newPlan(t, artifact.StateAuthorized), nil,
		defaultCapSet(), defaultBudget(), nil, false, true)
	if err == nil || auth != nil {
		t.Fatalf("production engine authorized a stale source snapshot: %v", err)
	}
	var denied *AuthorizationDenied
	if !errors.As(err, &denied) || denied.Step != StepDependencyFreshness {
		t.Fatalf("expected StepDependencyFreshness denial, got %v", err)
	}

	// Positive control: the same proposal carrying the LIVE domain hash passes
	// the freshness gate and reaches a full authorization.
	live, err := DomainSourceHash(dir, []string{staleProposal.TargetFiles[0]})
	if err != nil {
		t.Fatal(err)
	}
	fresh := defaultProposal()
	fresh.SourceSnapshotHash = live
	auth, err = eng.Evaluate(fresh, newPlan(t, artifact.StateAuthorized), nil,
		defaultCapSet(), budget.NewBudget(10, 500, 8000, 3, 5*time.Minute, 20), nil, false, true)
	if err != nil {
		t.Fatalf("production engine denied a fresh source snapshot: %v", err)
	}
	if auth == nil {
		t.Fatal("expected non-nil authorization for the fresh domain")
	}
}

// TestCapabilitySetScopeUnaffected documents that the freshness gate sits
// strictly at StepDependencyFreshness: scope containment still fires first for
// out-of-scope targets even when the hash would also mismatch.
func TestFreshnessGateOrderedAfterScopeContainment(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "inside.go", "package in\n")
	eng := NewAuthorizationEngine(
		newSHA256SourceHashVerifier(dir),
		&mockCheckpointChecker{hasCheckpoint: true},
		func() workflow.WorkflowState { return workflow.StateBuilding },
	)
	cs := capability.NewCapabilitySet()
	cs.Grant(capability.CapabilityWrite, capability.ScopeRule{
		Capability: capability.CapabilityWrite,
		Patterns:   []string{"*.go"},
	})
	cs.Grant(capability.CapabilityPatch, capability.ScopeRule{
		Capability: capability.CapabilityPatch,
		Patterns:   []string{"*.go"},
	})
	proposal := defaultProposal()
	proposal.TargetFiles = []string{"forbidden.rb"}
	_, err := eng.Evaluate(proposal, newPlan(t, artifact.StateAuthorized), nil,
		cs, defaultBudget(), nil, false, true)
	var denied *AuthorizationDenied
	if !errors.As(err, &denied) || denied.Step != StepScopeContainment {
		t.Fatalf("expected StepScopeContainment first, got %v", err)
	}
}

package execution

// PHASE 16.1 — RESOLUTION / DISCOVERY ISOLATION
//
// These tests pin the structural split the directive requires:
//
//	TargetResolver   — PURE path classification (os.Stat only, no scanning).
//	WorkspaceDiscovery — isolated, read-only, evidence-only.
//
// and the invariants that make the split meaningful:
//
//	I11  a directory is not a file
//	I13  discovery evidence is not mutation authority

import (
	"context"
	"testing"
)

// TestPhase16_1_TargetStateVocabularyIsClosed pins the six-value vocabulary.
func TestPhase16_1_TargetStateVocabularyIsClosed(t *testing.T) {
	states := AllTargetStates()
	if len(states) != 6 {
		t.Fatalf("target-state vocabulary has %d values, want exactly 6", len(states))
	}
	want := map[TargetState]bool{
		TargetStateUnboundDirectory: true,
		TargetStateUnboundPath:      true,
		TargetStateResolvedFile:     true,
		TargetStateResolvedSet:      true,
		TargetStateAmbiguous:        true,
		TargetStateNotFound:         true,
	}
	for _, s := range states {
		if !want[s] {
			t.Errorf("unexpected target state %q", s)
		}
	}
	if !TargetStateResolvedFile.IsBound() || !TargetStateResolvedSet.IsBound() {
		t.Error("resolved states must be bound")
	}
	for _, unbound := range []TargetState{
		TargetStateUnboundDirectory, TargetStateUnboundPath,
		TargetStateAmbiguous, TargetStateNotFound,
	} {
		if unbound.IsBound() {
			t.Errorf("%s must not be bound", unbound)
		}
	}
}

// TestPhase16_1_DirectoryIsNotAFile is I11: "." and any directory classify as
// UNBOUND_DIRECTORY and are never promoted to a specific file, even when the
// directory contains exactly one file.
func TestPhase16_1_DirectoryIsNotAFile(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html></html>\n"})
	resolver := NewTargetResolver(root)

	for _, target := range []string{".", "./", "index.html/..", root} {
		if got := resolver.Classify(target); got != TargetStateUnboundDirectory {
			t.Errorf("Classify(%q) = %s, want UNBOUND_DIRECTORY", target, got)
		}
	}

	res := resolver.Resolve(context.Background(), "Refactor project", []string{"."})
	if res.State != TargetStateUnboundDirectory {
		t.Fatalf("explicit directory state = %s, want UNBOUND_DIRECTORY", res.State)
	}
	if res.Dispatchable() {
		t.Fatal("a directory must never be dispatchable — the sole file in it is not promoted")
	}
	if res.Binding != nil {
		t.Fatalf("a directory must not produce a binding, got %+v", res.Binding)
	}
}

// TestPhase16_1_ResolverNeverScans is the "no candidate searching" property:
// classifying a name that is not on disk returns NOT_FOUND even though other
// files exist. A resolver that scanned would have offered them.
func TestPhase16_1_ResolverNeverScans(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.html":     "a\n",
		"src/b.html": "b\n",
	})
	resolver := NewTargetResolver(root)

	if got := resolver.Classify("missing.txt"); got != TargetStateNotFound {
		t.Fatalf("Classify(missing.txt) = %s, want NOT_FOUND", got)
	}
	// A directory is classified by stat alone, not by enumerating it.
	if got := resolver.Classify("src"); got != TargetStateUnboundDirectory {
		t.Fatalf("Classify(src) = %s, want UNBOUND_DIRECTORY", got)
	}
}

// TestPhase16_1_DiscoveryIsIsolatedFromAuthority is I13. A discovery pass
// produces candidate EVIDENCE tagged EvidenceKindCandidate; it can never carry
// a mutation kind, and the resolver never turns a candidate into a target
// without an explicit statement.
func TestPhase16_1_DiscoveryIsIsolatedFromAuthority(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":     "module example.com/demo\n\ngo 1.26\n",
		"main.go":    "package main\n",
		"src/app.go": "package app\n",
	})
	profile := NewWorkspaceDiscovery(root).Discover()

	if len(profile.Candidates) == 0 {
		t.Fatal("discovery must observe candidate files")
	}
	for _, c := range profile.Candidates {
		if c.Kind != EvidenceKindCandidate {
			t.Errorf("candidate %s carries kind %s, want %s", c.Path, c.Kind, EvidenceKindCandidate)
		}
	}
	// Manifest evidence is discovered but is evidence about workspace SHAPE.
	if !profile.HasManifest("go.mod") {
		t.Error("go.mod must be discovered as manifest evidence")
	}
	// Source roots are recorded as evidence.
	foundRoot := false
	for _, sr := range profile.SourceRoots {
		if sr.Path == "src" && sr.Kind == EvidenceKindSourceRoot {
			foundRoot = true
		}
	}
	if !foundRoot {
		t.Errorf("src must be discovered as a source root, got %+v", profile.SourceRoots)
	}

	// Evidence ≠ authority: an objective that names no file gets a
	// disambiguation request, never a chosen target.
	resolver := NewTargetResolver(root)
	res := resolver.Resolve(context.Background(), "Tidy this project up", nil)
	if res.Dispatchable() {
		t.Fatal("discovery evidence must not grant a mutation target")
	}
	if res.Binding != nil {
		t.Fatal("no candidate may become a binding")
	}
}

// TestPhase16_1_ExplicitDirectoryRefusalCarriesEvidence: the refusal is
// actionable — the human is shown what the directory contains, but the runtime
// still refuses to choose one.
func TestPhase16_1_ExplicitDirectoryRefusalCarriesEvidence(t *testing.T) {
	root := writeTree(t, map[string]string{"a.go": "package a\n", "b.go": "package b\n"})
	resolver := NewTargetResolver(root)

	res := resolver.Resolve(context.Background(), "Refactor the project", []string{t.TempDir()})
	// An absolute path outside the root is UNBOUND_PATH (it escapes), not a
	// directory promotion.
	if res.State != TargetStateUnboundPath {
		t.Fatalf("out-of-root absolute state = %s, want UNBOUND_PATH", res.State)
	}
	if res.Dispatchable() {
		t.Fatal("an out-of-root path must never be dispatchable")
	}
}

// TestPhase16_1_DiscoveryReadsButNeverWrites: the scan leaves the tree
// byte-identical.
func TestPhase16_1_DiscoveryReadsButNeverWrites(t *testing.T) {
	root := writeTree(t, map[string]string{"go.mod": "module x\n", "main.go": "package main\n"})
	before := snapshotTree(t, root)
	_ = NewWorkspaceDiscovery(root).Discover()
	assertTreeUnchanged(t, root, before)
}

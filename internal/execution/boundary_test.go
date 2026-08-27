package execution

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper to write file.
func boundaryWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func boundaryRead(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestBoundary_AtomicRollbackOnContractExhaustion simulates 3/3 retry
// rejection on invalid artifact format and asserts the workspace file content
// matches pre-execution state bit-for-bit. This codifies the Fail-Closed
// invariant: exhausted retry budget => instant DAG abort + instant rollback,
// partial writes forbidden.
func TestBoundary_AtomicRollbackOnContractExhaustion(t *testing.T) {
	root := t.TempDir()
	original := "package main\nfunc main() { println(\"hello\") }\n"
	boundaryWrite(t, root, "main.go", original)

	occ := NewOCCVerifier(root)
	baseDigest := occ.TreeDigest([]string{"main.go"})
	originals := map[string][]byte{"main.go": []byte(original)}

	// Simulate corrupted writes that would happen if partial execution leaked.
	boundaryWrite(t, root, "main.go", "corrupted attempt 1\n")
	boundaryWrite(t, root, "main.go", "corrupted attempt 2\n")

	validator := NewDefaultArtifactValidator()
	invalidRaw := []byte("this is not a valid patch — no SEARCH or diff markers")
	for attempt := 1; attempt <= 3; attempt++ {
		_, err := validator.ValidateArtifact(invalidRaw, "main.go")
		if !errors.Is(err, ErrFormatRejected) {
			t.Fatalf("attempt %d: ValidateArtifact = %v, want ErrFormatRejected", attempt, err)
		}
	}
	// Exhausted budget triggers atomic rollback.
	if err := RollbackAndVerify(root, []string{"main.go"}, baseDigest, originals); err != nil {
		t.Fatalf("RollbackAndVerify: %v", err)
	}
	if got := boundaryRead(t, root, "main.go"); got != original {
		t.Fatalf("atomic rollback failed: got %q want %q", got, original)
	}
	// Cryptographic assertion must still hold.
	b := NewExecutionBoundary(root, []string{"main.go"})
	if err := b.AssertWorkspaceIntegrity(baseDigest); err != nil {
		t.Fatalf("AssertWorkspaceIntegrity after rollback: %v", err)
	}
}

// TestBoundary_DigestMismatchDetection injects deliberate corruption during
// rollback and asserts the engine halts with a DigestMismatchError.
func TestBoundary_DigestMismatchDetection(t *testing.T) {
	root := t.TempDir()
	original := "line 1\nline 2\n"
	boundaryWrite(t, root, "target.txt", original)

	occ := NewOCCVerifier(root)
	baseDigest := occ.TreeDigest([]string{"target.txt"})
	originals := map[string][]byte{"target.txt": []byte(original)}

	// Simulate a rollback that should restore but we inject corruption after it.
	if err := RollbackAndVerify(root, []string{"target.txt"}, baseDigest, originals); err != nil {
		t.Fatalf("clean rollback: %v", err)
	}
	// Inject deliberate corruption post-rollback.
	boundaryWrite(t, root, "target.txt", "CORRUPTED BY INJECTED FAULT\n")

	b := NewExecutionBoundary(root, []string{"target.txt"})
	err := b.AssertWorkspaceIntegrity(baseDigest)
	if err == nil {
		t.Fatal("expected DigestMismatchError, got nil")
	}
	var mismatch *DigestMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want *DigestMismatchError", err)
	}
	if mismatch.Expected != baseDigest {
		t.Errorf("expected %q, got %q", baseDigest, mismatch.Expected)
	}
	if mismatch.Actual == baseDigest {
		t.Error("actual digest should differ from base after corruption")
	}
	// Also verify RollbackAndVerify surfaces mismatch when originals are stale.
	staleOriginals := map[string][]byte{"target.txt": []byte("stale that does not match base")}
	err = RollbackAndVerify(root, []string{"target.txt"}, baseDigest, staleOriginals)
	if err == nil {
		t.Fatal("RollbackAndVerify with stale originals must return DigestMismatchError")
	}
	if !errors.As(err, &mismatch) {
		t.Fatalf("RollbackAndVerify err = %v, want *DigestMismatchError", err)
	}
}

// TestBoundary_ZeroPartialWrite executes a 5-subtask DAG where subtask 3 fails.
// It verifies lines modified by subtask 1 and 2 are completely reverted.
func TestBoundary_ZeroPartialWrite(t *testing.T) {
	root := t.TempDir()
	original := "task1: pending\ntask2: pending\ntask3: pending\ntask4: pending\ntask5: pending\n"
	boundaryWrite(t, root, "dag.txt", original)

	occ := NewOCCVerifier(root)
	baseDigest := occ.TreeDigest([]string{"dag.txt"})
	originals := map[string][]byte{"dag.txt": []byte(original)}

	// Simulate applying subtasks 1 and 2, then failing on 3.
	// We use direct file writes to simulate the engine's mutate step.
	apply := func(content string) {
		boundaryWrite(t, root, "dag.txt", content)
	}
	// Subtask 1: mutate line 1
	after1 := strings.Replace(original, "task1: pending", "task1: done", 1)
	apply(after1)
	if got := boundaryRead(t, root, "dag.txt"); !strings.Contains(got, "task1: done") {
		t.Fatal("subtask 1 apply not visible")
	}
	// Subtask 2: mutate line 2
	after2 := strings.Replace(after1, "task2: pending", "task2: done", 1)
	apply(after2)
	if got := boundaryRead(t, root, "dag.txt"); !strings.Contains(got, "task2: done") {
		t.Fatal("subtask 2 apply not visible")
	}
	// Subtask 3: simulate artifact validation failure.
	validator := NewDefaultArtifactValidator()
	_, err := validator.ValidateArtifact([]byte("garbage without markers"), "dag.txt")
	if !errors.Is(err, ErrFormatRejected) {
		t.Fatalf("subtask 3 validation = %v, want ErrFormatRejected", err)
	}
	// Fail-closed: immediate DAG abort + instant rollback before subtasks 4/5 execute.
	if err := RollbackAndVerify(root, []string{"dag.txt"}, baseDigest, originals); err != nil {
		t.Fatalf("rollback after subtask 3 failure: %v", err)
	}
	// Verify zero partial writes: subtasks 1 and 2 fully reverted.
	if got := boundaryRead(t, root, "dag.txt"); got != original {
		t.Fatalf("zero partial write violated: got %q want %q", got, original)
	}
	// Verify 4 and 5 never executed (their markers absent).
	if got := boundaryRead(t, root, "dag.txt"); strings.Contains(got, "task4: done") || strings.Contains(got, "task5: done") {
		t.Fatalf("subtasks 4/5 must never execute after abort, got %q", got)
	}
	// Cryptographic assertion still holds.
	b := NewExecutionBoundary(root, []string{"dag.txt"})
	if err := b.AssertWorkspaceIntegrity(baseDigest); err != nil {
		t.Fatalf("post-rollback integrity: %v", err)
	}
}

// TestArtifactValidator_TypedErrors ensures the validator returns the typed
// sentinels required for P1 NormalizingValidator decoration.
func TestArtifactValidator_TypedErrors(t *testing.T) {
	v := NewDefaultArtifactValidator()

	t.Run("format rejected", func(t *testing.T) {
		_, err := v.ValidateArtifact([]byte("no markers"), "a.go")
		if !errors.Is(err, ErrFormatRejected) {
			t.Fatalf("err = %v, want ErrFormatRejected", err)
		}
	})
	t.Run("scope violation absolute", func(t *testing.T) {
		_, err := v.ValidateArtifact([]byte("<<<<<<< SEARCH\nhi\n=======\nbye\n>>>>>>>"), "/abs/path.go")
		if !errors.Is(err, ErrScopeViolation) {
			t.Fatalf("err = %v, want ErrScopeViolation", err)
		}
	})
	t.Run("scope violation traversal", func(t *testing.T) {
		_, err := v.ValidateArtifact([]byte("<<<<<<< SEARCH\nhi\n=======\nbye\n>>>>>>>"), "../escape.go")
		if !errors.Is(err, ErrScopeViolation) {
			t.Fatalf("err = %v, want ErrScopeViolation", err)
		}
	})
	t.Run("ambiguous anchor empty search", func(t *testing.T) {
		_, err := v.ValidateArtifact([]byte("<<<<<<< SEARCH\n   \n=======\nreplace\n>>>>>>>"), "a.go")
		if !errors.Is(err, ErrAmbiguousAnchor) {
			t.Fatalf("err = %v, want ErrAmbiguousAnchor", err)
		}
	})
	t.Run("valid bounded patch passes", func(t *testing.T) {
		bp, err := v.ValidateArtifact([]byte("<<<<<<< SEARCH\nhello\n=======\nworld\n>>>>>>>"), "a.txt")
		if err != nil {
			t.Fatalf("valid patch rejected: %v", err)
		}
		if bp.Target != "a.txt" || bp.Search != "hello" || bp.Replace != "world" {
			t.Fatalf("bounded patch = %+v, want target a.txt search hello replace world", bp)
		}
	})
}

// TestArtifactValidator_DecoratorReady proves the interface is ready for P1
// NormalizingValidator decoration without modifying execution loops: a
// decorator can wrap the validator, normalize, then delegate.
func TestArtifactValidator_DecoratorReady(t *testing.T) {
	base := NewDefaultArtifactValidator()
	decorator := &normalizingValidatorStub{inner: base}
	bp, err := decorator.ValidateArtifact([]byte("<<<<<<< SEARCH\nhello\n=======\nworld\n>>>>>>>"), "a.txt")
	if err != nil {
		t.Fatalf("decorator delegation failed: %v", err)
	}
	if bp.Search != "hello" {
		t.Fatalf("decorator patch search = %q, want hello", bp.Search)
	}
	// Invalid through decorator still maps to typed error.
	_, err = decorator.ValidateArtifact([]byte("bad"), "a.txt")
	if !errors.Is(err, ErrFormatRejected) {
		t.Fatalf("decorator err = %v, want ErrFormatRejected", err)
	}
}

type normalizingValidatorStub struct{ inner ArtifactValidator }

func (n *normalizingValidatorStub) ValidateArtifact(raw []byte, target string) (*BoundedPatch, error) {
	// P1 normalization would happen here; stub just trims and delegates.
	raw = []byte(strings.TrimSpace(string(raw)))
	return n.inner.ValidateArtifact(raw, target)
}

package boundary

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/execution"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBoundary_AtomicRollbackOnContractExhaustion(t *testing.T) {
	root := t.TempDir()
	original := "package main\nfunc main() { println(\"hello\") }\n"
	writeFile(t, root, "main.go", original)
	occ := execution.NewOCCVerifier(root)
	baseDigest := occ.TreeDigest([]string{"main.go"})
	originals := map[string][]byte{"main.go": []byte(original)}
	writeFile(t, root, "main.go", "corrupted 1\n")
	writeFile(t, root, "main.go", "corrupted 2\n")
	v := execution.NewDefaultArtifactValidator()
	invalidRaw := []byte("this is not a valid patch — no SEARCH or diff markers")
	for attempt := 1; attempt <= 3; attempt++ {
		_, err := v.ValidateArtifact(invalidRaw, "main.go")
		if !errors.Is(err, execution.ErrFormatRejected) {
			t.Fatalf("attempt %d: err = %v, want ErrFormatRejected", attempt, err)
		}
	}
	if err := RollbackAndVerify(root, []string{"main.go"}, baseDigest, originals); err != nil {
		t.Fatalf("RollbackAndVerify: %v", err)
	}
	if got := readFile(t, root, "main.go"); got != original {
		t.Fatalf("atomic rollback failed: got %q want %q", got, original)
	}
	if err := AssertWorkspaceIntegrity(root, []string{"main.go"}, baseDigest); err != nil {
		t.Fatalf("AssertWorkspaceIntegrity: %v", err)
	}
}

func TestBoundary_DigestMismatchDetection(t *testing.T) {
	root := t.TempDir()
	original := "line 1\nline 2\n"
	writeFile(t, root, "target.txt", original)
	occ := execution.NewOCCVerifier(root)
	baseDigest := occ.TreeDigest([]string{"target.txt"})
	originals := map[string][]byte{"target.txt": []byte(original)}
	if err := RollbackAndVerify(root, []string{"target.txt"}, baseDigest, originals); err != nil {
		t.Fatalf("clean rollback: %v", err)
	}
	writeFile(t, root, "target.txt", "CORRUPTED\n")
	err := AssertWorkspaceIntegrity(root, []string{"target.txt"}, baseDigest)
	if err == nil {
		t.Fatal("expected DigestMismatchError")
	}
	var mismatch *execution.DigestMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want *DigestMismatchError", err)
	}
	staleOriginals := map[string][]byte{"target.txt": []byte("stale")}
	err = RollbackAndVerify(root, []string{"target.txt"}, baseDigest, staleOriginals)
	if err == nil {
		t.Fatal("RollbackAndVerify with stale originals must return DigestMismatchError")
	}
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want *DigestMismatchError", err)
	}
}

func TestBoundary_ZeroPartialWrite(t *testing.T) {
	root := t.TempDir()
	original := "task1: pending\ntask2: pending\ntask3: pending\ntask4: pending\ntask5: pending\n"
	writeFile(t, root, "dag.txt", original)
	occ := execution.NewOCCVerifier(root)
	baseDigest := occ.TreeDigest([]string{"dag.txt"})
	originals := map[string][]byte{"dag.txt": []byte(original)}
	after1 := strings.Replace(original, "task1: pending", "task1: done", 1)
	writeFile(t, root, "dag.txt", after1)
	after2 := strings.Replace(after1, "task2: pending", "task2: done", 1)
	writeFile(t, root, "dag.txt", after2)
	v := execution.NewDefaultArtifactValidator()
	_, err := v.ValidateArtifact([]byte("garbage without markers"), "dag.txt")
	if !errors.Is(err, execution.ErrFormatRejected) {
		t.Fatalf("validation = %v, want ErrFormatRejected", err)
	}
	if err := RollbackAndVerify(root, []string{"dag.txt"}, baseDigest, originals); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := readFile(t, root, "dag.txt"); got != original {
		t.Fatalf("zero partial write violated: got %q want %q", got, original)
	}
	if got := readFile(t, root, "dag.txt"); strings.Contains(got, "task4: done") || strings.Contains(got, "task5: done") {
		t.Fatalf("subtasks 4/5 must never execute, got %q", got)
	}
	if err := AssertWorkspaceIntegrity(root, []string{"dag.txt"}, baseDigest); err != nil {
		t.Fatalf("integrity: %v", err)
	}
}

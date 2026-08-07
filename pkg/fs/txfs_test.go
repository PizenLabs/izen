package txfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// mustBegin fails the test when Begin errors.
func mustBegin(t *testing.T, tx *TxFS) {
	t.Helper()
	if err := tx.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
}

// readFile reads a file relative to root, failing the test on error.
func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// stat reports whether a path relative to root exists.
func stat(t *testing.T, root, rel string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(filepath.Join(root, rel))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		t.Fatalf("stat %s: %v", rel, err)
	}
	return info
}

func TestCommitWritesFileContentAndMode(t *testing.T) {
	root := t.TempDir()
	tx := NewTxFS(root)
	mustBegin(t, tx)

	if err := tx.WriteFile("hello.txt", []byte("hello world"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Nothing is visible before commit.
	if stat(t, root, "hello.txt") != nil {
		t.Fatal("file must not exist before Commit")
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := readFile(t, root, "hello.txt"); got != "hello world" {
		t.Fatalf("content = %q, want %q", got, "hello world")
	}
	if info := stat(t, root, "hello.txt"); info == nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want 0o640", info.Mode().Perm())
	}
	if tx.Active() {
		t.Error("transaction must be inactive after Commit")
	}
}

func TestCommitDefaultMode(t *testing.T) {
	root := t.TempDir()
	tx := NewTxFS(root)
	mustBegin(t, tx)
	if err := tx.WriteFile("default.txt", []byte("x"), 0); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if info := stat(t, root, "default.txt"); info == nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0o644", info.Mode().Perm())
	}
}

func TestCommitCreatesParentDirectories(t *testing.T) {
	root := t.TempDir()
	tx := NewTxFS(root)
	mustBegin(t, tx)

	if err := tx.WriteFile("src/components/button.tsx", []byte("export const B = 1;"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := readFile(t, root, "src/components/button.tsx"); got != "export const B = 1;" {
		t.Fatalf("content = %q", got)
	}
}

func TestCommitAppliesWritesAndRemoves(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	tx := NewTxFS(root)
	mustBegin(t, tx)

	if err := tx.WriteFile("new.txt", []byte("fresh"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := tx.RemoveFile("keep.txt"); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if got := readFile(t, root, "new.txt"); got != "fresh" {
		t.Fatalf("new.txt = %q, want %q", got, "fresh")
	}
	if stat(t, root, "keep.txt") != nil {
		t.Error("keep.txt must be removed")
	}
}

func TestOperationsRequireActiveTransaction(t *testing.T) {
	tx := NewTxFS(t.TempDir())
	if err := tx.WriteFile("a.txt", []byte("x"), 0o644); !errors.Is(err, ErrNoActiveTransaction) {
		t.Fatalf("WriteFile before Begin: expected ErrNoActiveTransaction, got %v", err)
	}
	if err := tx.RemoveFile("a.txt"); !errors.Is(err, ErrNoActiveTransaction) {
		t.Fatalf("RemoveFile before Begin: expected ErrNoActiveTransaction, got %v", err)
	}
	if err := tx.Commit(); !errors.Is(err, ErrNoActiveTransaction) {
		t.Fatalf("Commit before Begin: expected ErrNoActiveTransaction, got %v", err)
	}
	if err := tx.Rollback(); !errors.Is(err, ErrNoActiveTransaction) {
		t.Fatalf("Rollback before Begin: expected ErrNoActiveTransaction, got %v", err)
	}
}

func TestBeginRejectsDoubleTransaction(t *testing.T) {
	tx := NewTxFS(t.TempDir())
	mustBegin(t, tx)
	if err := tx.Begin(); !errors.Is(err, ErrActiveTransaction) {
		t.Fatalf("second Begin: expected ErrActiveTransaction, got %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	mustBegin(t, tx) // a fresh transaction is allowed after rollback
}

func TestRollbackOfNewFileLeavesNothingBehind(t *testing.T) {
	root := t.TempDir()
	tx := NewTxFS(root)
	mustBegin(t, tx)

	if err := tx.WriteFile("ghost.txt", []byte("partial"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if stat(t, root, "ghost.txt") != nil {
		t.Error("rolled-back new file must not exist")
	}
	if !tx.Active() && tx.StagedCount() != 0 {
		t.Fatal("rollback must clear all staged state")
	}
}

func TestRollbackRestoresOverwrittenFile(t *testing.T) {
	root := t.TempDir()
	origPath := filepath.Join(root, "data.txt")
	if err := os.WriteFile(origPath, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	tx := NewTxFS(root)
	mustBegin(t, tx)

	if err := tx.WriteFile("data.txt", []byte("REPLACED"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if got := readFile(t, root, "data.txt"); got != "ORIGINAL" {
		t.Fatalf("content = %q, want %q", got, "ORIGINAL")
	}
	if info := stat(t, root, "data.txt"); info == nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0o600 (original preserved)", info.Mode().Perm())
	}
}

func TestRollbackRestoresRemovedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	tx := NewTxFS(root)
	mustBegin(t, tx)

	if err := tx.RemoveFile("keep.txt"); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := readFile(t, root, "keep.txt"); got != "precious" {
		t.Fatalf("content = %q, want %q", got, "precious")
	}
}

func TestRollbackRestoresAfterPartialCommit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("ORIGINAL-A"), 0o644); err != nil {
		t.Fatal(err)
	}
	// z.txt sorts last in the commit order; make it a directory so the rename
	// in phase two fails after a.txt has already been applied.
	blocked := filepath.Join(root, "z.txt")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	tx := NewTxFS(root)
	mustBegin(t, tx)
	if err := tx.WriteFile("a.txt", []byte("NEW-A"), 0o644); err != nil {
		t.Fatalf("WriteFile a.txt: %v", err)
	}
	if err := tx.WriteFile("z.txt", []byte("NEW-Z"), 0o644); err != nil {
		t.Fatalf("WriteFile z.txt: %v", err)
	}

	if err := tx.Commit(); err == nil {
		t.Fatal("expected Commit to fail against the blocking directory")
	}
	// A partially applied commit: a.txt was renamed before z.txt failed.
	if got := readFile(t, root, "a.txt"); got != "NEW-A" {
		t.Fatalf("precondition broken: a.txt = %q, want %q", got, "NEW-A")
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	// The original file is restored byte-for-byte.
	if got := readFile(t, root, "a.txt"); got != "ORIGINAL-A" {
		t.Fatalf("a.txt = %q, want %q (workspace must be pristine)", got, "ORIGINAL-A")
	}
	// The blocking directory is preserved, never deleted by rollback.
	if info := stat(t, root, "z.txt"); info == nil || !info.IsDir() {
		t.Fatal("z.txt directory must be preserved by rollback")
	}
	// No temp scaffolding is left behind.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) > len(".txfs") && e.Name()[:len(".txfs")] == ".txfs" {
			t.Fatalf("temp file %q left behind", e.Name())
		}
	}
}

func TestRollbackPrunesCreatedDirectories(t *testing.T) {
	root := t.TempDir()
	tx := NewTxFS(root)
	mustBegin(t, tx)

	if err := tx.WriteFile("deep/nested/dir/file.txt", []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if stat(t, root, "deep") != nil {
		t.Error("created parent directories must be pruned on rollback")
	}
}

func TestPathEscapesRejected(t *testing.T) {
	root := t.TempDir()
	tx := NewTxFS(root)
	mustBegin(t, tx)

	for _, bad := range []string{"../evil.txt", "/etc/passwd", "a/../../evil.txt"} {
		if err := tx.WriteFile(bad, []byte("x"), 0o644); !errors.Is(err, ErrPathEscapesRoot) {
			t.Fatalf("WriteFile(%q): expected ErrPathEscapesRoot, got %v", bad, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "..", "evil.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("escaping write must never touch the filesystem")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit after rejected escapes: %v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("workspace must stay empty, entries = %v err = %v", entries, err)
	}
}

func TestStagedIntrospection(t *testing.T) {
	tx := NewTxFS(t.TempDir())
	mustBegin(t, tx)
	if err := tx.WriteFile("b.txt", []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tx.WriteFile("a.txt", []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tx.RemoveFile("c.txt"); err != nil {
		t.Fatal(err)
	}
	if got := tx.StagedCount(); got != 3 {
		t.Fatalf("StagedCount = %d, want 3", got)
	}
	if got := tx.StagedPaths(); !reflect.DeepEqual(got, []string{"a.txt", "b.txt", "c.txt"}) {
		t.Fatalf("StagedPaths = %v, want sorted paths", got)
	}
}

func TestReadFileReturnsStagedOrLiveContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "live.txt"), []byte("LIVE"), 0o644); err != nil {
		t.Fatal(err)
	}
	tx := NewTxFS(root)
	mustBegin(t, tx)

	if got, err := tx.ReadFile("live.txt"); err != nil || string(got) != "LIVE" {
		t.Fatalf("ReadFile(live) = %q, %v", got, err)
	}
	if err := tx.WriteFile("live.txt", []byte("STAGED"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := tx.ReadFile("live.txt"); err != nil || string(got) != "STAGED" {
		t.Fatalf("ReadFile(staged) = %q, %v", got, err)
	}
}

func TestCommitEmptyTransactionIsNoOp(t *testing.T) {
	tx := NewTxFS(t.TempDir())
	mustBegin(t, tx)
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit of empty transaction: %v", err)
	}
	if tx.Active() {
		t.Error("transaction must close after empty commit")
	}
}

func TestLastWriteWinsInSameTransaction(t *testing.T) {
	root := t.TempDir()
	tx := NewTxFS(root)
	mustBegin(t, tx)
	if err := tx.WriteFile("f.txt", []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tx.WriteFile("f.txt", []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := readFile(t, root, "f.txt"); got != "second" {
		t.Fatalf("content = %q, want %q", got, "second")
	}
}

func TestConcurrentWritesCommitAtomically(t *testing.T) {
	root := t.TempDir()
	tx := NewTxFS(root)
	mustBegin(t, tx)

	const workers = 8
	const perWorker = 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				rel := filepath.Join("batch", "worker", string(rune('a'+w)), "file.txt")
				if err := tx.WriteFile(rel, []byte(rel), 0o644); err != nil {
					t.Errorf("WriteFile(%s): %v", rel, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := tx.StagedCount(); got != 0 {
		t.Fatalf("StagedCount after commit = %d, want 0", got)
	}
	for w := 0; w < workers; w++ {
		rel := filepath.Join("batch", "worker", string(rune('a'+w)), "file.txt")
		if got := readFile(t, root, rel); got != rel {
			t.Fatalf("%s = %q, want %q", rel, got, rel)
		}
	}
}

func TestCommitCreatesWorkspaceRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "fresh", "workspace")
	tx := NewTxFS(root)
	mustBegin(t, tx)

	if err := tx.WriteFile("deep/file.txt", []byte("fresh root"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := readFile(t, root, "deep/file.txt"); got != "fresh root" {
		t.Fatalf("content = %q", got)
	}
}

func TestRollbackPrunesDirectoriesAfterPartialCommit(t *testing.T) {
	root := t.TempDir()
	// z.txt sorts last in the commit order; a directory there forces the
	// phase-two rename to fail after deep/nested/file.txt was already applied.
	blocked := filepath.Join(root, "z.txt")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	tx := NewTxFS(root)
	mustBegin(t, tx)
	if err := tx.WriteFile("deep/nested/file.txt", []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := tx.WriteFile("z.txt", []byte("z"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("expected Commit to fail against the blocking directory")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// The file written by the partial commit is gone and every directory the
	// commit created is pruned.
	if info := stat(t, root, "deep"); info != nil {
		t.Fatal("created parent directories must be pruned on rollback")
	}
	if info := stat(t, root, "z.txt"); info == nil || !info.IsDir() {
		t.Fatal("blocking directory must be preserved")
	}
}

func TestTransactionReusableAcrossRuns(t *testing.T) {
	root := t.TempDir()
	tx := NewTxFS(root)

	mustBegin(t, tx)
	if err := tx.WriteFile("run1.txt", []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	mustBegin(t, tx)
	if err := tx.WriteFile("run2.txt", []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if got := readFile(t, root, "run1.txt"); got != "one" {
		t.Fatalf("run1.txt = %q", got)
	}
	if got := readFile(t, root, "run2.txt"); got != "two" {
		t.Fatalf("run2.txt = %q", got)
	}
}

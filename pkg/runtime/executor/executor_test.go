package executor

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/projection/diff"
	"github.com/PizenLabs/izen/pkg/runtime/target"
)

// tmpRef builds a TargetRef pointing at an absolute path inside a temp dir.
func tmpRef(workdir, name string) *target.TargetRef {
	path := filepath.Join(workdir, name)
	return &target.TargetRef{Raw: name, Canonical: path, Exists: false, Tracked: false, Source: target.ResolutionRaw}
}

// sampleDiff appends line4 to a base of line1..line3.
const sampleDiff = `--- a/file.go
+++ b/file.go
@@ -1,3 +1,4 @@
 line1
 line2
 line3
+line4
`

// writeFile writes content into dir and fails the test on error.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// readFile reads content from path and fails the test on error.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// assertNoTempOrphans fails the test if any .tmp.izen.* file survives in dir.
func assertNoTempOrphans(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp.izen.") {
			t.Errorf("orphaned temp file %q in %s", e.Name(), dir)
		}
	}
}

func TestProposalValidation(t *testing.T) {
	t.Parallel()

	v := NewValidator()

	t.Run("valid explicit patch lines on new file", func(t *testing.T) {
		t.Parallel()
		tr := tmpRef(t.TempDir(), "new.txt")
		proposal := ProposedMutation{
			ProposalID: "p1",
			TargetRef:  tr,
			PatchLines: []diff.PatchLine{
				{Type: diff.MutationAdd, Content: "package main"},
				{Type: diff.MutationAdd, Content: "func main() {}"},
			},
		}
		res, err := v.Validate(proposal)
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if !res.Valid {
			t.Fatalf("expected valid result, got reason %q", res.ErrorReason)
		}
		if res.Evidence.Added != 2 || res.Evidence.Deleted != 0 {
			t.Errorf("evidence counts = +%d -%d, want +2 -0", res.Evidence.Added, res.Evidence.Deleted)
		}
		if len(res.Evidence.Lines) != 2 {
			t.Errorf("evidence lines = %d, want 2", len(res.Evidence.Lines))
		}
		if res.Evidence.TargetFile != tr.Canonical {
			t.Errorf("evidence target = %q, want %q", res.Evidence.TargetFile, tr.Canonical)
		}
		if res.RequiresRollback {
			t.Error("validation must not require rollback (side-effect-free)")
		}
	})

	t.Run("valid unified diff on existing file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeFile(t, dir, "file.go", "line1\nline2\nline3\n")
		proposal := ProposedMutation{
			ProposalID: "p2",
			TargetRef:  &target.TargetRef{Raw: "file.go", Canonical: path, Exists: true, Source: target.ResolutionVCS},
			RawPatch:   sampleDiff,
		}
		res, err := v.Validate(proposal)
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if !res.Valid {
			t.Fatalf("expected valid result, got reason %q", res.ErrorReason)
		}
		if res.Evidence.Added != 1 || res.Evidence.Deleted != 0 {
			t.Errorf("evidence counts = +%d -%d, want +1 -0", res.Evidence.Added, res.Evidence.Deleted)
		}
		if len(res.Evidence.Lines) != 1 || res.Evidence.Lines[0].Type != diff.MutationAdd {
			t.Errorf("evidence lines = %+v, want a single add line", res.Evidence.Lines)
		}
		if res.Evidence.Lines[0].Content != "line4" {
			t.Errorf("evidence line content = %q, want %q", res.Evidence.Lines[0].Content, "line4")
		}
	})

	t.Run("valid whole-file rewrite on new file", func(t *testing.T) {
		t.Parallel()
		tr := tmpRef(t.TempDir(), "new.txt")
		proposal := ProposedMutation{
			TargetRef: tr,
			RawPatch:  "package main\nfunc main() {}\n",
		}
		res, err := v.Validate(proposal)
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if !res.Valid {
			t.Fatalf("expected valid result, got reason %q", res.ErrorReason)
		}
		if res.Evidence.Added != 2 || res.Evidence.Deleted != 0 {
			t.Errorf("evidence counts = +%d -%d, want +2 -0", res.Evidence.Added, res.Evidence.Deleted)
		}
	})

	t.Run("modify and delete evidence counts", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeFile(t, dir, "m.txt", "keep\nold\n")
		proposal := ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "m.txt", Canonical: path, Exists: true},
			PatchLines: []diff.PatchLine{
				{Type: diff.MutationModify, Content: "keep"},
				{Type: diff.MutationModify, Content: "new"},
				{Type: diff.MutationDelete, Content: "gone"},
			},
		}
		res, err := v.Validate(proposal)
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if !res.Valid {
			t.Fatalf("expected valid result, got reason %q", res.ErrorReason)
		}
		if res.Evidence.Added != 1 || res.Evidence.Deleted != 1 {
			t.Errorf("evidence counts = +%d -%d, want +1 -1", res.Evidence.Added, res.Evidence.Deleted)
		}
	})

	t.Run("nil target reference rejected", func(t *testing.T) {
		t.Parallel()
		res, err := v.Validate(ProposedMutation{ProposalID: "p", RawPatch: "x"})
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result for nil target reference")
		}
		if res.ErrorReason == "" {
			t.Error("expected descriptive error reason")
		}
	})

	t.Run("empty canonical path rejected", func(t *testing.T) {
		t.Parallel()
		res, err := v.Validate(ProposedMutation{
			TargetRef: &target.TargetRef{},
			RawPatch:  "x",
		})
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result for empty canonical path")
		}
	})

	t.Run("path traversal rejected", func(t *testing.T) {
		t.Parallel()
		res, err := v.Validate(ProposedMutation{
			TargetRef: &target.TargetRef{Canonical: "../../etc/passwd"},
			RawPatch:  "root:x",
		})
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result for path traversal")
		}
		if !strings.Contains(res.ErrorReason, "escapes") {
			t.Errorf("reason = %q, want traversal description", res.ErrorReason)
		}
	})

	t.Run("empty payload rejected", func(t *testing.T) {
		t.Parallel()
		tr := tmpRef(t.TempDir(), "x.txt")
		res, err := v.Validate(ProposedMutation{TargetRef: tr})
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result for empty payload")
		}
	})

	t.Run("whitespace-only raw patch rejected", func(t *testing.T) {
		t.Parallel()
		tr := tmpRef(t.TempDir(), "x.txt")
		res, err := v.Validate(ProposedMutation{TargetRef: tr, RawPatch: "  \n"})
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result for whitespace-only raw patch")
		}
	})

	t.Run("unknown mutation type rejected", func(t *testing.T) {
		t.Parallel()
		tr := tmpRef(t.TempDir(), "x.txt")
		res, err := v.Validate(ProposedMutation{
			TargetRef: tr,
			PatchLines: []diff.PatchLine{
				{Type: diff.MutationType(99), Content: "x"},
			},
		})
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result for unknown mutation type")
		}
	})

	t.Run("patch header traversal rejected", func(t *testing.T) {
		t.Parallel()
		tr := tmpRef(t.TempDir(), "file.go")
		bad := `--- a/file.go
+++ b/../../escape.go
@@ -1,3 +1,4 @@
 line1
+line2
`
		res, err := v.Validate(ProposedMutation{TargetRef: tr, RawPatch: bad})
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result for patch header traversal")
		}
	})

	t.Run("absolute patch header rejected", func(t *testing.T) {
		t.Parallel()
		tr := tmpRef(t.TempDir(), "file.go")
		bad := `--- /etc/hosts
+++ /etc/hosts
@@ -1 +1 @@
-old
+new
`
		res, err := v.Validate(ProposedMutation{TargetRef: tr, RawPatch: bad})
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result for absolute patch header")
		}
	})

	t.Run("unified diff mismatching content rejected", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeFile(t, dir, "file.go", "alpha\nbeta\n")
		res, err := v.Validate(ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "file.go", Canonical: path, Exists: true},
			RawPatch:  sampleDiff,
		})
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result for mismatched unified diff")
		}
	})

	t.Run("nil validator receiver", func(t *testing.T) {
		t.Parallel()
		var nilValidator *ProposalValidator
		if _, err := nilValidator.Validate(ProposedMutation{}); err == nil {
			t.Fatal("expected error for nil validator")
		}
	})
}

func TestPrepareSnapshot(t *testing.T) {
	t.Parallel()

	e := NewExecutor()
	dir := t.TempDir()

	t.Run("existing file captures content and mode", func(t *testing.T) {
		t.Parallel()
		path := writeFile(t, dir, "a.txt", "original\n")
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		backup, err := e.PrepareSnapshot(path)
		if err != nil {
			t.Fatalf("PrepareSnapshot returned error: %v", err)
		}
		if !backup.Exists {
			t.Fatal("expected Exists = true for existing file")
		}
		if string(backup.Content) != "original\n" {
			t.Errorf("content = %q, want %q", string(backup.Content), "original\n")
		}
		if backup.FileMode != uint32(0o640) {
			t.Errorf("FileMode = %o, want 640", backup.FileMode)
		}
		if backup.Path != path {
			t.Errorf("Path = %q, want %q", backup.Path, path)
		}
	})

	t.Run("missing file yields non-existent snapshot", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "missing.txt")
		backup, err := e.PrepareSnapshot(path)
		if err != nil {
			t.Fatalf("PrepareSnapshot returned error: %v", err)
		}
		if backup.Exists {
			t.Fatal("expected Exists = false for missing file")
		}
		if len(backup.Content) != 0 {
			t.Errorf("content = %q, want empty", backup.Content)
		}
	})

	t.Run("nil executor receiver", func(t *testing.T) {
		t.Parallel()
		var nilExec *RuntimeExecutor
		if _, err := nilExec.PrepareSnapshot("x.txt"); err == nil {
			t.Fatal("expected error for nil executor")
		}
	})

	t.Run("snapshot of directory path fails", func(t *testing.T) {
		t.Parallel()
		blocker := filepath.Join(t.TempDir(), "d")
		if err := os.Mkdir(blocker, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if _, err := e.PrepareSnapshot(blocker); err == nil {
			t.Fatal("expected error snapshotting a directory")
		}
	})
}

func TestCommitAtomicOverwriteAndCreate(t *testing.T) {
	t.Parallel()

	e := NewExecutor()

	t.Run("atomic overwrite of existing file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeFile(t, dir, "f.txt", "old\n")
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		backup, err := e.PrepareSnapshot(path)
		if err != nil {
			t.Fatalf("PrepareSnapshot: %v", err)
		}
		proposal := ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "f.txt", Canonical: path, Exists: true},
			RawPatch:  "new content\n",
		}
		if err := e.Commit(proposal, backup); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if got := readFile(t, path); got != "new content\n" {
			t.Errorf("content = %q, want %q", got, "new content\n")
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Errorf("mode = %o, want 640", info.Mode().Perm())
		}
		assertNoTempOrphans(t, dir)
	})

	t.Run("creation of new file via whole-file patch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "new.txt")
		backup, err := e.PrepareSnapshot(path)
		if err != nil {
			t.Fatalf("PrepareSnapshot: %v", err)
		}
		if backup.Exists {
			t.Fatal("expected non-existent snapshot")
		}
		proposal := ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "new.txt", Canonical: path, Exists: false},
			RawPatch:  "line1\nline2\n",
		}
		if err := e.Commit(proposal, backup); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if got := readFile(t, path); got != "line1\nline2\n" {
			t.Errorf("content = %q, want %q", got, "line1\nline2\n")
		}
		assertNoTempOrphans(t, dir)
	})

	t.Run("creation via typed patch lines", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "lines.txt")
		backup, err := e.PrepareSnapshot(path)
		if err != nil {
			t.Fatalf("PrepareSnapshot: %v", err)
		}
		proposal := ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "lines.txt", Canonical: path, Exists: false},
			PatchLines: []diff.PatchLine{
				{Type: diff.MutationAdd, Content: "a"},
				{Type: diff.MutationDelete, Content: "never-present"},
				{Type: diff.MutationAdd, Content: "b"},
			},
		}
		if err := e.Commit(proposal, backup); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if got := readFile(t, path); got != "a\nb\n" {
			t.Errorf("content = %q, want %q", got, "a\nb\n")
		}
		assertNoTempOrphans(t, dir)
	})

	t.Run("creation of nested file creates parent directories", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "nested", "deep", "f.txt")
		backup, err := e.PrepareSnapshot(path)
		if err != nil {
			t.Fatalf("PrepareSnapshot: %v", err)
		}
		proposal := ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "nested/deep/f.txt", Canonical: path, Exists: false},
			RawPatch:  "deep\n",
		}
		if err := e.Commit(proposal, backup); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if got := readFile(t, path); got != "deep\n" {
			t.Errorf("content = %q, want %q", got, "deep\n")
		}
	})
}

func TestCommitUnifiedDiffApply(t *testing.T) {
	t.Parallel()

	e := NewExecutor()
	dir := t.TempDir()
	path := writeFile(t, dir, "file.go", "line1\nline2\nline3\n")
	backup, err := e.PrepareSnapshot(path)
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	proposal := ProposedMutation{
		TargetRef: &target.TargetRef{Raw: "file.go", Canonical: path, Exists: true},
		RawPatch:  sampleDiff,
	}
	if err := e.Commit(proposal, backup); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := readFile(t, path); got != "line1\nline2\nline3\nline4\n" {
		t.Errorf("content = %q, want %q", got, "line1\nline2\nline3\nline4\n")
	}
	assertNoTempOrphans(t, dir)
}

func TestRollbackExistingFile(t *testing.T) {
	t.Parallel()

	e := NewExecutor()
	dir := t.TempDir()
	path := writeFile(t, dir, "f.txt", "original\n")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	backup, err := e.PrepareSnapshot(path)
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	proposal := ProposedMutation{
		TargetRef: &target.TargetRef{Raw: "f.txt", Canonical: path, Exists: true},
		RawPatch:  "mutated\n",
	}
	if err := e.Commit(proposal, backup); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := readFile(t, path); got != "mutated\n" {
		t.Fatalf("pre-rollback content = %q, want %q", got, "mutated\n")
	}

	if err := e.Rollback(backup); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := readFile(t, path); got != "original\n" {
		t.Errorf("rollback content = %q, want %q", got, "original\n")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after rollback: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("rollback mode = %o, want 640", info.Mode().Perm())
	}
}

func TestRollbackNewFile(t *testing.T) {
	t.Parallel()

	e := NewExecutor()
	dir := t.TempDir()
	path := filepath.Join(dir, "created.txt")

	backup, err := e.PrepareSnapshot(path)
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	proposal := ProposedMutation{
		TargetRef: &target.TargetRef{Raw: "created.txt", Canonical: path, Exists: false},
		RawPatch:  "created content\n",
	}
	if err := e.Commit(proposal, backup); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should exist after commit: %v", err)
	}

	if err := e.Rollback(backup); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("file should be removed after rollback, stat err = %v", err)
	}
}

func TestCommitAutoRollback(t *testing.T) {
	t.Parallel()

	t.Run("materialization failure restores original content", func(t *testing.T) {
		t.Parallel()
		e := NewExecutor()
		dir := t.TempDir()
		path := writeFile(t, dir, "f.txt", "original\n")

		backup, err := e.PrepareSnapshot(path)
		if err != nil {
			t.Fatalf("PrepareSnapshot: %v", err)
		}
		proposal := ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "f.txt", Canonical: path, Exists: true},
			RawPatch:  sampleDiff, // hunks expect line1..3, file has only "original"
		}
		if err := e.Commit(proposal, backup); err == nil {
			t.Fatal("expected Commit error for mismatched unified diff")
		}
		if got := readFile(t, path); got != "original\n" {
			t.Errorf("content = %q, want original restored", got)
		}
		assertNoTempOrphans(t, dir)
	})

	t.Run("rename failure rolls back created artifact", func(t *testing.T) {
		t.Parallel()
		e := NewExecutor()
		dir := t.TempDir()
		// Inject a rename failure by making the target an existing empty
		// directory; Commit's rename onto a directory fails.
		blocker := filepath.Join(dir, "occupied.txt")
		if err := os.Mkdir(blocker, 0o755); err != nil {
			t.Fatalf("mkdir blocker: %v", err)
		}

		backup := &FileBackup{Path: blocker, Exists: false}
		proposal := ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "occupied.txt", Canonical: blocker, Exists: false},
			RawPatch:  "new content\n",
		}
		if err := e.Commit(proposal, backup); err == nil {
			t.Fatal("expected Commit error for rename onto directory")
		}
		if _, err := os.Stat(blocker); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("rollback should have removed the blocker, stat err = %v", err)
		}
		assertNoTempOrphans(t, dir)
	})

	t.Run("nil backup rejected before any write", func(t *testing.T) {
		t.Parallel()
		e := NewExecutor()
		tr := tmpRef(t.TempDir(), "x.txt")
		if err := e.Commit(ProposedMutation{TargetRef: tr, RawPatch: "x"}, nil); err == nil {
			t.Fatal("expected error for nil backup")
		}
	})

	t.Run("nil target reference rejected", func(t *testing.T) {
		t.Parallel()
		e := NewExecutor()
		backup := &FileBackup{Path: "x.txt", Exists: false}
		if err := e.Commit(ProposedMutation{RawPatch: "x"}, backup); err == nil {
			t.Fatal("expected error for nil target reference")
		}
	})
}

func TestRollbackValidation(t *testing.T) {
	t.Parallel()

	e := NewExecutor()

	t.Run("nil backup rejected", func(t *testing.T) {
		t.Parallel()
		if err := e.Rollback(nil); err == nil {
			t.Fatal("expected error for nil backup")
		}
	})

	t.Run("empty path rejected", func(t *testing.T) {
		t.Parallel()
		if err := e.Rollback(&FileBackup{}); err == nil {
			t.Fatal("expected error for empty path")
		}
	})

	t.Run("removing already-absent file is idempotent", func(t *testing.T) {
		t.Parallel()
		backup := &FileBackup{Path: filepath.Join(t.TempDir(), "never.txt"), Exists: false}
		if err := e.Rollback(backup); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
	})

	t.Run("restoring pristine snapshot is idempotent", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeFile(t, dir, "a.txt", "original\n")
		backup, err := e.PrepareSnapshot(path)
		if err != nil {
			t.Fatalf("PrepareSnapshot: %v", err)
		}
		if err := e.Rollback(backup); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
		if got := readFile(t, path); got != "original\n" {
			t.Errorf("content = %q, want %q", got, "original\n")
		}
	})

	t.Run("nil executor receiver", func(t *testing.T) {
		t.Parallel()
		var nilExec *RuntimeExecutor
		if err := nilExec.Rollback(&FileBackup{}); err == nil {
			t.Fatal("expected error for nil executor")
		}
	})
}

// newFileDiff creates a new file with two lines using the standard
// git new-file unified-diff shape (--- /dev/null header, zero old range).
const newFileDiff = `--- /dev/null
+++ b/fresh.txt
@@ -0,0 +1,2 @@
+line1
+line2
`

// multiHunkDiff replaces the first and third lines of a three-line file.
const multiHunkDiff = `--- a/m.txt
+++ b/m.txt
@@ -1,1 +1,1 @@
-a
+b
@@ -3,1 +3,1 @@
-c
+d
`

func TestNewFileDiffValidationAndCommit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.txt")
	tr := &target.TargetRef{Raw: "fresh.txt", Canonical: path, Exists: false, Source: target.ResolutionRaw}

	proposal := ProposedMutation{TargetRef: tr, RawPatch: newFileDiff}

	v := NewValidator()
	res, err := v.Validate(proposal)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid new-file diff, got %q", res.ErrorReason)
	}
	if res.Evidence.Added != 2 || res.Evidence.Deleted != 0 {
		t.Errorf("evidence = +%d -%d, want +2 -0", res.Evidence.Added, res.Evidence.Deleted)
	}

	e := NewExecutor()
	backup, err := e.PrepareSnapshot(path)
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	if err := e.Commit(proposal, backup); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := readFile(t, path); got != "line1\nline2\n" {
		t.Errorf("content = %q, want %q", got, "line1\nline2\n")
	}
	assertNoTempOrphans(t, dir)
}

func TestMultiHunkDiffCommit(t *testing.T) {
	t.Parallel()

	e := NewExecutor()
	dir := t.TempDir()
	path := writeFile(t, dir, "m.txt", "a\nx\nc\n")

	proposal := ProposedMutation{
		TargetRef: &target.TargetRef{Raw: "m.txt", Canonical: path, Exists: true},
		RawPatch:  multiHunkDiff,
	}

	v := NewValidator()
	res, err := v.Validate(proposal)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid multi-hunk diff, got %q", res.ErrorReason)
	}
	if res.Evidence.Added != 2 || res.Evidence.Deleted != 2 {
		t.Errorf("evidence = +%d -%d, want +2 -2", res.Evidence.Added, res.Evidence.Deleted)
	}

	backup, err := e.PrepareSnapshot(path)
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	if err := e.Commit(proposal, backup); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := readFile(t, path); got != "b\nx\nd\n" {
		t.Errorf("content = %q, want %q", got, "b\nx\nd\n")
	}
}

func TestCommitFailureInjection(t *testing.T) {
	t.Parallel()

	t.Run("nil executor receiver", func(t *testing.T) {
		t.Parallel()
		var nilExec *RuntimeExecutor
		backup := &FileBackup{Path: "x.txt", Exists: false}
		if err := nilExec.Commit(ProposedMutation{TargetRef: &target.TargetRef{Canonical: "x.txt"}, RawPatch: "x"}, backup); err == nil {
			t.Fatal("expected error for nil executor")
		}
	})

	t.Run("empty target path rejected", func(t *testing.T) {
		t.Parallel()
		e := NewExecutor()
		backup := &FileBackup{Exists: false}
		proposal := ProposedMutation{
			TargetRef: &target.TargetRef{},
			RawPatch:  "x",
		}
		if err := e.Commit(proposal, backup); err == nil {
			t.Fatal("expected error for empty target path")
		}
	})

	t.Run("empty payload rolls back", func(t *testing.T) {
		t.Parallel()
		e := NewExecutor()
		dir := t.TempDir()
		path := writeFile(t, dir, "f.txt", "original\n")
		backup, err := e.PrepareSnapshot(path)
		if err != nil {
			t.Fatalf("PrepareSnapshot: %v", err)
		}
		proposal := ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "f.txt", Canonical: path, Exists: true},
		}
		if err := e.Commit(proposal, backup); err == nil {
			t.Fatal("expected error for empty mutation payload")
		}
		if got := readFile(t, path); got != "original\n" {
			t.Errorf("content = %q, want original", got)
		}
		assertNoTempOrphans(t, dir)
	})

	t.Run("directory creation failure rolls back and reports rollback error", func(t *testing.T) {
		t.Parallel()
		e := NewExecutor()
		dir := t.TempDir()
		// "x" is a regular file, so MkdirAll(dir/x) fails and the subsequent
		// Rollback of dir/x/y.txt fails too (parent is not a directory).
		if err := os.WriteFile(filepath.Join(dir, "x"), []byte("file"), 0o644); err != nil {
			t.Fatalf("write x: %v", err)
		}
		targetPath := filepath.Join(dir, "x", "y.txt")
		backup := &FileBackup{Path: targetPath, Exists: false}
		proposal := ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "x/y.txt", Canonical: targetPath, Exists: false},
			RawPatch:  "content\n",
		}
		err := e.Commit(proposal, backup)
		if err == nil {
			t.Fatal("expected Commit error for directory creation failure")
		}
		if !strings.Contains(err.Error(), "rollback failed") {
			t.Errorf("expected combined rollback failure, got %v", err)
		}
	})

	t.Run("malformed hunk diff rolls back via materialization failure", func(t *testing.T) {
		t.Parallel()
		e := NewExecutor()
		dir := t.TempDir()
		path := writeFile(t, dir, "f.go", "line1\n")
		backup, err := e.PrepareSnapshot(path)
		if err != nil {
			t.Fatalf("PrepareSnapshot: %v", err)
		}
		raw := "--- a/f.go\n+++ b/f.go\n@@ bogus @@\n+line\n"
		proposal := ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "f.go", Canonical: path, Exists: true},
			RawPatch:  raw,
		}
		if err := e.Commit(proposal, backup); err == nil {
			t.Fatal("expected Commit error for malformed hunk diff")
		}
		if got := readFile(t, path); got != "line1\n" {
			t.Errorf("content = %q, want original", got)
		}
		assertNoTempOrphans(t, dir)
	})

	t.Run("backup path empty falls back to target canonical", func(t *testing.T) {
		t.Parallel()
		e := NewExecutor()
		dir := t.TempDir()
		path := filepath.Join(dir, "fallback.txt")
		backup := &FileBackup{Exists: false}
		proposal := ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "fallback.txt", Canonical: path, Exists: false},
			RawPatch:  "via canonical\n",
		}
		if err := e.Commit(proposal, backup); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if got := readFile(t, path); got != "via canonical\n" {
			t.Errorf("content = %q, want %q", got, "via canonical\n")
		}
	})
}

func TestRollbackFailureInjection(t *testing.T) {
	t.Parallel()

	e := NewExecutor()

	t.Run("restoring over a directory fails", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		blocker := filepath.Join(dir, "d")
		if err := os.Mkdir(blocker, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		backup := &FileBackup{Path: blocker, Exists: true, Content: []byte("x"), FileMode: 0o644}
		if err := e.Rollback(backup); err == nil {
			t.Fatal("expected Rollback error when target is a directory")
		}
	})

	t.Run("removing a non-empty directory fails", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		blocker := filepath.Join(dir, "d")
		if err := os.Mkdir(blocker, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(blocker, "child"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write child: %v", err)
		}
		backup := &FileBackup{Path: blocker, Exists: false}
		if err := e.Rollback(backup); err == nil {
			t.Fatal("expected Rollback error for non-empty directory")
		}
	})

	t.Run("zero file mode falls back to default on restore", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeFile(t, dir, "z.txt", "original\n")
		backup := &FileBackup{Path: path, Exists: true, Content: []byte("original\n"), FileMode: 0}
		if err := e.Rollback(backup); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
		if got := readFile(t, path); got != "original\n" {
			t.Errorf("content = %q, want %q", got, "original\n")
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("mode = %o, want 644", info.Mode().Perm())
		}
	})
}

func TestValidatorEdgeCases(t *testing.T) {
	t.Parallel()

	v := NewValidator()

	t.Run("directory target read failure is invalid", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		blocker := filepath.Join(dir, "d")
		if err := os.Mkdir(blocker, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		res, err := v.Validate(ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "d", Canonical: blocker, Exists: true},
			RawPatch:  "x\n",
		})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result when target cannot be read")
		}
		if !strings.Contains(res.ErrorReason, "cannot read target") {
			t.Errorf("reason = %q, want read failure description", res.ErrorReason)
		}
	})

	t.Run("bare dotdot target is traversal", func(t *testing.T) {
		t.Parallel()
		res, err := v.Validate(ProposedMutation{
			TargetRef: &target.TargetRef{Canonical: ".."},
			RawPatch:  "x",
		})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result for .. target")
		}
	})

	t.Run("dotdot prefix target is traversal", func(t *testing.T) {
		t.Parallel()
		res, err := v.Validate(ProposedMutation{
			TargetRef: &target.TargetRef{Canonical: "../evil.txt"},
			RawPatch:  "x",
		})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result for traversal target")
		}
	})

	t.Run("line diff default branch emits interleaved add", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeFile(t, dir, "d.txt", "b\na\n")
		res, err := v.Validate(ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "d.txt", Canonical: path, Exists: true},
			PatchLines: []diff.PatchLine{
				{Type: diff.MutationModify, Content: "c"},
				{Type: diff.MutationModify, Content: "b"},
			},
		})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if !res.Valid {
			t.Fatalf("expected valid result, got %q", res.ErrorReason)
		}
		if res.Evidence.Added != 1 || res.Evidence.Deleted != 1 {
			t.Errorf("evidence = +%d -%d, want +1 -1", res.Evidence.Added, res.Evidence.Deleted)
		}
	})

	t.Run("large diff falls back to delete-all add-all", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		oldLines := make([]string, 0, 1002)
		newLines := make([]string, 0, 1002)
		for i := 0; i < 1002; i++ {
			oldLines = append(oldLines, "old-"+strconv.Itoa(i))
			newLines = append(newLines, "new-"+strconv.Itoa(i))
		}
		path := writeFile(t, dir, "big.txt", strings.Join(oldLines, "\n")+"\n")
		res, err := v.Validate(ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "big.txt", Canonical: path, Exists: true},
			RawPatch:  strings.Join(newLines, "\n") + "\n",
		})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if !res.Valid {
			t.Fatalf("expected valid result, got %q", res.ErrorReason)
		}
		if res.Evidence.Added != 1002 || res.Evidence.Deleted != 1002 {
			t.Errorf("evidence = +%d -%d, want +1002 -1002", res.Evidence.Added, res.Evidence.Deleted)
		}
	})

	t.Run("malformed hunk header rejected as unparseable", func(t *testing.T) {
		t.Parallel()
		tr := tmpRef(t.TempDir(), "f.go")
		raw := "--- a/f.go\n+++ b/f.go\n@@ bogus @@\n+line\n"
		res, err := v.Validate(ProposedMutation{TargetRef: tr, RawPatch: raw})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if res.Valid {
			t.Fatal("expected invalid result for malformed hunk header")
		}
		if !strings.Contains(res.ErrorReason, "no parseable hunks") {
			t.Errorf("reason = %q, want unparseable hunks", res.ErrorReason)
		}
	})

	t.Run("backslash marker line is tolerated", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeFile(t, dir, "nl.txt", "line1\n")
		raw := `--- a/nl.txt
+++ b/nl.txt
@@ -1 +1,2 @@
 line1
+line2
\ No newline at end of file
`
		res, err := v.Validate(ProposedMutation{
			TargetRef: &target.TargetRef{Raw: "nl.txt", Canonical: path, Exists: true},
			RawPatch:  raw,
		})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if !res.Valid {
			t.Fatalf("expected valid diff with backslash marker, got %q", res.ErrorReason)
		}
		if res.Evidence.Added != 1 {
			t.Errorf("evidence added = %d, want 1", res.Evidence.Added)
		}
	})
}

func TestDiffHelperFunctions(t *testing.T) {
	t.Parallel()

	t.Run("hunkOldStart parses valid and malformed headers", func(t *testing.T) {
		t.Parallel()
		if got := hunkOldStart("@@ -1,3 +1,4 @@"); got != 1 {
			t.Errorf("hunkOldStart(valid) = %d, want 1", got)
		}
		if got := hunkOldStart("@@ bogus @@"); got != 0 {
			t.Errorf("hunkOldStart(malformed) = %d, want 0", got)
		}
		if got := hunkOldStart("not a hunk"); got != 0 {
			t.Errorf("hunkOldStart(non-header) = %d, want 0", got)
		}
	})

	t.Run("malformed hunk header yields no hunks", func(t *testing.T) {
		t.Parallel()
		raw := "--- a/f.go\n+++ b/f.go\n@@ bogus @@\n+line\n"
		if !looksLikeUnifiedDiff(raw) {
			t.Fatal("expected looksLikeUnifiedDiff true")
		}
		if len(parseDiffHunks(raw)) != 0 {
			t.Fatal("expected malformed hunk header to be skipped")
		}
	})

	t.Run("new-file hunk against non-empty base rejected", func(t *testing.T) {
		t.Parallel()
		if _, err := applyUnifiedDiff("existing\n", newFileDiff); err == nil {
			t.Fatal("expected error applying new-file diff to non-empty base")
		}
	})

	t.Run("splitLines edge cases", func(t *testing.T) {
		t.Parallel()
		if got := splitLines(""); got != nil {
			t.Errorf("splitLines empty = %v, want nil", got)
		}
		if got := splitLines("a\nb\n"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
			t.Errorf("splitLines = %v, want [a b]", got)
		}
		if got := splitLines("single"); len(got) != 1 || got[0] != "single" {
			t.Errorf("splitLines = %v, want [single]", got)
		}
	})

	t.Run("patchTargetEscapes accepts dev null and relative headers", func(t *testing.T) {
		t.Parallel()
		if patchTargetEscapes("--- /dev/null\n+++ b/fresh.txt\n") {
			t.Error("dev-null and relative headers must not be flagged as escaping")
		}
		if !patchTargetEscapes("--- a/../up.txt\n+++ b/up.txt\n") {
			t.Error("expected ../ header to be flagged")
		}
	})

	t.Run("isPathTraversal edge cases", func(t *testing.T) {
		t.Parallel()
		for _, p := range []string{"", "..", "../x", "a/../../b"} {
			if !isPathTraversal(p) {
				t.Errorf("isPathTraversal(%q) = false, want true", p)
			}
		}
		for _, p := range []string{"a.txt", "nested/a.txt", "/abs/path.txt", "."} {
			if isPathTraversal(p) {
				t.Errorf("isPathTraversal(%q) = true, want false", p)
			}
		}
	})
}

func TestSnapshotCommitRollbackRoundTrip(t *testing.T) {
	t.Parallel()

	e := NewExecutor()
	v := NewValidator()
	dir := t.TempDir()
	path := writeFile(t, dir, "f.txt", "line1\nline2\nline3\n")

	backup, err := e.PrepareSnapshot(path)
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	proposal := ProposedMutation{
		ProposalID: "roundtrip",
		TargetRef:  &target.TargetRef{Raw: "f.txt", Canonical: path, Exists: true},
		RawPatch:   sampleDiff,
	}

	res, err := v.Validate(proposal)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.Valid {
		t.Fatalf("proposal invalid: %s", res.ErrorReason)
	}
	if res.Evidence.Added != 1 || res.Evidence.Deleted != 0 {
		t.Errorf("evidence = +%d -%d, want +1 -0", res.Evidence.Added, res.Evidence.Deleted)
	}

	if err := e.Commit(proposal, backup); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := readFile(t, path); got != "line1\nline2\nline3\nline4\n" {
		t.Fatalf("post-commit content = %q", got)
	}

	if err := e.Rollback(backup); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := readFile(t, path); got != "line1\nline2\nline3\n" {
		t.Errorf("post-rollback content = %q, want original", got)
	}
}

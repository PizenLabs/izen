package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/runtime/target"
)

// TestFileExecutorCommit_RefusesSymlinkEscapeThroughTheKernel is the
// functional proof that the FileExecutor mutation seam now crosses the Runtime
// Kernel.
//
// Before the migration, Commit placed the resolved bytes with a hand-rolled
// temp-file-and-rename protocol: the temp file was created inside the target's
// directory and renamed over the target. That follows a symlink wherever it
// points, so a target whose parent is a symlink out of the workspace wrote
// outside the grant with no event, no evidence and no verification. The
// kernel's filesystem capability resolves the real path and refuses the
// escape, so this test observes a behaviour the legacy write could not
// produce: the mutation is refused and the file beyond the boundary is
// byte-for-byte unchanged.
//
// It is a behaviour assertion rather than a spy on an internal call precisely
// because a spy could be satisfied by a seam that was wired up and then not
// used — which is the failure mode this whole migration has to rule out.
func TestFileExecutorCommit_RefusesSymlinkEscapeThroughTheKernel(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	victim := filepath.Join(outside, "escaped.txt")
	if err := os.WriteFile(victim, []byte("before"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	e := execIn(root)
	targetPath := filepath.Join(root, "link", "escaped.txt")

	// The snapshot is not what this test is about: reading the target follows
	// the symlink, so it may legitimately report the escaping file as present.
	// That is a read, and reads were never the seam under migration. What has
	// to hold is that committing those bytes cannot place them outside.
	backup, err := e.PrepareSnapshot(targetPath)
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	proposal := ProposedMutation{
		ProposalID: "escape",
		TargetRef:  &target.TargetRef{Raw: "link/escaped.txt", Canonical: targetPath, Exists: false},
		RawPatch:   "after",
	}
	err = e.Commit(t.Context(), proposal, backup)
	if err == nil {
		t.Fatal("a write whose target resolves outside the workspace succeeded; the kernel seam was bypassed")
	}
	if !strings.Contains(err.Error(), "outside the workspace root") {
		t.Errorf("error = %v; want the kernel's confinement refusal to name the boundary", err)
	}

	body, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatalf("reading the outside file: %v", readErr)
	}
	if string(body) != "before" {
		t.Fatalf("the workspace boundary was crossed: outside file = %q, want %q", string(body), "before")
	}
}

// TestFileExecutorCommit_StillWritesConfinedTargets proves the seam did not
// simply refuse everything. A confined target must still be written, and its
// bytes must match the resolved proposal content.
//
// Without this, "the kernel refused" would be a complete description of the
// migrated path and every caller would be broken in a way no lock notices.
func TestFileExecutorCommit_StillWritesConfinedTargets(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := writeFile(t, root, "confined.txt", "before\n")

	e := execIn(root)
	backup, err := e.PrepareSnapshot(path)
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	proposal := ProposedMutation{
		ProposalID: "confined",
		TargetRef:  &target.TargetRef{Raw: "confined.txt", Canonical: path, Exists: true},
		RawPatch:   "after\n",
	}
	if err := e.Commit(t.Context(), proposal, backup); err != nil {
		t.Fatalf("Commit on a confined target: %v", err)
	}
	if got := readFile(t, path); got != "after\n" {
		t.Errorf("content = %q, want %q", got, "after\n")
	}
}

// TestFileExecutorRollback_StillRestoresAndRemoves pins the two rollback
// directions through the kernel seam.
//
// A rollback that quietly stopped working leaves the workspace in exactly the
// state a failed commit is supposed to prevent, and it does so silently: the
// commit reports an error either way. Restoring an overwritten file and
// removing one the transaction created are separate kernel operations with
// separate evidence, so both are asserted rather than one standing in for the
// other.
func TestFileExecutorRollback_StillRestoresAndRemoves(t *testing.T) {
	t.Parallel()

	t.Run("restores overwritten content", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeFile(t, dir, "f.txt", "original\n")
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatalf("chmod: %v", err)
		}

		e := execIn(dir)
		backup, err := e.PrepareSnapshot(path)
		if err != nil {
			t.Fatalf("PrepareSnapshot: %v", err)
		}
		if err := e.Commit(t.Context(), ProposedMutation{
			ProposalID: "restore",
			TargetRef:  &target.TargetRef{Raw: "f.txt", Canonical: path, Exists: true},
			RawPatch:   "mutated\n",
		}, backup); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		if err := e.Rollback(t.Context(), backup); err != nil {
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
	})

	t.Run("removes what the transaction created", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "created.txt")

		e := execIn(dir)
		backup, err := e.PrepareSnapshot(path)
		if err != nil {
			t.Fatalf("PrepareSnapshot: %v", err)
		}
		if err := e.Commit(t.Context(), ProposedMutation{
			ProposalID: "create",
			TargetRef:  &target.TargetRef{Raw: "created.txt", Canonical: path, Exists: false},
			RawPatch:   "created\n",
		}, backup); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("file should exist after commit: %v", err)
		}

		if err := e.Rollback(t.Context(), backup); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("file should be removed after rollback, stat err = %v", err)
		}
	})
}

// TestFileExecutor_UnboundWorkspaceRefusesToMutate pins the construction
// requirement the kernel seam creates.
//
// The grant is formed over one workspace root. An executor that was never told
// which workspace it may mutate cannot name one, and the failure mode to rule
// out is a fallback that quietly uses the process working directory — that
// would place bytes outside the grant the caller believes it holds. The
// executor must refuse instead.
func TestFileExecutor_UnboundWorkspaceRefusesToMutate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "f.txt", "original\n")

	e := NewExecutor()
	backup, err := e.PrepareSnapshot(path)
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	proposal := ProposedMutation{
		ProposalID: "unbound",
		TargetRef:  &target.TargetRef{Raw: "f.txt", Canonical: path, Exists: true},
		RawPatch:   "mutated\n",
	}

	err = e.Commit(t.Context(), proposal, backup)
	if err == nil || !strings.Contains(err.Error(), "no workspace bound") {
		t.Fatalf("Commit on an unbound executor: err = %v; want an explicit refusal to place bytes", err)
	}
	if got := readFile(t, path); got != "original\n" {
		t.Errorf("an unbound executor mutated the workspace: content = %q, want %q", got, "original\n")
	}
	if err := e.Rollback(t.Context(), backup); err == nil {
		t.Error("Rollback on an unbound executor reported success")
	}
	if got := readFile(t, path); got != "original\n" {
		t.Errorf("content after refused rollback = %q, want %q", got, "original\n")
	}
}

// TestFileExecutor_TargetOutsideWorkspaceIsRefused covers the lexical half of
// confinement, which Core owns and which runs before the kernel is asked.
//
// Both halves matter and neither replaces the other: this check refuses a
// target that climbs out of the root without opening anything, and the kernel
// re-checks from its own root handle. A test that only covered the symlink
// case would pass even if the lexical guard were deleted.
func TestFileExecutor_TargetOutsideWorkspaceIsRefused(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := filepath.Join(base, "victim.txt")
	if err := os.WriteFile(outside, []byte("before"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	e := execIn(root)
	if err := e.Rollback(t.Context(), &FileBackup{Path: outside, Exists: false}); err == nil {
		t.Fatal("rollback removed a target outside the declared workspace")
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "before" {
		t.Fatalf("a target outside the workspace was disturbed: body=%q err=%v", string(body), err)
	}
}

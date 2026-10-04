package filesystem_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/runtime/capabilities/filesystem"
	"github.com/PizenLabs/izen/runtime/kernel"
)

// TestFileDeleteRemovesAFileAndReportsIt proves the removing half of the
// mutation boundary: the file is gone and the evidence says exactly how.
func TestFileDeleteRemovesAFileAndReportsIt(t *testing.T) {
	caps, root := newWorkspace(t)
	abs := writeFile(t, root, "obsolete.txt", "old\n")
	del := lookup(t, caps, kernel.FileDelete)

	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileDelete}, []string{"obsolete.txt"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	obs, invokeErr := del.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileDelete,
		Step:       "remove",
		Target:     "obsolete.txt",
		Grant:      grant,
	})
	if invokeErr != nil {
		t.Fatalf("invoke: %v", invokeErr)
	}
	if obs.Verdict != kernel.VerdictPass {
		t.Fatalf("verdict = %s (%s); want PASS", obs.Verdict, obs.Detail)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("the file is still on disk (stat err = %v)", err)
	}

	kinds := map[kernel.EvidenceKind]bool{}
	for _, f := range obs.Facts {
		kinds[f.Kind] = true
	}
	if !kinds[kernel.EvidenceFileDeleted] {
		t.Error("no FILE_DELETED evidence; the mutation axis would stay untouched")
	}
	if !kinds[kernel.EvidenceFileAbsent] {
		t.Error("no FILE_ABSENT evidence; a DELETE contract could not observe the target gone")
	}
}

// TestFileDeleteOnAbsentTargetIsNoOp proves absence is a NO_OP, not a mutation.
func TestFileDeleteOnAbsentTargetIsNoOp(t *testing.T) {
	caps, _ := newWorkspace(t)
	del := lookup(t, caps, kernel.FileDelete)

	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileDelete}, []string{"missing.txt"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	obs, invokeErr := del.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileDelete,
		Step:       "remove",
		Target:     "missing.txt",
		Grant:      grant,
	})
	if invokeErr != nil {
		t.Fatalf("invoke: %v", invokeErr)
	}
	if obs.Verdict != kernel.VerdictNoOp {
		t.Fatalf("verdict = %s; want NO_OP for an already-absent target", obs.Verdict)
	}
	for _, f := range obs.Facts {
		if f.Kind == kernel.EvidenceFileDeleted {
			t.Fatal("a NO_OP delete produced FILE_DELETED evidence; that claims a mutation that did not happen")
		}
	}
}

// TestFileDeleteRefusesADirectory proves the capability removes one file, not a
// tree.
func TestFileDeleteRefusesADirectory(t *testing.T) {
	caps, root := newWorkspace(t)
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	del := lookup(t, caps, kernel.FileDelete)

	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileDelete}, []string{"dir"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	obs, invokeErr := del.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileDelete,
		Step:       "remove",
		Target:     "dir",
		Grant:      grant,
	})
	if invokeErr != nil {
		t.Fatalf("invoke: %v", invokeErr)
	}
	if obs.Verdict != kernel.VerdictFail {
		t.Fatalf("verdict = %s; want FAIL for a directory", obs.Verdict)
	}
	if _, err := os.Stat(filepath.Join(root, "dir")); err != nil {
		t.Fatalf("the directory was removed: %v", err)
	}
}

// TestFileDeleteRefusesASymlinkEscape proves the removing direction is confined
// exactly as the writing direction is.
func TestFileDeleteRefusesASymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("must survive"), 0o644); err != nil {
		t.Fatalf("seed victim: %v", err)
	}

	caps, err := filesystem.New(root)
	if err != nil {
		t.Fatalf("capability: %v", err)
	}
	del := lookup(t, caps, kernel.FileDelete)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileDelete}, []string{"link/victim.txt"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, invokeErr := del.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileDelete,
		Step:       "remove",
		Target:     "link/victim.txt",
		Grant:      grant,
	}); invokeErr == nil {
		t.Fatal("a symlink escape was not refused")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the victim outside the workspace was removed: %v", err)
	}
}

// TestFileDeleteRequiresAGrant proves the capability-local gate refuses a grant
// that does not name file.delete.
func TestFileDeleteRequiresAGrant(t *testing.T) {
	caps, _ := newWorkspace(t)
	del := lookup(t, caps, kernel.FileDelete)

	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileExists}, []string{"a.txt"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if authErr := del.Authorize(kernel.Request{
		Capability: kernel.FileDelete,
		Step:       "remove",
		Target:     "a.txt",
		Grant:      grant,
	}); authErr == nil {
		t.Fatal("file.delete authorized a grant that does not permit it")
	}
}

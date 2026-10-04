package filesystem_test

import (
	"context"
	"os"
	"testing"

	"github.com/PizenLabs/izen/runtime/kernel"
)

// TestFileWritePreservesExistingMode proves the atomic replace does not reset a
// destination's permission bits.
//
// The rename that commits a write installs a new inode. Without an explicit
// chmod to the destination's existing mode, every write would silently change
// the file's permissions to the creation default — a state change the request
// never asked for. The legacy os.WriteFile this capability replaced preserved
// the mode, so losing it here would be a regression.
func TestFileWritePreservesExistingMode(t *testing.T) {
	caps, root := newWorkspace(t)
	abs := writeFile(t, root, "script.sh", "#!/bin/sh\n")
	if err := os.Chmod(abs, 0o750); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	write := lookup(t, caps, kernel.FileWrite)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileWrite}, []string{"script.sh"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	obs, invokeErr := write.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileWrite,
		Step:       "write",
		Target:     "script.sh",
		Args:       map[string]string{"content": "#!/bin/sh\necho hi\n"},
		Grant:      grant,
	})
	if invokeErr != nil {
		t.Fatalf("invoke: %v", invokeErr)
	}
	if obs.Verdict != kernel.VerdictPass {
		t.Fatalf("verdict = %s (%s); want PASS", obs.Verdict, obs.Detail)
	}

	info, statErr := os.Stat(abs)
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Errorf("mode = %o; want 0750 preserved across the atomic replace", got)
	}
}

// TestFileWriteNewFileDefaultsTo0644 proves a destination that did not exist is
// created with the ordinary default.
func TestFileWriteNewFileDefaultsTo0644(t *testing.T) {
	caps, root := newWorkspace(t)
	write := lookup(t, caps, kernel.FileWrite)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileWrite}, []string{"new.txt"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, invokeErr := write.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileWrite,
		Step:       "write",
		Target:     "new.txt",
		Args:       map[string]string{"content": "hello\n"},
		Grant:      grant,
	}); invokeErr != nil {
		t.Fatalf("invoke: %v", invokeErr)
	}
	info, statErr := os.Stat(root + "/new.txt")
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("mode = %o; want 0644 for a newly created file", got)
	}
}

package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestApplyContextAppliesPatch asserts the context-bounded Apply path applies
// a valid patch normally (no regression from the unbounded Apply).
func TestApplyContextAppliesPatch(t *testing.T) {
	dir := t.TempDir()
	pm := NewPatchManager(dir)
	pm.SetAuthorization(testAuth())

	target := filepath.Join("sub", "file.txt")
	fullPath := filepath.Join(dir, target)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte("original content"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	patch := &Patch{
		ID:       "ctx-apply-1",
		File:     target,
		Original: "original content",
		Modified: "patched content",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pm.ApplyContext(ctx, patch); err != nil {
		t.Fatalf("ApplyContext: %v", err)
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "patched content" {
		t.Fatalf("file content = %q, want %q", string(data), "patched content")
	}
}

// TestApplyContextExpiredDeadlineReturnsTerminalTimeout asserts an already
// expired deadline aborts deterministically with ErrPatchApplyTimeout BEFORE
// any patch work is spawned — the terminal signal the TUI needs to emit an
// error message instead of freezing the "Applying hotfix..." spinner.
func TestApplyContextExpiredDeadlineReturnsTerminalTimeout(t *testing.T) {
	dir := t.TempDir()
	pm := NewPatchManager(dir)
	pm.SetAuthorization(testAuth())

	patch := &Patch{
		ID:       "ctx-timeout-1",
		File:     "file.txt",
		Original: "original content",
		Modified: "patched content",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // deadline already expired

	err := pm.ApplyContext(ctx, patch)
	if err == nil {
		t.Fatal("expected an error from ApplyContext with an expired deadline")
	}
	if !errors.Is(err, ErrPatchApplyTimeout) {
		t.Fatalf("expected ErrPatchApplyTimeout, got: %v", err)
	}
}

// TestApplyContextNilManagerGuards asserts a nil PatchManager yields a clean
// error rather than a nil-pointer panic that would orphan the UI spinner.
func TestApplyContextNilManagerGuards(t *testing.T) {
	var pm *PatchManager
	err := pm.ApplyContext(context.Background(), &Patch{})
	if err == nil {
		t.Fatal("expected a clean 'not configured' error from nil PatchManager")
	}
}

// TestApplyContextNilContextFallsBack asserts a nil context degrades to the
// legacy unbounded Apply (no panic, same semantics as Apply).
func TestApplyContextNilContextFallsBack(t *testing.T) {
	dir := t.TempDir()
	pm := NewPatchManager(dir)
	pm.SetAuthorization(testAuth())

	target := "file.txt"
	fullPath := filepath.Join(dir, target)
	if err := os.WriteFile(fullPath, []byte("original content"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	patch := &Patch{
		ID:       "ctx-nil-1",
		File:     target,
		Original: "original content",
		Modified: "patched content",
	}
	//nolint:staticcheck // deliberately exercises the nil-context fallback branch
	if err := pm.ApplyContext(nil, patch); err != nil {
		t.Fatalf("ApplyContext(nil ctx): %v", err)
	}
	data, _ := os.ReadFile(fullPath)
	if string(data) != "patched content" {
		t.Fatalf("file content = %q, want %q", string(data), "patched content")
	}
}

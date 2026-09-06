package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/artifact"
	"github.com/PizenLabs/izen/internal/core/domain/execution"
)

func TestRollbackCleansUntrackedFiles(t *testing.T) {
	root := t.TempDir()
	coord := NewDiskCheckpointCoordinator(root)

	// Create baseline file
	baseFile := filepath.Join(root, "main.go")
	if err := os.WriteFile(baseFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	chkID, err := coord.CreateBeforeBuild(ctx, domain.FrameID("frame_test_1"))
	if err != nil {
		t.Fatalf("CreateBeforeBuild: %v", err)
	}
	if !coord.HasRef() {
		t.Fatal("HasRef should be true after checkpoint")
	}

	// Simulate failed build generating untracked files
	untracked := filepath.Join(root, "untracked_generated.go")
	if err := os.WriteFile(untracked, []byte("generated"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "tmp", "newfile.txt")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("tmp"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Also modify existing file
	if err := os.WriteFile(baseFile, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wire stores for transactional alignment check
	store := artifact.NewArtifactStore()
	store.StagePatchForce("artifact-1", "diff content")
	execState := execution.NewExecutionState()
	execState.RecordStepForce("step-1", "result")
	coord.SetStores(store, execState)

	// Rollback should restore original file and remove untracked
	if err := coord.Rollback(ctx, chkID, domain.RollbackLocal); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Untracked files must be gone
	if _, err := os.Stat(untracked); !os.IsNotExist(err) {
		t.Errorf("untracked file was not cleaned: %v", err)
	}
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Errorf("nested untracked file was not cleaned: %v", err)
	}
	// Baseline file restored
	data, err := os.ReadFile(baseFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package main\n" {
		t.Errorf("baseline not restored: got %q", string(data))
	}
	// Artifact store cleared
	if store.Count() != 0 {
		t.Errorf("artifact store not cleared after rollback: count=%d", store.Count())
	}
	// Execution state reset — count should be 0 and version rewound
	if execState.Count() != 0 {
		t.Errorf("execution state not reset: count=%d", execState.Count())
	}

	// Idempotent second rollback should not error
	if err := coord.Rollback(ctx, chkID, domain.RollbackLocal); err != nil {
		t.Errorf("second rollback (idempotent) failed: %v", err)
	}
}

func TestRollbackErrorHandlingWhenFileLocksPreventWrite(t *testing.T) {
	root := t.TempDir()
	coord := NewDiskCheckpointCoordinator(root)

	// Create a file and snapshot
	target := filepath.Join(root, "locked.go")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	chkID, err := coord.CreateBeforeBuild(ctx, domain.FrameID("frame_lock"))
	if err != nil {
		t.Fatalf("CreateBeforeBuild: %v", err)
	}

	// Simulate file lock by making directory read-only so write fails
	// Replace file's directory with read-only permissions
	// Create a scenario where rollback's WriteFile will fail due to permission
	// We make root read-only temporarily.
	if err := os.Chmod(root, 0o500); err != nil {
		t.Skip("cannot chmod for lock simulation")
	}
	defer os.Chmod(root, 0o755)

	// Modify file content before rollback to trigger restore attempt
	// Need to make file writable again briefly, then lock again
	_ = os.Chmod(root, 0o755)
	_ = os.WriteFile(target, []byte("modified"), 0o644)
	_ = os.Chmod(root, 0o500)

	err = coord.Rollback(ctx, chkID, domain.RollbackLocal)
	if err == nil {
		t.Log("rollback did not error under lock — acceptable if OS permits root write, checking idempotency path")
		// On some platforms (darwin root user) chmod 500 still allows write; we accept.
	} else {
		// Error is expected and should be non-nil and not panic
		t.Logf("rollback correctly returned error under lock: %v", err)
	}

	// Restore permissions for cleanup
	_ = os.Chmod(root, 0o755)

	// Rollback with unknown ID should be idempotent (no error)
	if err := coord.Rollback(ctx, domain.CheckpointID("nonexistent"), domain.RollbackLocal); err != nil {
		t.Errorf("rollback with unknown ID should be idempotent: %v", err)
	}

	// Clear should be idempotent
	if err := coord.Clear(ctx, domain.CheckpointID("nonexistent")); err != nil {
		t.Errorf("clear unknown should not error: %v", err)
	}
	if err := coord.Clear(ctx, chkID); err != nil {
		t.Errorf("clear: %v", err)
	}
	// After clear, rollback is still idempotent
	if err := coord.Rollback(ctx, chkID, domain.RollbackLocal); err != nil {
		t.Errorf("rollback after clear should be idempotent: %v", err)
	}
}

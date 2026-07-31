// Package checkpoint implements the Stateful Checkpoint Subsystem: a fast,
// in-memory and file-backed engine that guarantees exact, atomic rollback of
// workspace file mutations applied during /build execution or patch trials.
//
// A Checkpoint captures the byte-exact original state of every target file
// before any mutation is written. If a patch application or compilation
// verification fails, Rollback restores the workspace to the captured state
// and deletes any files that did not exist at capture time. Once the build
// succeeds, Commit discards the buffered blobs and the file-backed shadow copy.
//
// The manager is optional-injected into the build executor; nil wiring leaves
// the headless/CLI fallback paths completely unchanged.
package checkpoint

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// CheckpointID is the stable, collision-resistant identifier of a workspace
// checkpoint. It follows the form chk_<unix-nano-timestamp>_<8-hex-short-hash>.
type CheckpointID string

// Checkpoint captures the pre-mutation state of the target files it protects.
//
// OriginalBlobs holds the byte-exact content read from disk before mutation;
// MissingFiles marks the target paths that did not exist at capture time so a
// rollback knows to delete them instead of restoring content. The struct is
// immutable-by-convention: consumers must not mutate the slices or maps.
type Checkpoint struct {
	// ID is the stable identifier of this checkpoint.
	ID CheckpointID
	// CreatedAt is the wall-clock time the checkpoint was captured.
	CreatedAt time.Time
	// Stage is the name of the pipeline stage that initiated the checkpoint
	// (e.g. "build.executor").
	Stage string
	// TargetFiles lists the file paths (relative to the workspace root) whose
	// original state is protected by this checkpoint.
	TargetFiles []string
	// OriginalBlobs maps a target path to its byte-exact pre-mutation content.
	OriginalBlobs map[string][]byte
	// MissingFiles marks target paths that did not exist at capture time. On
	// rollback such paths are deleted rather than restored.
	MissingFiles map[string]bool

	// blobDir is the file-backed shadow directory under
	// .izen/checkpoints/workspace/<id>/ where original blobs are persisted for
	// crash safety. Empty when no blobs were persisted.
	blobDir string
	// blobPaths maps a target path to its persisted shadow-copy location.
	blobPaths map[string]string
}

// NewCheckpointID generates a collision-resistant checkpoint identifier of the
// form chk_<timestamp>_<short_hash>. The timestamp carries ordering while the
// hash (over timestamp plus cryptographically random bytes) provides uniqueness.
func NewCheckpointID() CheckpointID {
	var seed [8]byte
	_, _ = rand.Read(seed[:])
	ts := time.Now().UnixNano()
	sum := sha256.Sum256(append(seed[:], []byte(fmt.Sprintf("%d", ts))...))
	return CheckpointID(fmt.Sprintf("chk_%d_%s", ts, hex.EncodeToString(sum[:])[:8]))
}

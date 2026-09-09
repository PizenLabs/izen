// Package atomicio provides crash-safe atomic file writes.
//
// All writes go to a temporary file in the same target directory, are
// fsynced to physical disk, then atomically swapped into place with
// rename(2). Readers of the destination observe the old or the new
// content, never a partial write. Temporary files are always cleaned up
// on failure.
package atomicio

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to filename atomically with the given
// permission bits.
//
// Protocol:
//
//  1. Create a temporary file in the same target directory
//     (<filename>.tmp.<rand>).
//  2. Write data and call Sync on the descriptor to flush to disk.
//  3. Atomically swap into place with os.Rename.
//  4. Best-effort fsync of the parent directory so the rename is durable.
//
// Any failure removes the temporary file and leaves an existing
// destination untouched.
func WriteFileAtomic(filename string, data []byte, perm os.FileMode) error {
	if filename == "" {
		return fmt.Errorf("atomicio: empty filename")
	}
	if perm == 0 {
		perm = 0o644
	}
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("atomicio: mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(filename)+".tmp.*")
	if err != nil {
		return fmt.Errorf("atomicio: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Cleanup on any failure before the rename consumes the temp file.
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicio: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicio: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomicio: close temp: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("atomicio: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, filename); err != nil {
		return fmt.Errorf("atomicio: rename temp: %w", err)
	}
	success = true

	// Best-effort directory fsync so the rename itself survives a crash.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

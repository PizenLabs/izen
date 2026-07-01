package session

import (
	"os"
	"path/filepath"
	"sort"
)

// RunRetentionPolicy enforces a maximum number of files in the given directory by deleting the oldest files.
// It returns an error if any occurs during the process, but individual file deletion errors are ignored.
// If the directory does not exist, it returns nil.
func RunRetentionPolicy(dirPath string, maxFiles int) error {
	// Check if the directory exists
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		return nil
	}

	// Read the directory entries
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return err
	}

	// If the number of entries is less than or equal to maxFiles, do nothing
	if len(entries) <= maxFiles {
		return nil
	}

	// Sort entries by name (ascending) because the names are timestamp-based and we want to remove the oldest
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	// Calculate how many files to delete
	filesToDelete := len(entries) - maxFiles

	// Delete the oldest files
	for i := 0; i < filesToDelete; i++ {
		entry := entries[i]
		fullPath := filepath.Join(dirPath, entry.Name())
		// We ignore errors for individual deletions to not break the loop
		_ = os.RemoveAll(fullPath)
	}

	return nil
}

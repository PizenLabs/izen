package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// safetyMarkerSubstrings marks checkpoint labels that must never be pruned.
var safetyMarkerSubstrings = []string{"safety", "protected", "keep"}

// checkpointEntry is one prunable checkpoint directory with its creation
// timestamp and protection status.
type checkpointEntry struct {
	id        string
	dir       string
	timestamp int64
}

// PruneCheckpoints enforces FIFO retention over .izen/checkpoints/cp-*:
// checkpoints are sorted chronologically by creation timestamp and the
// oldest are removed via os.RemoveAll until count <= maxRetention.
//
// Directories not matching cp-* (e.g. the session-start snapshot) are never
// counted nor deleted. Checkpoints carrying a labeled safety marker (label
// containing safety/protected/keep, case-insensitive) or a sentinel file
// (.keep, KEEP, protected) inside the checkpoint directory are excluded
// from deletion and do not count toward the retention limit.
//
// A negative maxRetention is an error. Zero means remove all non-protected
// checkpoints.
func PruneCheckpoints(workDir string, maxRetention int) error {
	if strings.TrimSpace(workDir) == "" {
		return fmt.Errorf("checkpoint: empty workDir")
	}
	if maxRetention < 0 {
		return fmt.Errorf("checkpoint: negative maxRetention %d", maxRetention)
	}
	root := checkpointsDir(workDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("checkpoint: read %s: %w", root, err)
	}
	var candidates []checkpointEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "cp-") {
			continue
		}
		// Contain the name to a single path element.
		if name != filepath.Base(name) || strings.Contains(name, "..") {
			continue
		}
		dir := filepath.Join(root, name)
		ts, protected := inspectCheckpointDir(dir, name)
		if protected {
			continue
		}
		candidates = append(candidates, checkpointEntry{
			id:        name,
			dir:       dir,
			timestamp: ts,
		})
	}
	if len(candidates) <= maxRetention {
		return nil
	}
	// Oldest first (chronological FIFO).
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].timestamp != candidates[j].timestamp {
			return candidates[i].timestamp < candidates[j].timestamp
		}
		return candidates[i].id < candidates[j].id
	})
	excess := len(candidates) - maxRetention
	for i := 0; i < excess; i++ {
		// Safety: only remove directories directly under the checkpoints
		// root with the cp- prefix.
		if !strings.HasPrefix(candidates[i].dir, root+string(os.PathSeparator)) {
			continue
		}
		if err := os.RemoveAll(candidates[i].dir); err != nil {
			return fmt.Errorf("checkpoint: prune %s: %w", candidates[i].id, err)
		}
	}
	return nil
}

// inspectCheckpointDir returns the creation timestamp (nanoseconds) and
// protection status for one checkpoint directory.
func inspectCheckpointDir(dir, name string) (int64, bool) {
	// Sentinel files protect the checkpoint regardless of manifest state.
	for _, sentinel := range []string{".keep", "KEEP", "protected"} {
		if _, err := os.Stat(filepath.Join(dir, sentinel)); err == nil {
			return checkpointTimestampFallback(dir, name), true
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if err != nil {
		return checkpointTimestampFallback(dir, name), false
	}
	var rec struct {
		Label     string    `json:"label"`
		Timestamp time.Time `json:"timestamp"`
	}
	if jerr := json.Unmarshal(data, &rec); jerr == nil && !rec.Timestamp.IsZero() {
		if isSafetyLabel(rec.Label) {
			return rec.Timestamp.UnixNano(), true
		}
		return rec.Timestamp.UnixNano(), false
	}
	// Corrupt or missing timestamp: fall back to directory/suffix ordering.
	// Still honor a readable safety label.
	var labelOnly struct {
		Label string `json:"label"`
	}
	if jerr := json.Unmarshal(data, &labelOnly); jerr == nil && isSafetyLabel(labelOnly.Label) {
		return checkpointTimestampFallback(dir, name), true
	}
	return checkpointTimestampFallback(dir, name), false
}

// checkpointTimestampFallback derives a timestamp from the cp-<nanos> suffix
// (creation nanos) or directory mod time so pruning stays chronological even
// for corrupt manifests.
func checkpointTimestampFallback(dir, name string) int64 {
	var suffix int64
	if _, err := fmt.Sscanf(name, "cp-%d", &suffix); err == nil && suffix > 0 {
		return suffix
	}
	if fi, err := os.Stat(dir); err == nil {
		if ts := fi.ModTime().UnixNano(); ts > 0 {
			return ts
		}
	}
	return 0
}

// isSafetyLabel reports whether a checkpoint label is a safety marker.
func isSafetyLabel(label string) bool {
	lower := strings.ToLower(strings.TrimSpace(label))
	if lower == "" {
		return false
	}
	for _, m := range safetyMarkerSubstrings {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

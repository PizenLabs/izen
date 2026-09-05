package autonomy

import "os"

// AdaptiveSelector applies the pre-execution heuristic for direct file
// mutations ($hot / $prompt on a resolved target): it forces the bounded
// patch contract when the target exceeds safe full-rewrite thresholds.
//
// Heuristic (fail-closed):
//   - IF target file size > 500 bytes  → BOUNDED_PATCH
//   - IF model max_output <= 2048 tokens → BOUNDED_PATCH
//
// In both cases FULL_REWRITE is bypassed and the initial strategy is
// forced to BOUNDED_PATCH.
func AdaptiveSelectStrategy(targetPath string, fileSizeBytes int, maxOutputTokens int) string {
	if fileSizeBytes > 500 || maxOutputTokens <= 2048 {
		return StrategyBoundedPatch
	}
	// Small file + generous budget: full-rewrite remains permissible.
	return StrategyFullArtifact
}

// FileSizeBytes reads the resolved workspace-relative file size. It
// returns -1 when the file does not exist or cannot be read; the caller
// must treat -1 as "unknown size" (no forced bounded patch from size).
func FileSizeBytes(targetPath string) int {
	info, err := os.Stat(targetPath)
	if err != nil {
		return -1
	}
	return int(info.Size())
}

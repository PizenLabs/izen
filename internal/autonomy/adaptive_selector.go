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
// Constrained Output Budget Invariant: models capped at max_output <= 1024
// (free-tier style) MUST force max_tokens = min(requested, 980) and DISABLE
// FULL_REWRITE entirely, forcing SEARCH_REPLACE output. The <= 2048 rule
// already subsumes the <= 1024 constrained band.
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

// ConstrainedOutputThreshold is the max_output ceiling at or below which a
// model is treated as constrained (free-tier style).
const ConstrainedOutputThreshold = 1024

// ConstrainedMaxTokens is the enforced output budget for constrained models.
const ConstrainedMaxTokens = 980

// IsConstrainedOutputBudget reports whether a model's max output budget is
// constrained (<= 1024, positive only).
func IsConstrainedOutputBudget(maxOutputTokens int) bool {
	return maxOutputTokens > 0 && maxOutputTokens <= ConstrainedOutputThreshold
}

// ClampMaxTokensForConstrained enforces min(requested, 980) for constrained
// budgets; unconstrained budgets pass through unchanged.
func ClampMaxTokensForConstrained(requested, maxOutputTokens int) int {
	if !IsConstrainedOutputBudget(maxOutputTokens) {
		return requested
	}
	if requested <= 0 || requested > ConstrainedMaxTokens {
		return ConstrainedMaxTokens
	}
	return requested
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

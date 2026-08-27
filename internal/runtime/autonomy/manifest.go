package autonomy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── P2 Manifest-First Adaptive Decomposition ────────────────────────────────
//
// Two-Pass Execution Strategy:
//
//	Pass 1 (Manifest): Model emits a lightweight JSON Manifest defining proposed
//	   mutation targets and operations. This pass is strictly READ-ONLY and
//	   isolated from workspace disk writes — it never mutates the filesystem.
//	Pass 2 (Bounded Execution): Runtime calculates exact mutation scope based on
//	   the Manifest. File splitting occurs ONLY if actual mutation surface
//	   exceeds max_output token budget.
//
// Semantic Splitting over Line Chunks: Decomposition must only occur along
// logical boundaries (AST nodes, top-level sections, or explicit semantic
// manifests), never arbitrary line ranges.

// MutationSpec describes one proposed mutation inside the manifest.
type MutationSpec struct {
	// Symbol is the Go/TS symbol or HTML section identifier (e.g., "HandlerFoo", "section#hero").
	Symbol string `json:"symbol,omitempty"`
	// Selector is the CSS selector or HTML selector (e.g., "#sidebar", ".card", "<section#hero>").
	Selector string `json:"selector,omitempty"`
	// Action is the mutation operation: delete, modify, insert.
	Action string `json:"action"`
	// EstimatedLines is the estimated number of lines this mutation touches.
	EstimatedLines int `json:"estimatedLines"`
}

// MutationManifest is the lightweight JSON manifest emitted in Pass 1.
// It is read-only and must never trigger workspace disk writes.
type MutationManifest struct {
	// TargetFile is the workspace-relative file the mutations apply to.
	TargetFile string `json:"targetFile"`
	// Intent is the human-readable objective (e.g., "delete redundant content").
	Intent string `json:"intent"`
	// Mutations is the ordered list of proposed mutation specs.
	Mutations []MutationSpec `json:"mutations"`
}

// ParseMutationManifest parses raw JSON bytes into a MutationManifest.
// It is strictly read-only: no filesystem access, no state mutation.
// Returns error for empty, corrupt, or structurally invalid manifests so the
// caller can safely fall back to a single bounded inspection pass.
func ParseMutationManifest(raw []byte) (*MutationManifest, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("manifest: empty payload")
	}
	var m MutationManifest
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return nil, fmt.Errorf("manifest: invalid JSON: %w", err)
	}
	if strings.TrimSpace(m.TargetFile) == "" {
		return nil, fmt.Errorf("manifest: targetFile is required")
	}
	// Validate mutations if present.
	for i, mut := range m.Mutations {
		act := strings.ToLower(strings.TrimSpace(mut.Action))
		switch act {
		case "delete", "modify", "insert":
			// ok
		case "":
			return nil, fmt.Errorf("manifest: mutations[%d] missing action", i)
		default:
			return nil, fmt.Errorf("manifest: mutations[%d] invalid action %q (want delete|modify|insert)", i, mut.Action)
		}
		if mut.Symbol == "" && mut.Selector == "" {
			// Require at least one identifier for semantic targeting.
			return nil, fmt.Errorf("manifest: mutations[%d] requires symbol or selector", i)
		}
		if mut.EstimatedLines < 0 {
			return nil, fmt.Errorf("manifest: mutations[%d] estimatedLines cannot be negative", i)
		}
		// Normalize action to lower case.
		m.Mutations[i].Action = act
	}
	return &m, nil
}

// EstimateMutationSurface replaces the naive full-file heuristic
// (target_tokens * FullRewriteTokenMultiplier) with a manifest-aware
// surface estimate. When a valid manifest is present, the estimate is the
// sum of its mutations' EstimatedLines converted to tokens; otherwise it
// falls back to the full-file estimate. This is the sole authority for
// bypass vs decompose decisions.
func EstimateMutationSurface(manifest *MutationManifest, targetContent []byte) int {
	if manifest == nil || len(manifest.Mutations) == 0 {
		// Fallback: full-file estimate (backward compatible when no manifest).
		if len(targetContent) == 0 {
			return 0
		}
		return planner.EstimateTokens(len(targetContent)) * execution.FullRewriteTokenMultiplier
	}
	totalLines := 0
	for _, mut := range manifest.Mutations {
		if mut.EstimatedLines > 0 {
			totalLines += mut.EstimatedLines
		} else {
			// Default minimal surface for a mutation without explicit estimate.
			totalLines += 1
		}
	}
	// Convert lines to tokens using a conservative per-line byte estimate.
	// Use targetContent's average line length when available, clamped to
	// [30, 100] bytes to avoid degenerate estimates on minified or sparse files.
	avgBytesPerLine := 60
	if len(targetContent) > 0 {
		// Count lines without splitting the whole file.
		lineCount := 0
		for _, c := range targetContent {
			if c == '\n' {
				lineCount++
			}
		}
		lineCount++ // last line without trailing newline
		if lineCount > 0 {
			avg := len(targetContent) / lineCount
			if avg < 30 {
				avg = 30
			} else if avg > 100 {
				avg = 100
			}
			avgBytesPerLine = avg
		}
	}
	totalBytes := totalLines * avgBytesPerLine
	return planner.EstimateTokens(totalBytes) * execution.FullRewriteTokenMultiplier
}

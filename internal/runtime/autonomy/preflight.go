package autonomy

import (
	"context"
	"errors"
	"fmt"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── Pass 1 Manifest Auto-Hook (Preflight Autonomy Loop) ─────────────────────
//
// When Boundary 2 refuses a target because its full-file rewrite estimate
// exceeds max_output (I5, preflight_infeasible), the loop no longer jumps
// straight to a line-chunked DAG. It first issues a lightweight READ-ONLY
// Pass 1 Manifest Request to the model:
//
//	Pass 1 (Manifest)  — a tiny provider invocation that emits ONLY a raw
//	                     MutationManifest JSON naming the minimal mutation
//	                     surface for the objective. Strictly read-only: it
//	                     never reads the workspace beyond the caller-provided
//	                     target bytes and never writes anything.
//	Pass 2 (Bounded)    — the parsed manifest is fed to AdaptiveDecompose, so
//	                     the DAG is scoped to the mutation surface. If the
//	                     surface fits the budget the file executes as ONE
//	                     atomic unit (1 API call); otherwise sub-tasks are
//	                     grouped along the semantic blocks the manifest marked
//	                     for modification — never a naive line slicer.
//
// When Pass 1 fails (transport error, malformed JSON, or an empty mutation
// list) the loop falls back to a SINGLE-PASS BOUNDED INSPECTION task. The
// fallback NEVER re-slices the file into arbitrary line ranges: pruning
// discipline is preserved even without a usable manifest.

// ManifestPassFunc is the injectable Pass 1 manifest generator. It returns a
// parsed MutationManifest or an error; the preflight hook treats an error or
// an empty mutation list as "no manifest available" and falls back to the
// single-pass bounded inspection. Implementations MUST be read-only.
type ManifestPassFunc func(ctx context.Context, objective string, targetContent []byte) (*MutationManifest, error)

// ManifestPassForExecutor binds a ManifestPassFunc to the RuntimeExecutor's
// provider. It is the production wiring of the Pass 1 auto-hook: the executor
// remains the single authority that invokes the provider.
func ManifestPassForExecutor(exec *execution.RuntimeExecutor) ManifestPassFunc {
	return func(ctx context.Context, objective string, targetContent []byte) (*MutationManifest, error) {
		return ExecuteManifestPass(ctx, exec, objective, targetContent)
	}
}

// ExecuteManifestPass performs the read-only Pass 1 manifest request against
// the executor's provider and parses the raw response into a MutationManifest.
// It is strictly read-only: the provider receives the caller-provided target
// bytes and a minimal system prompt enforcing raw MutationManifest JSON output;
// nothing is ever written to the workspace. The parsed manifest is the only
// thing that crosses back to the DAG strategy decision.
func ExecuteManifestPass(ctx context.Context, exec *execution.RuntimeExecutor, prompt string, targetContent []byte) (*MutationManifest, error) {
	if exec == nil {
		return nil, errors.New("autonomy: manifest pass requires a runtime executor")
	}
	raw, err := exec.InvokeManifestPass(ctx, prompt, targetContent)
	if err != nil {
		return nil, fmt.Errorf("autonomy: manifest pass: %w", err)
	}
	manifest, err := ParseMutationManifest([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("autonomy: manifest pass: %w", err)
	}
	return manifest, nil
}

// PreflightRequiresManifest reports whether the canonical Boundary-2 estimate
// (target_bytes/4 × FullRewriteTokenMultiplier) exceeds max_output, i.e.
// whether the preflight autonomy loop MUST issue the automatic Pass 1 manifest
// request before determining the DAG strategy. It mirrors execution's
// EvaluatePreflight formula so the hook fires on exactly the same condition
// the preflight guard refuses the objective under.
func PreflightRequiresManifest(targetBytes, maxOutputTokens int) bool {
	if maxOutputTokens <= 0 {
		return false // unbounded budget: not provably infeasible at this boundary
	}
	if targetBytes <= 0 {
		return false // creation intent: no baseline to size-estimate
	}
	estimated := (targetBytes / 4) * execution.FullRewriteTokenMultiplier
	return estimated > maxOutputTokens
}

// runPreflight is the automatic Pass 1 manifest hook of the preflight autonomy
// loop. Given a target the Boundary-2 guard refused as infeasible, it decides
// the DAG strategy:
//
//  1. When a ManifestPassFunc is wired, it issues the read-only manifest
//     request and feeds the parsed manifest into AdaptiveDecompose. A valid
//     manifest with at least one mutation yields either a single atomic task
//     (surface ≤ budget) or a semantic DAG pruned to the mutated blocks.
//  2. On manifest failure or an empty mutation list it falls back to a
//     SINGLE-PASS BOUNDED INSPECTION task — never a naive line slicer.
//  3. When no manifest pass is wired (backward-compatible test/CLI paths) it
//     defers to the injected DecomposeFunc unchanged.
func (d *Driver) runPreflight(ctx context.Context, objective, target string, source []byte, baseDigest string, maxOut int) (*planner.ExecutionDAG, error) {
	if d.decompose == nil {
		return nil, errors.New("autonomy: decomposition disabled")
	}
	if d.manifestPass == nil {
		return d.decompose(objective, target, source, baseDigest, maxOut)
	}
	if !PreflightRequiresManifest(len(source), maxOut) {
		return d.decompose(objective, target, source, baseDigest, maxOut)
	}
	// ── AUTOMATIC PASS 1 MANIFEST REQUEST ─────────────────────────────
	manifest, mErr := d.manifestPass(ctx, objective, source)
	if mErr == nil && manifest != nil && len(manifest.Mutations) > 0 {
		dag, err := AdaptiveDecompose(objective, target, source, baseDigest, maxOut, manifest)
		if err != nil {
			diagnosticf("[preflight] manifest-scoped decomposition failed: %v — falling back to single-pass bounded inspection", err)
		} else {
			diagnosticf("[preflight] Pass 1 manifest staged manifest_target=%s mutations=%d sub_tasks=%d manifest_scoped=%v",
				manifest.TargetFile, len(manifest.Mutations), len(dag.SubTasks), dag.ManifestScoped)
			return dag, nil
		}
	}
	// Manifest generation failed or returned empty mutations: fall back to a
	// single-pass bounded inspection. Deliberately NOT the naive line slicer —
	// pruning discipline must hold even without a usable manifest.
	if mErr != nil {
		diagnosticf("[preflight] Pass 1 manifest unavailable (%v) — single-pass bounded inspection", mErr)
	} else {
		diagnosticf("[preflight] Pass 1 manifest returned no mutations — single-pass bounded inspection")
	}
	return singleTaskDAG(objective, target, source, baseDigest, maxOut, manifest, planner.SubTaskBudget(maxOut))
}

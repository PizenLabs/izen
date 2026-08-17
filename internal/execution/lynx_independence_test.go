package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/retrieval"
)

// ── Phase 1 Invariant 8: Lynx is never an execution dependency ─────────────
//
// The external Lynx engine (the `lx` binary) is an optional
// repository-discovery/evidence capability. Lynx availability MUST NOT
// determine which execution architecture is used: the RuntimeExecutor path is
// identical whether the global search router runs the native engine or the
// hybrid Lynx engine. These tests pin that independence on the strategy /
// target resolution surface the executor consumes.

// TestExecutorTargetResolutionIndependentOfLynx proves the executor's
// canonical strategy + target resolution produces the identical profile under
// both router states. Lynx enhances search; it never selects the execution
// path, never resolves the mutation target, and never appears on the
// executor's call graph.
func TestExecutorTargetResolutionIndependentOfLynx(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html><body><p>keep</p></body></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := dir

	resolve := func() (strategy string, targets string) {
		g := NewIntentGateway(root)
		profile := g.SelectStrategy("read index.html and remove redundant content")
		var parts []string
		for _, tg := range profile.Targets {
			if tg.Resolved != "" {
				parts = append(parts, tg.Resolved)
			}
		}
		return profile.Strategy.String(), strings.Join(parts, ",")
	}

	// Lynx unavailable: native fallback search.
	retrieval.SetGlobalRouter(retrieval.NewRouterWithEngine(retrieval.NewNativeGoEngine(root), retrieval.EngineNative, nil))
	nativeStrategy, nativeTargets := resolve()

	// Lynx available: hybrid enhanced search.
	retrieval.SetGlobalRouter(retrieval.NewRouterWithEngine(retrieval.NewLynxAdapter("/usr/bin/lx", root), retrieval.EngineLynx, nil))
	lynxStrategy, lynxTargets := resolve()

	if nativeStrategy != lynxStrategy {
		t.Fatalf("strategy differs by Lynx availability: native=%q lynx=%q — Lynx must never select the execution path", nativeStrategy, lynxStrategy)
	}
	if nativeTargets != lynxTargets {
		t.Fatalf("target resolution differs by Lynx availability: native=%q lynx=%q — Lynx must never resolve mutation targets", nativeTargets, lynxTargets)
	}
	if nativeStrategy != "targeted_mutation" {
		t.Fatalf("precondition: expected targeted_mutation, got %q", nativeStrategy)
	}
	if nativeTargets != "index.html" {
		t.Fatalf("precondition: expected index.html, got %q", nativeTargets)
	}
}

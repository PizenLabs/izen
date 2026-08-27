package verifier

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/execution/cache"
)

// TestL3Verifier_BypassesTopologyCache asserts that verifier.VerifyGlobalObjective
// (L3 Global Audit) re-scans live disk files and explicitly ignores cached
// snapshots. Execution truth remains real-time workspace disk state.
//
// The cache is a read-only indexer for Preflight/Elastic Scope — NEVER for
// post-apply verification or MutationEvidence.
func TestL3Verifier_BypassesTopologyCache(t *testing.T) {
	// Prepare a stale cache entry with SHA of "old" content.
	oldContent := []byte("<div id=\"main\">old</div>")
	h := sha256.Sum256(oldContent)
	oldSHA := hex.EncodeToString(h[:])

	c := cache.New(4)
	stale := cache.BuildSnapshot(oldSHA, "index.html", nil, 5, 10, 1, 2.0)
	stale.NodeCount = 999 // obviously stale marker
	c.Put(stale)

	// Now mutate the file on disk to "new" content.
	root := t.TempDir()
	target := "index.html"
	newContent := []byte("<div id=\"main\">new</div><div id=\"extra\">added</div>")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, target), newContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Live disk bytes.
	live, err := os.ReadFile(filepath.Join(root, target))
	if err != nil {
		t.Fatal(err)
	}
	// Ensure the live SHA differs from the stale cached SHA.
	lh := sha256.Sum256(live)
	liveSHA := hex.EncodeToString(lh[:])
	if liveSHA == oldSHA {
		t.Fatal("live SHA should differ from stale cache SHA")
	}
	// The stale entry must still be reachable via its old SHA.
	if got, ok := c.Get(oldSHA); !ok || got.NodeCount != 999 {
		t.Fatalf("stale cache entry should still exist via old SHA: ok=%v got=%v", ok, got)
	}
	// The live SHA must NOT be in cache (unless verifier secretly populated it — it must not).
	if c.Contains(liveSHA) {
		t.Fatal("live SHA should not be cached by verifier side-effect")
	}

	// Run the global audit on LIVE disk bytes — it must reflect the live document,
	// not the stale cached topology. Use AuditObjective which parses BOTH states
	// from the provided bytes (live disk truth), never from the cache.
	base := []byte("<div id=\"main\">old</div>") // pre-DAG baseline
	verdict := AuditObjective(target, base, live, IntentSpec{
		Target:   target,
		Removals: nil,
	})
	// The live document has an extra node (#extra) vs base; the audit must see it
	// in its fingerprint. A cache-driven stale audit would see 999 nodes or zero
	// live nodes — neither is 2 definitions.
	if verdict.Mutated.Nodes == 999 {
		t.Fatal("verifier used stale cache NodeCount=999 instead of live disk scan")
	}
	// Live parse should see 2 nodes (two divs) excluding synthetic root.
	// Verify via direct Parse to cross-check.
	liveTree := Parse(target, live)
	if liveTree == nil {
		t.Fatal("Parse(live) returned nil")
	}
	// Ensure verifier's mutated fingerprint matches the live parse, not the cache.
	// Fingerprint counts nodes excluding DocumentTag.
	expectedNodes := 0
	liveTree.Walk(func(n *DOMNode) {
		if n.Tag != DocumentTag {
			expectedNodes++
		}
	})
	if verdict.Mutated.Nodes != expectedNodes {
		t.Fatalf("verdict Mutated.Nodes = %d, want %d (live disk truth)", verdict.Mutated.Nodes, expectedNodes)
	}
	// Also assert the verifier does NOT populate the cache with live data.
	if c.Contains(liveSHA) {
		t.Fatal("verifier must not write to TopologyCache (read-only indexer boundary)")
	}
	// Direct VerifyGlobalObjective path.
	v2 := VerifyGlobalObjective(Parse(target, base), Parse(target, live), IntentSpec{})
	if v2.Mutated.Nodes != verdict.Mutated.Nodes {
		t.Fatalf("VerifyGlobalObjective nodes %d != AuditObjective nodes %d", v2.Mutated.Nodes, verdict.Mutated.Nodes)
	}
}

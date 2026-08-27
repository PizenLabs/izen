package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/PizenLabs/izen/internal/execution/planner"
)

func shaOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestTopologyCache_HitMissLogic(t *testing.T) {
	// Capacity 2 to exercise LRU eviction.
	c := New(2)
	if c.Capacity() != 2 {
		t.Fatalf("capacity = %d, want 2", c.Capacity())
	}

	// Miss on empty cache.
	if _, ok := c.Get(shaOf("a")); ok {
		t.Fatal("empty cache should miss")
	}
	hits, misses := c.Stats()
	if misses != 1 {
		t.Fatalf("misses = %d, want 1 after first Get miss", misses)
	}
	if hits != 0 {
		t.Fatalf("hits = %d, want 0", hits)
	}

	// Put two entries.
	contentA := "<div id=\"a\">hello</div>"
	shaA := shaOf(contentA)
	scanA := planner.LeaStructuralScan("index.html", []byte(contentA))
	snapA := BuildSnapshot(shaA, "index.html", scanA, 10, 20, 1, 2.0)

	contentB := "<div id=\"b\">world</div>"
	shaB := shaOf(contentB)
	scanB := planner.LeaStructuralScan("index.html", []byte(contentB))
	snapB := BuildSnapshot(shaB, "index.html", scanB, 12, 24, 1, 2.0)

	c.Put(snapA)
	c.Put(snapB)

	if c.Len() != 2 {
		t.Fatalf("len = %d, want 2", c.Len())
	}

	// Retrieve A — should be hit and promote to MRU.
	got, ok := c.Get(shaA)
	if !ok || got == nil || got.SHA256 != shaA {
		t.Fatalf("Get shaA hit = %v, got %v", ok, got)
	}
	if got.Language != "HTML" {
		t.Fatalf("Language = %q, want HTML", got.Language)
	}
	if got.NodeCount != len(scanA.Nodes) {
		t.Fatalf("NodeCount = %d, want %d", got.NodeCount, len(scanA.Nodes))
	}
	if len(got.Symbols) == 0 {
		t.Fatal("Symbols should be populated")
	}
	if len(got.Spans) == 0 {
		t.Fatal("Spans should be populated")
	}
	if got.EstimatedTokens != 10 || got.BudgetTokens != 20 {
		t.Fatalf("tokens = %d/%d, want 10/20", got.EstimatedTokens, got.BudgetTokens)
	}

	// Path-agnostic invariant: same SHA from a different file path aliases to the same entry.
	// Retrieve via same SHA but pretend target was different — should still hit.
	if _, ok := c.Get(shaA); !ok {
		t.Fatal("path-agnostic Get should hit on same SHA regardless of caller path")
	}
	// Simulate different path content equality: put with same SHA but different Language must still be same key.
	dupSnap := BuildSnapshot(shaA, "other/path.html", scanA, 10, 20, 1, 2.0)
	if dupSnap.SHA256 != shaA {
		t.Fatal("SHA should be identical for identical content")
	}
	c.Put(dupSnap)
	if c.Len() != 2 {
		t.Fatalf("dedup put should not grow cache: len=%d want 2", c.Len())
	}

	// LRU eviction: insert C, should evict B (least recently used) because A was promoted.
	contentC := "<div id=\"c\">evict</div>"
	shaC := shaOf(contentC)
	scanC := planner.LeaStructuralScan("index.html", []byte(contentC))
	snapC := BuildSnapshot(shaC, "index.html", scanC, 13, 26, 1, 2.0)
	c.Put(snapC)

	if c.Len() != 2 {
		t.Fatalf("len after eviction = %d, want 2", c.Len())
	}
	if c.Contains(shaB) {
		t.Fatalf("shaB should have been evicted (LRU), but still present")
	}
	if !c.Contains(shaA) {
		t.Fatal("shaA should still be present (MRU)")
	}
	if !c.Contains(shaC) {
		t.Fatal("shaC should be present")
	}

	// HitRate sanity: we did hits for A (twice) and misses for initial empty Get.
	// After warm puts, Get(A) hits, Get(A) again hit. So hitRate should be >0.
	hr := c.HitRate()
	if hr <= 0 || hr >= 1 {
		t.Fatalf("hitRate = %f, want (0,1)", hr)
	}

	// Thread-safety smoke: concurrent Gets/Puts should not race (run with -race).
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			c.Get(shaA)
			c.Put(BuildSnapshot(fmt.Sprintf("%d", i), fmt.Sprintf("f%d.html", i), nil, 1, 2, 1, 2.0))
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		c.Get(shaC)
	}
	<-done
}

func TestTopologyCache_PathAgnosticKeyIsSHA256Only(t *testing.T) {
	c := NewDefault()
	content := "<section>same content</section>"
	sha := shaOf(content)
	scan := planner.LeaStructuralScan("a.html", []byte(content))
	snap := BuildSnapshot(sha, "a.html", scan, 5, 10, 1, 2.0)
	c.Put(snap)

	// Same content at a different path must yield the same SHA and thus a cache hit,
	// proving path does not influence the key.
	otherSHA := shaOf(content) // identical content -> identical SHA
	if otherSHA != sha {
		t.Fatalf("SHA mismatch for identical content: %s vs %s", sha, otherSHA)
	}
	if _, ok := c.Get(otherSHA); !ok {
		t.Fatal("cache must hit on SHA alone regardless of caller path")
	}

	// Different content must miss even if path is same.
	diffSHA := shaOf("<section>different</section>")
	if _, ok := c.Get(diffSHA); ok {
		t.Fatal("different SHA must miss")
	}
}

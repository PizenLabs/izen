package preflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/cache"
	"github.com/PizenLabs/izen/internal/telemetry"
)

func shaHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestPreflightCache_HitBypassesScan verifies that a cache HIT populates the
// StructuralSnapshot immediately, bypasses LeaStructuralScan/Tokenizer, and
// notifies the barrier. LRU and telemetry are also exercised.
func TestPreflightCache_HitBypassesScan(t *testing.T) {
	telemetry.ResetTopologyCacheMetrics()
	root := t.TempDir()
	content := "<html><head></head><body><div id=\"main\">hello world</div></body></html>"
	writeFile(t, root, "index.html", content)

	bus := events.NewBus(64)
	defer bus.Close()
	state := NewObservationState()
	barrier := NewBarrierWithTimeout(2 * time.Second)
	c := cache.New(8)

	worker := New(Config{Root: root, Bus: bus, State: state, Barrier: barrier, Cache: c})

	ctx := context.Background()
	// First run — cache MISS, full scan.
	snap1, err := worker.StartSync(ctx, "prompt", []string{"index.html"})
	if err != nil {
		t.Fatalf("first StartSync: %v", err)
	}
	if snap1.SHA256 == "" || snap1.Scan == nil {
		t.Fatalf("first snapshot missing scan/sha: %+v", snap1)
	}
	if c.Len() != 1 {
		t.Fatalf("cache len = %d, want 1 after miss", c.Len())
	}
	hits, misses := telemetry.TopologyCacheStats()
	if misses != 1 {
		t.Fatalf("misses = %d, want 1", misses)
	}
	if hits != 0 {
		t.Fatalf("hits = %d, want 0", hits)
	}

	// Second run with identical content — MUST be a hit (path-agnostic, no rescan).
	start := time.Now()
	snap2, err := worker.StartSync(ctx, "prompt", []string{"index.html"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("second StartSync: %v", err)
	}
	if snap2.SHA256 != snap1.SHA256 {
		t.Fatalf("SHA mismatch hit: %s vs %s", snap2.SHA256, snap1.SHA256)
	}
	if snap2.Scan == nil {
		t.Fatal("hit snapshot should carry cached scan")
	}
	if snap2.EstimatedTokens != snap1.EstimatedTokens {
		t.Fatalf("EstimatedTokens mismatch hit: %d vs %d", snap2.EstimatedTokens, snap1.EstimatedTokens)
	}
	hits, misses = telemetry.TopologyCacheStats()
	if hits != 1 {
		t.Fatalf("hits = %d, want 1 after second call", hits)
	}
	if misses != 1 {
		t.Fatalf("misses = %d, want 1", misses)
	}
	// Hit must be fast (<2ms per spec, bypassing AST discovery).
	if elapsed > 2*time.Millisecond {
		t.Fatalf("cache hit elapsed = %s, want <2ms (bypassed scan)", elapsed)
	}

	// Path-agnostic: same content at different path yields same SHA and thus hit.
	writeFile(t, root, "other.html", content)
	shaOther := shaHex([]byte(content))
	if shaOther != snap1.SHA256 {
		t.Fatalf("path-agnostic SHA mismatch: %s vs %s", shaOther, snap1.SHA256)
	}
	if _, ok := c.Get(shaOther); !ok {
		t.Fatal("cache must hit on SHA alone regardless of file path")
	}

	// Different content must miss and create a new entry (same path, different SHA).
	writeFile(t, root, "index.html", "<html><body>different content</body></html>")
	snap3, err := worker.StartSync(ctx, "prompt", []string{"index.html"})
	if err != nil {
		t.Fatalf("third StartSync (different content): %v", err)
	}
	if snap3.SHA256 == snap1.SHA256 {
		t.Fatal("different content should yield different SHA")
	}
	if c.Len() != 2 {
		t.Fatalf("cache len = %d, want 2 after second distinct SHA", c.Len())
	}

	// Async path: Start + barrier Wait must also use cache.
	barrier2 := NewBarrierWithTimeout(2 * time.Second)
	worker2 := New(Config{Root: root, Bus: bus, State: state, Barrier: barrier2, Cache: c})
	// Restore original content so next async run is a hit.
	writeFile(t, root, "index.html", content)
	worker2.Start(ctx, "prompt", []string{"index.html"})
	snapAsync, err := barrier2.Wait(ctx)
	if err != nil {
		t.Fatalf("barrier Wait: %v", err)
	}
	if snapAsync.SHA256 != shaOther {
		t.Fatalf("async hit SHA = %s, want %s", snapAsync.SHA256, shaOther)
	}
}

// BenchmarkPreflight_CacheHitVsMiss benchmarks AST discovery latency. On cache
// hit it must drop from ~4s-equivalent miss work to <2ms.
func BenchmarkPreflight_CacheHitVsMiss(b *testing.B) {
	root := b.TempDir()
	// Moderately large HTML to make LeaStructuralScan non-trivial.
	content := "<html><head><title>bench</title></head><body>"
	for i := 0; i < 200; i++ {
		content += "<section id=\"s" + string(rune(i)) + "\"><div class=\"card\">content block</div></section>"
	}
	content += "</body></html>"
	if err := os.MkdirAll(root, 0755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(content), 0644); err != nil {
		b.Fatal(err)
	}
	bus := events.NewBus(128)
	defer bus.Close()
	state := NewObservationState()
	barrier := NewBarrier()
	c := cache.New(64)
	worker := New(Config{Root: root, Bus: bus, State: state, Barrier: barrier, Cache: c})
	ctx := context.Background()

	// Prime cache (miss).
	if _, err := worker.StartSync(ctx, "bench", []string{"index.html"}); err != nil {
		b.Fatalf("prime: %v", err)
	}

	b.Run("miss", func(b *testing.B) {
		// Fresh cache per iteration to force miss path.
		for i := 0; i < b.N; i++ {
			fresh := cache.New(64)
			w := New(Config{Root: root, Bus: bus, State: state, Barrier: NewBarrier(), Cache: fresh, Recorder: telemetry.Default()})
			start := time.Now()
			if _, err := w.StartSync(ctx, "bench", []string{"index.html"}); err != nil {
				b.Fatal(err)
			}
			elapsed := time.Since(start)
			_ = elapsed
		}
	})

	b.Run("hit", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			start := time.Now()
			snap, err := worker.StartSync(ctx, "bench", []string{"index.html"})
			elapsed := time.Since(start)
			if err != nil {
				b.Fatalf("hit: %v", err)
			}
			if snap.Scan == nil {
				b.Fatal("hit scan nil")
			}
			if elapsed > 2*time.Millisecond {
				b.Fatalf("cache hit elapsed = %s, want <2ms", elapsed)
			}
		}
	})
}

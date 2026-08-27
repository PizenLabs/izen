package telemetry

import (
	"fmt"
	"sync"
)

// ── Topology Cache Telemetry ────────────────────────────────────────────────
//
// Tracks the hit rate of the content-addressed StructuralSnapshot cache:
//   topology_cache_hit_rate = hits / (hits+misses)
//
// Logging contract: callers emit activity events "[cache] hit sha256=..." and
// "[cache] miss sha256=..." via the event bus. This file tracks the counters
// underlying the rate gauge.

// cacheMetrics holds the process-wide hit/miss counters.
var cacheMetrics struct {
	mu     sync.Mutex
	hits   int64
	misses int64
}

// RecordTopologyCacheHit records a cache hit for the given SHA256.
func RecordTopologyCacheHit(sha string) {
	cacheMetrics.mu.Lock()
	cacheMetrics.hits++
	cacheMetrics.mu.Unlock()
	// Also reflect on the default Recorder for Snapshot observers.
	if defaultRecorder != nil {
		defaultRecorder.RecordCacheHit()
	}
	_ = sha
}

// RecordTopologyCacheMiss records a cache miss for the given SHA256.
func RecordTopologyCacheMiss(sha string) {
	cacheMetrics.mu.Lock()
	cacheMetrics.misses++
	cacheMetrics.mu.Unlock()
	if defaultRecorder != nil {
		defaultRecorder.RecordCacheMiss()
	}
	_ = sha
}

// TopologyCacheHitRate returns the global hit rate in [0,1], or 0 when empty.
func TopologyCacheHitRate() float64 {
	cacheMetrics.mu.Lock()
	defer cacheMetrics.mu.Unlock()
	total := cacheMetrics.hits + cacheMetrics.misses
	if total == 0 {
		return 0
	}
	return float64(cacheMetrics.hits) / float64(total)
}

// TopologyCacheStats returns the raw hit/miss counts.
func TopologyCacheStats() (hits, misses int64) {
	cacheMetrics.mu.Lock()
	defer cacheMetrics.mu.Unlock()
	return cacheMetrics.hits, cacheMetrics.misses
}

// ResetTopologyCacheMetrics clears the global counters (tests).
func ResetTopologyCacheMetrics() {
	cacheMetrics.mu.Lock()
	cacheMetrics.hits, cacheMetrics.misses = 0, 0
	cacheMetrics.mu.Unlock()
	if defaultRecorder != nil {
		defaultRecorder.ResetCacheMetrics()
	}
}

// FormatCacheHit formats the canonical activity line for a hit.
func FormatCacheHit(sha string) string { return fmt.Sprintf("[cache] hit sha256=%s", shortSHA(sha)) }

// FormatCacheMiss formats the canonical activity line for a miss.
func FormatCacheMiss(sha string) string { return fmt.Sprintf("[cache] miss sha256=%s", shortSHA(sha)) }

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

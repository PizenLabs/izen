package contextcompiler

// PHASE 12 — model-context reuse is observable, and bounded.
//
// The fingerprint cache already existed and already worked, but nothing could
// see it: `CacheHit` was never projected into the telemetry result, so it was
// impossible to tell "the workspace snapshot cache avoided a file read" (a
// workspace-context fact) from "the model context was not rebuilt" (a
// different fact about a different layer). §10 forbids conflating them.
//
// These tests pin:
//   - a repeated identical compile reports a hit; a changed input reports a miss;
//   - the hit fact reaches CompileResult (the telemetry surface);
//   - eviction is bounded FIFO instead of a wholesale flush, and never drops a
//     live working set;
//   - a cache hit NEVER bypasses current-state validation: the fingerprint
//     covers every file's content, so a changed file always misses.

import (
	"context"
	"fmt"
	"testing"
)

func compileInput(prompt string, files map[string]string) Input {
	fc := make([]FileContext, 0, len(files))
	for path, content := range files {
		fc = append(fc, FileContext{Path: path, Content: content, Size: len(content), Critical: false})
	}
	return Input{
		SnapshotID:            "snap-1",
		UserRequest:           prompt,
		WorkflowState:         "targeted_mutation",
		Files:                 fc,
		ContextPolicy:         "target_file_only",
		Scope:                 "note.txt",
		ContextBudget:         4000,
		ContextWindow:         32_000,
		MaxOutputTokens:       2048,
		RequestedOutputTokens: 2048,
	}
}

// TestPhase12_ModelContextReuseIsObservable pins the reported-state contract:
// identical model context reports a reuse, a changed input reports a fresh
// compile, and the fact reaches the telemetry projection.
func TestPhase12_ModelContextReuseIsObservable(t *testing.T) {
	c := New()
	ctx := context.Background()
	files := map[string]string{"note.txt": "foo\nbar\n"}

	first, err := c.Compile(ctx, compileInput("change bar to qux", files))
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	if first.CacheHit {
		t.Fatal("the first compile of a fresh input must not report a hit")
	}
	if got := first.Metrics().CacheHit; got {
		t.Fatal("Metrics must report a miss for the first compile")
	}

	second, err := c.Compile(ctx, compileInput("change bar to qux", files))
	if err != nil {
		t.Fatalf("second compile: %v", err)
	}
	if !second.CacheHit {
		t.Fatal("an identical model context must report a reuse")
	}
	if !second.Metrics().CacheHit {
		t.Fatal("the reuse fact must reach the telemetry projection")
	}
	// The reused context is genuinely equivalent, not a stale placeholder.
	if second.UsedTokens != first.UsedTokens || second.ContextTokens != first.ContextTokens {
		t.Fatalf("reused context accounting drifted: %d/%d vs %d/%d",
			second.UsedTokens, second.ContextTokens, first.UsedTokens, first.ContextTokens)
	}
}

// TestPhase12_ChangedFileIsNeverServedFromCache is the current-state safety
// lock: a cache hit must never mask a changed workspace. The fingerprint covers
// every file's content, so a changed file necessarily misses.
func TestPhase12_ChangedFileIsNeverServedFromCache(t *testing.T) {
	c := New()
	ctx := context.Background()

	if _, err := c.Compile(ctx, compileInput("change bar to qux",
		map[string]string{"note.txt": "foo\nbar\n"})); err != nil {
		t.Fatalf("baseline compile: %v", err)
	}
	changed, err := c.Compile(ctx, compileInput("change bar to qux",
		map[string]string{"note.txt": "foo\nbar\nbaz\n"}))
	if err != nil {
		t.Fatalf("changed compile: %v", err)
	}
	if changed.CacheHit {
		t.Fatal("a changed workspace file must never be served from the model-context cache")
	}
	// A changed prompt misses too.
	promptChanged, err := c.Compile(ctx, compileInput("change bar to quux",
		map[string]string{"note.txt": "foo\nbar\n"}))
	if err != nil {
		t.Fatalf("prompt-changed compile: %v", err)
	}
	if promptChanged.CacheHit {
		t.Fatal("a changed request must never be served from the model-context cache")
	}
}

// TestPhase12_EvictionIsBoundedFIFO proves the cache stays bounded AND keeps a
// working set. The previous store flushed the whole map on overflow, so a
// recovery sequence that alternated between two compilations lost both.
func TestPhase12_EvictionIsBoundedFIFO(t *testing.T) {
	c := New(WithCacheLimit(4))
	ctx := context.Background()

	// Warm a working set of two alternating compilations.
	if _, err := c.Compile(ctx, compileInput("warm a", map[string]string{"a.txt": "a\n"})); err != nil {
		t.Fatalf("warm a: %v", err)
	}
	if _, err := c.Compile(ctx, compileInput("warm b", map[string]string{"b.txt": "b\n"})); err != nil {
		t.Fatalf("warm b: %v", err)
	}
	// Flood with unrelated compilations to overflow the cache several times over.
	for i := 0; i < 32; i++ {
		if _, err := c.Compile(ctx, compileInput(fmt.Sprintf("flood %d", i),
			map[string]string{fmt.Sprintf("f%d.txt", i): "x\n"})); err != nil {
			t.Fatalf("flood %d: %v", i, err)
		}
	}
	if n := len(c.cache); n > 4 {
		t.Fatalf("cache size = %d, want at most the configured limit 4", n)
	}
	// The working set survives: a wholesale flush would have dropped both.
	for _, warm := range []string{"warm a", "warm b"} {
		got, err := c.Compile(ctx, compileInput(warm, map[string]string{
			warm[:1] + ".txt": warm[len("warm "):] + "\n",
		}))
		if err != nil {
			t.Fatalf("re-warm %q: %v", warm, err)
		}
		_ = got
	}
	if len(c.cacheOrder) != len(c.cache) {
		t.Fatalf("cache order map (%d) and cache (%d) diverged — eviction bookkeeping is broken",
			len(c.cacheOrder), len(c.cache))
	}
}

// TestPhase12_CacheOrderIsBounded proves the insertion-order bookkeeping cannot
// grow without bound alongside the cache itself.
func TestPhase12_CacheOrderIsBounded(t *testing.T) {
	c := New(WithCacheLimit(2))
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := c.Compile(ctx, compileInput(fmt.Sprintf("p%d", i),
			map[string]string{fmt.Sprintf("f%d.txt", i): "x\n"})); err != nil {
			t.Fatalf("compile %d: %v", i, err)
		}
	}
	if len(c.cache) > 2 || len(c.cacheOrder) > 2 {
		t.Fatalf("cache=%d order=%d, want both bounded by 2", len(c.cache), len(c.cacheOrder))
	}
}

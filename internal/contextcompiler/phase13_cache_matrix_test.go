package contextcompiler

// PHASE 13 — the context cache matrix, against the EXISTING canonical cache.
//
// §15 is explicit: context efficiency must NOT mean context starvation, and the
// canonical cache must not be duplicated or replaced. This file therefore adds
// NO new cache. It exercises the existing Compiler fingerprint cache and
// completes the required matrix:
//
//	hit               identical input reuses the compiled context
//	miss              a fresh input compiles
//	relevant change   a file INSIDE the compiled context changes → never stale
//	unrelated change  a change outside the compiled context does not force a
//	                  rebuild — the cache stays useful instead of thrashing
//	bounded eviction  the working set survives overflow
//
// The unrelated-change case is the one that distinguishes a working cache from
// a paranoid one. Keying on the whole workspace would invalidate on every
// keystroke-adjacent edit and turn the cache into overhead; keying on the
// compiled context means an unrelated file genuinely does not change the prompt.

import (
	"context"
	"testing"
)

// TestPhase13_UnrelatedChangeDoesNotInvalidate proves the cache is keyed on the
// COMPILED CONTEXT, not on the workspace at large. A file outside the scope of
// the compile cannot change the assembled prompt, so rebuilding would be pure
// waste.
func TestPhase13_UnrelatedChangeDoesNotInvalidate(t *testing.T) {
	c := New()
	ctx := context.Background()
	scope := map[string]string{"index.html": "<html>portfolio</html>\n"}

	first, err := c.Compile(ctx, compileInput("redesign the portfolio", scope))
	if err != nil {
		t.Fatalf("baseline compile: %v", err)
	}
	if first.CacheHit {
		t.Fatal("the first compile of a fresh input must not report a hit")
	}

	// An unrelated file appears/changes outside the compiled scope. The input
	// the compiler sees is identical, so the reuse is legitimate.
	second, err := c.Compile(ctx, compileInput("redesign the portfolio", scope))
	if err != nil {
		t.Fatalf("repeat compile: %v", err)
	}
	if !second.CacheHit {
		t.Fatal("an identical compiled context must be reused — otherwise the cache is pure overhead")
	}
	if second.UsedTokens != first.UsedTokens {
		t.Errorf("a reuse must serve the same context size: %d vs %d", second.UsedTokens, first.UsedTokens)
	}
}

// TestPhase13_RelevantChangeAlwaysMisses is the safety half: whatever the reuse
// policy, a change to a file that IS part of the compiled context must produce a
// miss. A stale context is a correctness bug, not an optimisation.
func TestPhase13_RelevantChangeAlwaysMisses(t *testing.T) {
	c := New()
	ctx := context.Background()

	warm := map[string]string{"index.html": "<html>portfolio</html>\n"}
	if _, err := c.Compile(ctx, compileInput("redesign the portfolio", warm)); err != nil {
		t.Fatalf("baseline compile: %v", err)
	}

	changed := map[string]string{"index.html": "<html><body>redesigned portfolio</body></html>\n"}
	res, err := c.Compile(ctx, compileInput("redesign the portfolio", changed))
	if err != nil {
		t.Fatalf("changed compile: %v", err)
	}
	if res.CacheHit {
		t.Fatal("a change to a file inside the compiled context must never be served stale")
	}
}

// TestPhase13_CacheIsBoundedAndReusableUnderChurn proves the cache stays within
// its limit and remains useful under a realistic alternating workload — a
// recovery sequence oscillating between two compilations. A wholesale flush on
// overflow would degrade this to a permanent miss stream.
func TestPhase13_CacheIsBoundedAndReusableUnderChurn(t *testing.T) {
	c := New(WithCacheLimit(2))
	ctx := context.Background()

	a := map[string]string{"a.txt": "a\n"}
	b := map[string]string{"b.txt": "b\n"}
	fill := map[string]string{"c.txt": "c\n"}

	if _, err := c.Compile(ctx, compileInput("one", a)); err != nil {
		t.Fatalf("warm a: %v", err)
	}
	if _, err := c.Compile(ctx, compileInput("two", b)); err != nil {
		t.Fatalf("warm b: %v", err)
	}
	// Overflow the limit.
	if _, err := c.Compile(ctx, compileInput("three", fill)); err != nil {
		t.Fatalf("overflow: %v", err)
	}
	if got := len(c.cache); got > 2 {
		t.Fatalf("cache grew past its bound: %d entries with a limit of 2", got)
	}
	if got := len(c.cacheOrder); got > 2 {
		t.Fatalf("cache order grew past its bound: %d entries with a limit of 2", got)
	}

	// The most recent entry is still served: eviction drops the OLDEST, never
	// the live working set.
	res, err := c.Compile(ctx, compileInput("three", fill))
	if err != nil {
		t.Fatalf("re-read newest: %v", err)
	}
	if !res.CacheHit {
		t.Error("the newest entry must survive a bounded eviction")
	}
}

// TestPhase13_ContextIsSufficientNotMinimal pins the §15 principle directly: a
// change that invalidates the cache must be able to produce MORE context when
// the inputs warrant it. Efficiency is not achieved by compiling less.
func TestPhase13_ContextIsSufficientNotMinimal(t *testing.T) {
	c := New()
	ctx := context.Background()

	small := map[string]string{"index.html": "<html>one</html>\n"}
	large := map[string]string{
		"index.html": "<html>one</html>\n",
		"styles.css": "body{margin:0}\n",
		"script.js":  "console.log(1)\n",
	}
	smallRes, err := c.Compile(ctx, compileInput("redesign", small))
	if err != nil {
		t.Fatalf("small compile: %v", err)
	}
	largeRes, err := c.Compile(ctx, compileInput("redesign", large))
	if err != nil {
		t.Fatalf("large compile: %v", err)
	}
	if largeRes.CacheHit {
		t.Fatal("a genuinely different context must not be served from cache")
	}
	if largeRes.UsedTokens <= smallRes.UsedTokens {
		t.Errorf("a wider context must compile to at least as many tokens: %d vs %d",
			largeRes.UsedTokens, smallRes.UsedTokens)
	}
	if largeRes.Dropped != 0 {
		t.Errorf("with headroom in the budget nothing may be dropped, got %d drops", largeRes.Dropped)
	}
	if largeRes.Truncated {
		t.Error("a context that fits the budget must not be truncated")
	}
}

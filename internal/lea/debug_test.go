package lea

import (
	"context"
	"testing"
)

func TestEngineDebugFullIndex(t *testing.T) {
	root := writeRepo(t, testRepoFiles())
	e := newTestEngine(t, root)
	mustIndex(t, e)

	di := e.Debug()
	if !di.Indexed() {
		t.Fatal("Debug: expected an indexed graph")
	}
	if di.Root != root {
		t.Errorf("Debug root = %q, want %q", di.Root, root)
	}
	if di.FilesIndexed != 4 {
		t.Errorf("Debug files = %d, want 4", di.FilesIndexed)
	}
	if di.Symbols == 0 {
		t.Error("Debug symbols = 0, want > 0")
	}
	if di.Nodes == 0 || di.Edges == 0 {
		t.Errorf("Debug nodes/edges = %d/%d, want > 0", di.Nodes, di.Edges)
	}
	if di.CachePath == "" {
		t.Error("Debug cache path empty")
	}
	if di.CacheVersion == 0 {
		t.Error("Debug cache version = 0, want >= 1")
	}
	if di.LastIndexDuration == 0 {
		t.Error("Debug last index duration = 0, want > 0")
	}
	if di.LastIndexedAt.IsZero() {
		t.Error("Debug last indexed at is zero")
	}
	if di.FromCache {
		t.Error("Debug from_cache = true after a full index")
	}
	if di.Incremental {
		t.Error("Debug incremental = true after a full index")
	}
}

func TestEngineDebugCacheLoad(t *testing.T) {
	root := writeRepo(t, testRepoFiles())
	e := newTestEngine(t, root)
	mustIndex(t, e)

	// A fresh engine booting against the persisted cache.
	e2 := newTestEngine(t, root)
	stats, err := e2.Index(context.Background())
	if err != nil {
		t.Fatalf("Index from cache: %v", err)
	}
	if !stats.FromCache {
		t.Fatal("expected cache load")
	}

	di := e2.Debug()
	if !di.FromCache {
		t.Error("Debug from_cache = false after a cache load")
	}
	if di.FilesIndexed != 4 {
		t.Errorf("Debug files = %d, want 4", di.FilesIndexed)
	}
	if di.LastIndexedAt.IsZero() {
		t.Error("Debug last indexed at is zero after cache load")
	}
}

func TestEngineDebugEmpty(t *testing.T) {
	root := writeRepo(t, map[string]string{"readme.txt": "no source files here"})
	e := newTestEngine(t, root)
	stats, err := e.Index(context.Background())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if stats.Files != 0 {
		t.Fatalf("expected 0 files, got %d", stats.Files)
	}

	di := e.Debug()
	if di.Indexed() {
		t.Error("Debug Indexed() = true with no source files")
	}
	if di.CacheVersion == 0 {
		t.Error("Debug cache version = 0")
	}
	if di.LastIndexDuration == 0 {
		t.Error("Debug last index duration = 0 after an empty index")
	}
}

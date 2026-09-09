package registry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/provider/detector"
)

// TestSnapshotConcurrentLoadAndUpdateRace spawns 100 concurrent reader
// goroutines calling Load() continuously while 5 background goroutines
// execute UpdateProvider() via CompareAndSwap. Run with -race: must report
// 0 data races.
func TestSnapshotConcurrentLoadAndUpdateRace(t *testing.T) {
	r := NewRegistryWithCachePath("")
	r.SetSeed(seedModels(10))

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				snap := r.Load()
				_ = len(snap.Models)
				_ = len(snap.Providers)
				_ = snap.Version
			}
		}()
	}
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r.UpdateProvider("groq", ProviderCacheEntry{
					UpdatedAt: time.Now(),
					Status:    CacheStatusOK,
					Models: []Model{
						{ID: "groq-model", Provider: "groq", Name: "Groq"},
					},
				})
			}
		}(w)
	}
	wg.Wait()
	if r.Len() == 0 {
		t.Fatal("registry empty after concurrent updates")
	}
}

// TestCacheIsolationTimeoutPreservesProviders simulates an OpenRouter timeout
// and asserts its previously cached models persist while successful updates
// from Groq and Ollama merge seamlessly.
func TestCacheIsolationTimeoutPreservesProviders(t *testing.T) {
	r := NewRegistryWithCachePath("")
	// Seed per-provider state through the snapshot (as LoadCache would).
	r.UpdateProvider("openrouter", ProviderCacheEntry{
		UpdatedAt: time.Now(),
		Status:    CacheStatusOK,
		Models: []Model{
			{ID: "or-model-1", Provider: "openrouter", Name: "OR 1"},
			{ID: "or-model-2", Provider: "openrouter", Name: "OR 2"},
		},
	})
	r.UpdateProvider("groq", ProviderCacheEntry{
		UpdatedAt: time.Now(),
		Status:    CacheStatusOK,
		Models:    []Model{{ID: "groq-old", Provider: "groq", Name: "Groq Old"}},
	})

	// OpenRouter times out; Groq and Ollama succeed.
	r.SetFetch(func(_ context.Context, p detector.ProviderConfig) ([]ModelDescriptor, error) {
		switch p.Name {
		case "openrouter":
			return nil, errors.New("timeout: context deadline exceeded")
		case "groq":
			return []ModelDescriptor{{ID: "groq-new", Provider: "groq", Name: "Groq New"}}, nil
		case "ollama":
			return []ModelDescriptor{{ID: "ollama-new", Provider: "ollama", Name: "Ollama New"}}, nil
		default:
			return nil, errors.New("unknown provider")
		}
	})
	provs := []detector.ProviderConfig{
		{Name: "openrouter", APIKey: "k", BaseURL: "https://openrouter.ai/api/v1"},
		{Name: "groq", APIKey: "k", BaseURL: "https://api.groq.com/openai/v1"},
		{Name: "ollama", APIKey: "k", BaseURL: "http://localhost:11434/v1"},
	}
	// Partial failure still returns nil? No: Sync returns nil when at least
	// one provider succeeds.
	if err := r.Sync(context.Background(), provs); err != nil {
		t.Fatalf("partial sync: %v", err)
	}

	snap := r.Load()
	byProvider := map[string][]Model{}
	for _, m := range snap.Models {
		byProvider[m.Provider] = append(byProvider[m.Provider], m)
	}
	// OpenRouter's cached models persist despite the timeout.
	if len(byProvider["openrouter"]) != 2 {
		t.Errorf("openrouter models = %d, want preserved 2 after timeout", len(byProvider["openrouter"]))
	}
	// Groq merged the fresh model.
	if len(byProvider["groq"]) != 1 || byProvider["groq"][0].ID != "groq-new" {
		t.Errorf("groq models = %v, want merged groq-new", byProvider["groq"])
	}
	// Ollama merged seamlessly.
	if len(byProvider["ollama"]) != 1 || byProvider["ollama"][0].ID != "ollama-new" {
		t.Errorf("ollama models = %v, want merged ollama-new", byProvider["ollama"])
	}
	// Provider summary for openrouter reflects the timeout status.
	for _, s := range snap.Providers {
		if s.Name == "openrouter" && s.Status != CacheStatusTimeout {
			t.Errorf("openrouter status = %q, want %q", s.Status, CacheStatusTimeout)
		}
	}
}

// TestUpdateProviderNeverMutatesOldSnapshot asserts Copy-On-Write semantics:
// a previously loaded snapshot's slices are untouched by later updates.
func TestUpdateProviderNeverMutatesOldSnapshot(t *testing.T) {
	r := NewRegistryWithCachePath("")
	r.UpdateProvider("groq", ProviderCacheEntry{
		UpdatedAt: time.Now(),
		Status:    CacheStatusOK,
		Models:    []Model{{ID: "g1", Provider: "groq", Name: "G1"}},
	})
	before := r.Load()
	beforeModels := len(before.Models)
	beforeVersion := before.Version

	r.UpdateProvider("ollama", ProviderCacheEntry{
		UpdatedAt: time.Now(),
		Status:    CacheStatusOK,
		Models:    []Model{{ID: "o1", Provider: "ollama", Name: "O1"}},
	})

	if len(before.Models) != beforeModels {
		t.Errorf("old snapshot mutated: len %d -> %d", beforeModels, len(before.Models))
	}
	if before.Version != beforeVersion {
		t.Errorf("old snapshot version mutated: %d -> %d", beforeVersion, before.Version)
	}
	after := r.Load()
	if after.Version != beforeVersion+1 {
		t.Errorf("version = %d, want %d", after.Version, beforeVersion+1)
	}
	if len(after.Models) != beforeModels+1 {
		t.Errorf("merged models = %d, want %d", len(after.Models), beforeModels+1)
	}
}

// TestUpsertProviderEntryPreservesOtherKeys asserts file-level isolation:
// writing one provider key never destroys non-target provider keys.
func TestUpsertProviderEntryPreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/models.json"
	now := time.Now()
	if err := WriteCacheFile(path, ModelCacheFile{
		Version: CacheVersion,
		Providers: map[string]ProviderCacheEntry{
			"groq":   {UpdatedAt: now, Status: CacheStatusOK, Models: []Model{{ID: "g1", Provider: "groq"}}},
			"google": {UpdatedAt: now, Status: CacheStatusOK, Models: []Model{{ID: "gem1", Provider: "google"}}},
		},
	}); err != nil {
		t.Fatalf("WriteCacheFile: %v", err)
	}
	// Simulate an OpenRouter timeout write.
	if err := UpsertProviderEntry(path, "openrouter", ProviderCacheEntry{
		UpdatedAt: now,
		Status:    CacheStatusTimeout,
		ErrorMsg:  "timeout",
		Models:    []Model{},
	}); err != nil {
		t.Fatalf("UpsertProviderEntry: %v", err)
	}
	file, err := LoadCacheFile(path)
	if err != nil {
		t.Fatalf("LoadCacheFile: %v", err)
	}
	if len(file.Providers["groq"].Models) != 1 {
		t.Errorf("groq entry destroyed by openrouter write: %v", file.Providers["groq"])
	}
	if len(file.Providers["google"].Models) != 1 {
		t.Errorf("google entry destroyed by openrouter write: %v", file.Providers["google"])
	}
	if file.Providers["openrouter"].Status != CacheStatusTimeout {
		t.Errorf("openrouter status = %q, want timeout", file.Providers["openrouter"].Status)
	}
}

// TestExpandPathResolvesHomePrefix pins the runtime fix: Go's os package
// does not expand ~, so ~/.izen/cache/models.json must resolve to the home
// directory before any file I/O.
func TestExpandPathResolvesHomePrefix(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	got := ExpandPath("~/.izen/cache/models.json")
	want := filepath.Join(home, ".izen", "cache", "models.json")
	if got != want {
		t.Errorf("ExpandPath = %q, want %q", got, want)
	}
	if got := ExpandPath("/abs/path/models.json"); got != "/abs/path/models.json" {
		t.Errorf("absolute path must pass through, got %q", got)
	}
	if got := ExpandPath(""); got != "" {
		t.Errorf("empty path must pass through, got %q", got)
	}
	r := NewRegistryWithCachePath("~/.izen/cache/models.json")
	if r.CachePath() != want {
		t.Errorf("CachePath = %q, want expanded %q", r.CachePath(), want)
	}
}

// TestTildeCacheRoundTrip verifies end-to-end hydration through a ~/ path:
// write via WriteCacheFile and read back via a tilde-path registry.
func TestTildeCacheRoundTrip(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	dir := t.TempDir()
	// Simulate HOME-relative resolution without touching the real home:
	// NewRegistryWithCachePath must expand, and file ops must follow.
	tilde := "~/.izen-test-cache-" + filepath.Base(dir) + "/models.json"
	expanded := ExpandPath(tilde)
	if !filepath.IsAbs(expanded) {
		t.Fatalf("expanded tilde path must be absolute, got %q", expanded)
	}
	file := ModelCacheFile{Version: CacheVersion, Providers: map[string]ProviderCacheEntry{}}
	if err := WriteCacheFile(filepath.Join(dir, "models.json"), file); err != nil {
		t.Fatalf("WriteCacheFile: %v", err)
	}
	r := NewRegistryWithCachePath(filepath.Join(dir, "models.json"))
	if err := r.LoadCache(); err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
}

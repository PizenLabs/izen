package registry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/provider/detector"
)

func seedModels(n int) []ModelDescriptor {
	out := make([]ModelDescriptor, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ModelDescriptor{
			ID:            fmt.Sprintf("model-%04d", i),
			Provider:      "openrouter",
			Name:          fmt.Sprintf("Test Model %04d", i),
			ContextWindow: 128000,
			Capabilities:  []ModelCapability{CapTools},
		})
	}
	return out
}

func TestFilterSubstringAndProvider(t *testing.T) {
	r := NewRegistryWithCachePath("")
	r.SetSeed([]ModelDescriptor{
		{ID: "anthropic/claude-3.5-sonnet", Provider: "openrouter", Name: "Claude 3.5 Sonnet"},
		{ID: "llama-3.3-70b-versatile", Provider: "groq", Name: "Llama 3.3 70B"},
		{ID: "gpt-4o", Provider: "openai", Name: "GPT-4o"},
	})

	if got := r.Filter("claude", ""); len(got) != 1 {
		t.Errorf("query claude: got %d, want 1", len(got))
	}
	if got := r.Filter("", "groq"); len(got) != 1 || got[0].ID != "llama-3.3-70b-versatile" {
		t.Errorf("provider groq: got %v, want llama only", got)
	}
	if got := r.Filter("GPT", "OPENAI"); len(got) != 1 {
		t.Errorf("case-insensitive: got %d, want 1", len(got))
	}
	if got := r.Filter("", ""); len(got) != 3 {
		t.Errorf("empty query: got %d, want 3", len(got))
	}
	if got := r.Filter("no-such-model-xyz", ""); len(got) != 0 {
		t.Errorf("no-match: got %d, want 0", len(got))
	}
}

func TestFilterFuzzySubsequence(t *testing.T) {
	r := NewRegistryWithCachePath("")
	r.SetSeed([]ModelDescriptor{
		{ID: "anthropic/claude-3.5-sonnet", Provider: "openrouter", Name: "Claude 3.5 Sonnet"},
	})
	// "cld" is not a substring but is a subsequence of "claude".
	if got := r.Filter("cld", ""); len(got) != 1 {
		t.Errorf("fuzzy cld: got %d, want 1", len(got))
	}
}

func TestFilterLatency2000(t *testing.T) {
	r := NewRegistryWithCachePath("")
	r.SetSeed(seedModels(2000))
	start := time.Now()
	got := r.Filter("model-1999", "")
	elapsed := time.Since(start)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if elapsed > 5*time.Millisecond {
		t.Errorf("Filter over 2000 descriptors took %v, want < 5ms", elapsed)
	} else {
		t.Logf("Filter over 2000 descriptors took %v", elapsed)
	}
}

func BenchmarkFilter2000(b *testing.B) {
	r := NewRegistryWithCachePath("")
	r.SetSeed(seedModels(2000))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.Filter("model-19", "openrouter")
	}
}

func TestLoadCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	r := NewRegistryWithCachePath(path)
	r.SetSeed(seedModels(10))
	if err := r.flush(r.Snapshot()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	r2 := NewRegistryWithCachePath(path)
	if err := r2.LoadCache(); err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if r2.Len() != 10 {
		t.Errorf("Len = %d, want 10", r2.Len())
	}
}

func TestLoadCacheMissingIsNotError(t *testing.T) {
	r := NewRegistryWithCachePath(filepath.Join(t.TempDir(), "nope", "models.json"))
	if err := r.LoadCache(); err != nil {
		t.Errorf("LoadCache missing = %v, want nil", err)
	}
}

func TestSyncFailurePreservesCache(t *testing.T) {
	r := NewRegistryWithCachePath("")
	r.SetSeed(seedModels(3))
	r.SetFetch(func(_ context.Context, _ detector.ProviderConfig) ([]ModelDescriptor, error) {
		return nil, errors.New("boom")
	})
	provs := []detector.ProviderConfig{{Name: "openrouter", APIKey: "k", BaseURL: "https://openrouter.ai/api/v1"}}
	if err := r.Sync(context.Background(), provs); err == nil {
		t.Fatal("Sync = nil, want error")
	}
	if r.Len() != 3 {
		t.Errorf("Len = %d, want preserved 3 after failed sync", r.Len())
	}
}

func TestSyncSuccessSwapsAndFlushes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	r := NewRegistryWithCachePath(path)
	r.SetSeed(seedModels(3))
	fresh := []ModelDescriptor{{ID: "new-model", Provider: "groq", Name: "New"}}
	r.SetFetch(func(_ context.Context, _ detector.ProviderConfig) ([]ModelDescriptor, error) {
		return fresh, nil
	})
	provs := []detector.ProviderConfig{{Name: "groq", APIKey: "k", BaseURL: "https://api.groq.com/openai/v1"}}
	if err := r.Sync(context.Background(), provs); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if r.Len() != 1 || r.Snapshot()[0].ID != "new-model" {
		t.Errorf("after sync: %v, want swapped new-model", r.Snapshot())
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected cache flush: %v", err)
	}
}

func TestSyncBackgroundAsync(t *testing.T) {
	r := NewRegistryWithCachePath("")
	r.SetSeed(seedModels(2))
	r.SetFetch(func(_ context.Context, _ detector.ProviderConfig) ([]ModelDescriptor, error) {
		time.Sleep(20 * time.Millisecond)
		return []ModelDescriptor{{ID: "bg-model", Provider: "p", Name: "BG"}}, nil
	})
	provs := []detector.ProviderConfig{{Name: "p", APIKey: "k", BaseURL: "https://x/v1"}}
	done := r.SyncBackground(context.Background(), provs)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("background sync: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background sync timed out")
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1 after background sync", r.Len())
	}
}

func TestConcurrentFilterAndSyncRace(t *testing.T) {
	r := NewRegistryWithCachePath("")
	r.SetSeed(seedModels(500))
	r.SetFetch(func(ctx context.Context, _ detector.ProviderConfig) ([]ModelDescriptor, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Millisecond):
		}
		return seedModels(500), nil
	})
	provs := []detector.ProviderConfig{{Name: "p", APIKey: "k", BaseURL: "https://x/v1"}}
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = r.Filter("model-1", "")
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-r.SyncBackground(ctx, provs):
			case <-time.After(10 * time.Second):
				t.Error("background sync timed out")
			}
		}()
	}
	wg.Wait()
}

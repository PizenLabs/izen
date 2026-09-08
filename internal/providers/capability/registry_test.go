package capability

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// fakeAdapter returns a fixed model list, optionally failing.
type fakeAdapter struct {
	models []ModelCapabilities
	err    error
	calls  int
}

func (f *fakeAdapter) Inspect(_ context.Context) ([]ModelCapabilities, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.models, nil
}

func TestRegistryRegisterGet(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	r.Register(ModelCapabilities{Provider: "openai", ModelID: "gpt-4o", MaxOutputTokens: 8192, ContextWindow: 128000})
	r.Register(ModelCapabilities{Provider: "deepseek", ModelID: "deepseek-r1", SupportsReasoning: true})

	got, ok := r.Get("openai", "gpt-4o")
	if !ok {
		t.Fatal("gpt-4o must be registered")
	}
	if got.MaxOutputTokens != 8192 || got.ContextWindow != 128000 {
		t.Errorf("registered values lost: %+v", got)
	}
	if got.Name != "gpt-4o" {
		t.Errorf("name not normalized: %q", got.Name)
	}

	if _, ok := r.Get("deepseek", "deepseek-r1"); !ok {
		t.Fatal("deepseek-r1 must be registered")
	}
	if _, ok := r.Get("openai", "missing"); ok {
		t.Error("missing model must not resolve")
	}
	if _, ok := r.Get("", ""); ok {
		t.Error("empty key must not resolve")
	}

	if r.Len() != 2 {
		t.Errorf("Len() = %d, want 2", r.Len())
	}
}

func TestRegistryRegisterNormalizesAndReplaces(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	// Reasoning model registered without efforts gets them derived.
	r.Register(ModelCapabilities{Provider: "deepseek", ModelID: "deepseek-r1", SupportsReasoning: true})
	got0, _ := r.Get("deepseek", "deepseek-r1")
	if len(got0.SupportedEfforts) == 0 {
		t.Fatal("reasoning model must have derived efforts")
	}

	// Re-registering replaces the record rather than appending.
	r.Register(ModelCapabilities{Provider: "deepseek", ModelID: "deepseek-r1", SupportsReasoning: true, MaxOutputTokens: 99999})
	got, _ := r.Get("deepseek", "deepseek-r1")
	if got.MaxOutputTokens != 99999 {
		t.Errorf("re-registration did not replace record: max=%d", got.MaxOutputTokens)
	}
	if r.Len() != 1 {
		t.Errorf("Len() = %d, want 1 after replace", r.Len())
	}
}

func TestRegistryModelsSnapshot(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Register(ModelCapabilities{Provider: "openai", ModelID: "gpt-4o"})
	r.Register(ModelCapabilities{Provider: "deepseek", ModelID: "deepseek-chat"})

	snapshot := r.Models()
	if len(snapshot) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snapshot))
	}
	// Mutating the snapshot must not mutate the registry.
	snapshot[0].ModelID = "mutated"
	got, _ := r.Get("openai", "gpt-4o")
	if got.ModelID != "gpt-4o" {
		t.Error("Models() must return a defensive copy")
	}
}

func TestRegistryRefresh(t *testing.T) {
	t.Parallel()

	t.Run("replaces snapshot from adapter", func(t *testing.T) {
		r := NewRegistry()
		r.Register(ModelCapabilities{Provider: "openai", ModelID: "stale"})
		adapter := &fakeAdapter{models: []ModelCapabilities{
			{Provider: "openai", ModelID: "gpt-4o", ContextWindow: 128000},
			{Provider: "openrouter", ModelID: "openai/o3", SupportsReasoning: true},
		}}
		r.SetAdapter(adapter)

		if err := r.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh error: %v", err)
		}
		if r.Len() != 2 {
			t.Fatalf("Len() = %d, want 2", r.Len())
		}
		if _, ok := r.Get("openai", "stale"); ok {
			t.Error("stale model must be dropped after refresh")
		}
		o3, ok := r.Get("openrouter", "openai/o3")
		if !ok {
			t.Fatal("o3 must be present")
		}
		if !o3.SupportsReasoning {
			t.Error("o3 reasoning flag lost")
		}
		if len(o3.SupportedEfforts) != 5 {
			t.Errorf("o3 efforts = %v, want extended set", o3.SupportedEfforts)
		}
	})

	t.Run("respects ttl freshness", func(t *testing.T) {
		r := NewRegistry()
		adapter := &fakeAdapter{models: []ModelCapabilities{{Provider: "openai", ModelID: "gpt-4o"}}}
		r.SetAdapter(adapter)
		r.SetTTL(time.Hour)

		if err := r.Refresh(context.Background()); err != nil {
			t.Fatalf("first Refresh error: %v", err)
		}
		if err := r.Refresh(context.Background()); err != nil {
			t.Fatalf("second Refresh error: %v", err)
		}
		if adapter.calls != 1 {
			t.Errorf("adapter consulted %d times, want 1 within ttl", adapter.calls)
		}
	})

	t.Run("no adapter returns ErrNoAdapter", func(t *testing.T) {
		r := NewRegistry()
		if err := r.Refresh(context.Background()); !errors.Is(err, ErrNoAdapter) {
			t.Fatalf("err = %v, want ErrNoAdapter", err)
		}
	})

	t.Run("adapter failure aborts refresh", func(t *testing.T) {
		r := NewRegistry()
		sentinel := errors.New("inspect exploded")
		r.SetAdapter(&fakeAdapter{err: sentinel})
		if err := r.Refresh(context.Background()); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want sentinel", err)
		}
	})

	t.Run("sorts stable by provider then id", func(t *testing.T) {
		r := NewRegistry()
		r.SetAdapter(&fakeAdapter{models: []ModelCapabilities{
			{Provider: "openrouter", ModelID: "z/model"},
			{Provider: "openai", ModelID: "gpt-4o"},
			{Provider: "anthropic", ModelID: "claude-x"},
		}})
		if err := r.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh error: %v", err)
		}
		models := r.Models()
		got := make([]string, 0, len(models))
		for _, m := range models {
			got = append(got, m.Provider)
		}
		want := []string{"anthropic", "openai", "openrouter"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("sort order = %v, want %v", got, want)
		}
	})
}

func TestRegistryKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		provider string
		modelID  string
		want     string
	}{
		{"openai", "gpt-4o", "openai/gpt-4o"},
		{"openrouter", "openai/o3", "openai/o3"},
		{"OpenRouter", "OPENAI/O3", "openai/o3"},
		{"ollama", "llama3.1:8b", "ollama/llama3.1:8b"},
	}
	for _, tt := range tests {
		if got := key(tt.provider, tt.modelID); got != tt.want {
			t.Errorf("key(%q, %q) = %q, want %q", tt.provider, tt.modelID, got, tt.want)
		}
	}
}

func TestRegistryNilGuards(t *testing.T) {
	t.Parallel()
	var r *Registry
	if err := r.Refresh(context.Background()); err == nil {
		t.Error("nil registry Refresh must error")
	}
	if r.Models() != nil {
		t.Error("nil registry Models must be nil")
	}
	if r.Len() != 0 {
		t.Error("nil registry Len must be 0")
	}
	if _, ok := r.Get("openai", "gpt-4o"); ok {
		t.Error("nil registry Get must miss")
	}
	r.SetAdapter(&fakeAdapter{})    // must not panic
	r.SetTTL(time.Minute)           // must not panic
	r.Register(ModelCapabilities{}) // must not panic
}

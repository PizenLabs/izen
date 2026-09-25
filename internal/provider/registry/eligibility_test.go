package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/provider/detector"
)

// TestCheckExecutable_NoBlacklist: the adaptive runtime no longer blacklists
// any model. Even the observed agentic-harness models are executable — they are
// promoted, not rejected.
func TestCheckExecutable_NoBlacklist(t *testing.T) {
	for _, tc := range []struct{ provider, id string }{
		{"openrouter", "thinkingmachines/inkling:free"},
		{"OpenRouter", "ThinkingMachines/Inkling:Free"},
		{"  openrouter  ", "  thinkingmachines/inkling:free  "},
		{"openrouter", "thinkingmachines/inkling-small:free"},
		{"OpenRouter", "ThinkingMachines/Inkling-Small:Free"},
	} {
		if inelig := CheckExecutable(tc.provider, tc.id); inelig != nil {
			t.Fatalf("CheckExecutable(%q, %q) = %+v, want nil (no blacklist)", tc.provider, tc.id, inelig)
		}
	}
}

// TestCheckExecutable_NormalModels: ordinary models stay eligible, including
// the paid sibling of the seeded entry (no evidence against it).
func TestCheckExecutable_NormalModels(t *testing.T) {
	for _, tc := range []struct{ provider, id string }{
		{"openrouter", "anthropic/claude-3.5-sonnet"},
		{"openrouter", "openai/gpt-4o"},
		{"openrouter", "thinkingmachines/inkling"},
		{"openrouter", "thinkingmachines/inkling-small"},
		{"ollama", "llama3.2:3b"},
		{"openrouter", ""},
		{"", "openai/gpt-4o"},
	} {
		if inelig := CheckExecutable(tc.provider, tc.id); inelig != nil {
			t.Errorf("CheckExecutable(%q, %q) = %+v, want eligible", tc.provider, tc.id, inelig)
		}
	}
}

// TestCheckExecutable_ExactMatchNoOverblock: the policy is exact-ID matching,
// never substring or family matching — superstrings, other providers, and
// lookalikes must stay eligible.
func TestCheckExecutable_ExactMatchNoOverblock(t *testing.T) {
	for _, tc := range []struct{ provider, id string }{
		{"openrouter", "thinkingmachines/inkling:free:extended"},
		{"openrouter", "xthinkingmachines/inkling:free"},
		{"openrouter", "thinkingmachines/inkling:free2"},
		{"groq", "thinkingmachines/inkling:free"},
	} {
		if inelig := CheckExecutable(tc.provider, tc.id); inelig != nil {
			t.Errorf("CheckExecutable(%q, %q) = %+v, want eligible (exact match only)", tc.provider, tc.id, inelig)
		}
	}
}

// TestModelDescriptorExecutable covers the descriptor-level view used by
// ExecutableModels/LoadExecutable. No discovered model is locally rejected.
func TestModelDescriptorExecutable(t *testing.T) {
	flagged := ModelDescriptor{ID: "openai/gpt-4o", Provider: "openrouter", IneligibleReason: string(ReasonAgenticHarnessOnly)}
	if !flagged.Executable() {
		t.Errorf("legacy IneligibleReason must not make a descriptor non-executable")
	}
	policyHit := ModelDescriptor{ID: "thinkingmachines/inkling:free", Provider: "openrouter"}
	if !policyHit.Executable() {
		t.Errorf("agentic-harness model must be executable (promoted, not blocked)")
	}
	normal := ModelDescriptor{ID: "openai/gpt-4o", Provider: "openrouter"}
	if !normal.Executable() {
		t.Errorf("normal model must be executable")
	}
}

// TestExecutableModels_PreservesOrder: with no blacklist every discovered
// model is executable, order is preserved, and the input is never mutated.
func TestExecutableModels_PreservesOrder(t *testing.T) {
	in := []Model{
		{ID: "openai/gpt-4o", Provider: "openrouter"},
		{ID: "thinkingmachines/inkling:free", Provider: "openrouter"},
		{ID: "anthropic/claude-3.5-sonnet", Provider: "openrouter"},
	}
	got := ExecutableModels(in)
	if len(got) != 3 || got[0].ID != "openai/gpt-4o" || got[1].ID != "thinkingmachines/inkling:free" || got[2].ID != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("ExecutableModels = %v, want all three models in order", got)
	}
	if len(in) != 3 {
		t.Fatalf("input slice must not be mutated, got %d models", len(in))
	}
}

// TestLoadExecutable_NoModelHidden: with no blacklist the executable view
// equals the full discovered catalog.
func TestLoadExecutable_NoModelHidden(t *testing.T) {
	r := NewRegistryWithCachePath("")
	r.SetSeed([]ModelDescriptor{
		{ID: "openai/gpt-4o", Provider: "openrouter", Name: "GPT-4o"},
		{ID: "thinkingmachines/inkling:free", Provider: "openrouter", Name: "Inkling (free)"},
	})
	full := r.Load()
	if len(full.Models) != 2 {
		t.Fatalf("discovered catalog = %d models, want 2", len(full.Models))
	}
	exec := r.LoadExecutable()
	if len(exec.Models) != 2 {
		t.Fatalf("executable view = %v, want every discovered model (nothing hidden)", exec.Models)
	}
	if exec.Version != full.Version {
		t.Errorf("executable view must carry the snapshot version, got %d want %d", exec.Version, full.Version)
	}
}

// TestFetchHTTPModels_PreservesCatalog: a mocked catalog containing an
// agentic-harness model discovers every model and marks none ineligible.
func TestFetchHTTPModels_PreservesCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"id": "thinkingmachines/inkling:free", "name": "Inkling (free)"},
			map[string]any{"id": "openai/gpt-4o", "name": "GPT-4o"},
		}})
	}))
	defer server.Close()

	r := NewRegistryWithCachePath("")
	got, err := r.fetchHTTPModels(context.Background(), detector.ProviderConfig{
		Name:    "openrouter",
		APIKey:  "test-key",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("fetchHTTPModels() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("discovered = %d models, want 2 (catalog presence preserved)", len(got))
	}
	byID := map[string]ModelDescriptor{}
	for _, m := range got {
		byID[m.ID] = m
	}
	ink, ok := byID["thinkingmachines/inkling:free"]
	if !ok {
		t.Fatalf("agentic-harness model must still be discovered, got %v", got)
	}
	if !ink.Executable() {
		t.Errorf("agentic-harness model must stay executable (promoted, not blocked)")
	}
	if ink.IneligibleReason != "" {
		t.Errorf("no model may be marked ineligible, got %q", ink.IneligibleReason)
	}
	if normal := byID["openai/gpt-4o"]; !normal.Executable() {
		t.Errorf("normal model must stay executable: %+v", normal)
	}
}

// TestSync_PreservesRawCatalogOnDisk: Sync persists the full discovered
// catalog (including ineligible models) so raw provider metadata survives
// restarts; the executable view filters at read time.
func TestSync_PreservesRawCatalogOnDisk(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistryWithCachePath(filepath.Join(dir, "models.json"))
	r.SetFetch(func(_ context.Context, _ detector.ProviderConfig) ([]ModelDescriptor, error) {
		return []ModelDescriptor{
			{ID: "thinkingmachines/inkling:free", Provider: "openrouter", Name: "Inkling (free)"},
			{ID: "openai/gpt-4o", Provider: "openrouter", Name: "GPT-4o"},
		}, nil
	})
	provs := []detector.ProviderConfig{{Name: "openrouter", APIKey: "k", BaseURL: "https://openrouter.ai/api/v1"}}
	if err := r.Sync(context.Background(), provs); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if got := r.LoadExecutable().Models; len(got) != 2 {
		t.Fatalf("executable view after Sync = %v, want every discovered model", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	file, err := DecodeCacheFile(data)
	if err != nil {
		t.Fatalf("DecodeCacheFile: %v", err)
	}
	flat := file.FlattenModels()
	if len(flat) != 2 {
		t.Fatalf("on-disk catalog = %d models, want 2 (raw preserved)", len(flat))
	}
}

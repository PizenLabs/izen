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

// TestCheckExecutable_SeededAgenticOnly: the evidence-seeded entry is
// ineligible with its documented reason, matched case-insensitively.
func TestCheckExecutable_SeededAgenticOnly(t *testing.T) {
	for _, tc := range []struct{ provider, id string }{
		{"openrouter", "thinkingmachines/inkling:free"},
		{"OpenRouter", "ThinkingMachines/Inkling:Free"},
		{"  openrouter  ", "  thinkingmachines/inkling:free  "},
	} {
		inelig := CheckExecutable(tc.provider, tc.id)
		if inelig == nil {
			t.Fatalf("CheckExecutable(%q, %q) = nil, want ineligible", tc.provider, tc.id)
		}
		if inelig.Reason != ReasonAgenticHarnessOnly {
			t.Errorf("Reason = %q, want %q", inelig.Reason, ReasonAgenticHarnessOnly)
		}
		if inelig.Detail == "" || inelig.Evidence == "" {
			t.Errorf("Detail/Evidence must be documented, got %+v", inelig)
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
		{"openrouter", "thinkingmachines/inkling-small:free"},
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
// ExecutableModels/LoadExecutable.
func TestModelDescriptorExecutable(t *testing.T) {
	flagged := ModelDescriptor{ID: "openai/gpt-4o", Provider: "openrouter", IneligibleReason: string(ReasonAgenticHarnessOnly)}
	if flagged.Executable() {
		t.Errorf("descriptor with IneligibleReason must not be executable")
	}
	policyHit := ModelDescriptor{ID: "thinkingmachines/inkling:free", Provider: "openrouter"}
	if policyHit.Executable() {
		t.Errorf("policy-listed model must not be executable even without explicit flag")
	}
	normal := ModelDescriptor{ID: "openai/gpt-4o", Provider: "openrouter"}
	if !normal.Executable() {
		t.Errorf("normal model must be executable")
	}
}

// TestExecutableModels_PreservesOrder: filtering keeps order and never
// mutates the input.
func TestExecutableModels_PreservesOrder(t *testing.T) {
	in := []Model{
		{ID: "openai/gpt-4o", Provider: "openrouter"},
		{ID: "thinkingmachines/inkling:free", Provider: "openrouter"},
		{ID: "anthropic/claude-3.5-sonnet", Provider: "openrouter"},
	}
	got := ExecutableModels(in)
	if len(got) != 2 || got[0].ID != "openai/gpt-4o" || got[1].ID != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("ExecutableModels = %v, want the two eligible models in order", got)
	}
	if len(in) != 3 {
		t.Fatalf("input slice must not be mutated, got %d models", len(in))
	}
}

// TestLoadExecutable_SeparatesDiscoveredFromSelectable: the full snapshot
// still discovers every catalog model while the executable view excludes
// the ineligible one.
func TestLoadExecutable_SeparatesDiscoveredFromSelectable(t *testing.T) {
	r := NewRegistryWithCachePath("")
	r.SetSeed([]ModelDescriptor{
		{ID: "openai/gpt-4o", Provider: "openrouter", Name: "GPT-4o"},
		{ID: "thinkingmachines/inkling:free", Provider: "openrouter", Name: "Inkling (free)"},
	})
	full := r.Load()
	if len(full.Models) != 2 {
		t.Fatalf("discovered catalog = %d models, want 2 (raw preserved)", len(full.Models))
	}
	exec := r.LoadExecutable()
	if len(exec.Models) != 1 || exec.Models[0].ID != "openai/gpt-4o" {
		t.Fatalf("executable view = %v, want only openai/gpt-4o", exec.Models)
	}
	if exec.Version != full.Version {
		t.Errorf("executable view must carry the snapshot version, got %d want %d", exec.Version, full.Version)
	}
}

// TestFetchHTTPModels_MarksIneligibleButPreservesCatalog: a mocked catalog
// containing an agentic-only model yields discovered==true for both models,
// eligible==false/selectable==false only for the restricted one.
func TestFetchHTTPModels_MarksIneligibleButPreservesCatalog(t *testing.T) {
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
		t.Fatalf("agentic-only model must still be discovered, got %v", got)
	}
	if ink.Executable() {
		t.Errorf("thinkingmachines/inkling:free must be flagged ineligible")
	}
	if ink.IneligibleReason == "" {
		t.Errorf("flagged model must carry IneligibleReason")
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
	if got := r.LoadExecutable().Models; len(got) != 1 || got[0].ID != "openai/gpt-4o" {
		t.Fatalf("executable view after Sync = %v, want only openai/gpt-4o", got)
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

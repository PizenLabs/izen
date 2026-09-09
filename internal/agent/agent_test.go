package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/llm"
	"github.com/PizenLabs/izen/internal/provider/detector"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

func TestNormalizeModelID(t *testing.T) {
	cases := []struct {
		in           string
		wantProvider string
		wantBare     string
	}{
		{"groq/llama-3.3-70b-versatile", "groq", "llama-3.3-70b-versatile"},
		{"llama-3.3-70b-versatile", "", "llama-3.3-70b-versatile"},
		{"openrouter/anthropic/claude", "openrouter", "anthropic/claude"},
		{"  gpt-4o  ", "", "gpt-4o"},
		{"", "", ""},
	}
	for _, c := range cases {
		p, b := NormalizeModelID(c.in)
		if p != c.wantProvider || b != c.wantBare {
			t.Errorf("NormalizeModelID(%q) = (%q,%q), want (%q,%q)", c.in, p, b, c.wantProvider, c.wantBare)
		}
	}
}

// mockProvider is a scripted llm.LLMProvider.
type mockProvider struct {
	name string
	fn   func(req llm.PromptRequest) (llm.LLMResponse, error)
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) GenerateResponse(_ context.Context, req llm.PromptRequest) (llm.LLMResponse, error) {
	return m.fn(req)
}

func (m *mockProvider) StreamResponse(ctx context.Context, req llm.PromptRequest, _ llm.StreamHandler) (llm.LLMResponse, error) {
	return m.GenerateResponse(ctx, req)
}

func testRegistry() *registry.Registry {
	r := registry.NewRegistryWithCachePath("")
	r.SetSeed([]registry.ModelDescriptor{
		{ID: "groq/llama-3.3-70b-versatile", Provider: "groq", Name: "Llama", Capabilities: []registry.ModelCapability{registry.CapTools}},
		{ID: "tiny-smol", Provider: "groq", Name: "Smol", Capabilities: []registry.ModelCapability{registry.CapTools}},
	})
	return r
}

func testDispatcher(workDir string, fn func(req llm.PromptRequest) (llm.LLMResponse, error)) *Dispatcher {
	return &Dispatcher{
		WorkDir:   workDir,
		SessionID: "sess-test",
		Cfg:       &config.CascadeConfig{Roles: map[string]string{}},
		Reg:       testRegistry(),
		Providers: []detector.ProviderConfig{{Name: "groq", APIKey: "k", BaseURL: "https://api.groq.com/openai/v1"}},
		Factory: func(_, _, _ string) (llm.LLMProvider, error) {
			return &mockProvider{name: "groq", fn: fn}, nil
		},
	}
}

func TestDispatchForRoleLogsEvent(t *testing.T) {
	workDir := t.TempDir()
	d := testDispatcher(workDir, func(req llm.PromptRequest) (llm.LLMResponse, error) {
		return llm.LLMResponse{Content: "hello"}, nil
	})
	resp, err := d.DispatchForRole(context.Background(), "plan", "do work")
	if err != nil {
		t.Fatalf("DispatchForRole: %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("content = %q", resp.Content)
	}
	data, err := os.ReadFile(filepath.Join(workDir, ".izen", "audit", "events.ndjson"))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(data), "role_dispatched") {
		t.Fatalf("events missing role_dispatched:\n%s", data)
	}
}

func TestDispatchRetryable429(t *testing.T) {
	workDir := t.TempDir()
	d := testDispatcher(workDir, func(_ llm.PromptRequest) (llm.LLMResponse, error) {
		return llm.LLMResponse{}, &ProviderError{StatusCode: 429, Message: "rate limited"}
	})
	_, err := d.DispatchForRole(context.Background(), "default", "prompt")
	if err == nil {
		t.Fatal("expected 429 error")
	}
	if !IsRetryable(err) {
		t.Fatalf("expected retryable, got %v", err)
	}
	if IsRetryable(nil) {
		t.Fatal("nil must not be retryable")
	}
}

func TestLoopEndToEndWithMocks(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "app.txt"), []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}
	patchDoc, _ := json.Marshal(map[string]any{
		"files": []map[string]any{{"path": "app.txt", "content": "v1"}},
	})
	calls := 0
	d := testDispatcher(workDir, func(req llm.PromptRequest) (llm.LLMResponse, error) {
		calls++
		switch calls {
		case 1:
			return llm.LLMResponse{Content: "# Plan\n- update app"}, nil
		case 2:
			return llm.LLMResponse{Content: string(patchDoc)}, nil
		default:
			return llm.LLMResponse{Content: "feat: update app"}, nil
		}
	})
	loop := NewLoop(workDir, "sess-e2e", d)
	loop.RunID = "run1"
	if err := loop.Run(context.Background(), "update app.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, p := range []string{
		filepath.Join(".izen", "plans", "current.md"),
		filepath.Join(".izen", "patches", "run-run1-patch-1.json"),
		filepath.Join(".izen", "artifacts", "commit-message-run1.txt"),
		filepath.Join(".izen", "audit", "events.ndjson"),
	} {
		if _, err := os.Stat(filepath.Join(workDir, p)); err != nil {
			t.Fatalf("expected %s: %v", p, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(workDir, ".izen", "checkpoints"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected checkpoints, entries=%v err=%v", entries, err)
	}
	if got, _ := os.ReadFile(filepath.Join(workDir, "app.txt")); string(got) != "v1" {
		t.Fatalf("app.txt = %q, want v1", got)
	}
	// Evidence file must exist.
	arts, err := os.ReadDir(filepath.Join(workDir, ".izen", "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	foundEvidence := false
	for _, e := range arts {
		if strings.HasPrefix(e.Name(), "evidence_") {
			foundEvidence = true
		}
	}
	if !foundEvidence {
		t.Fatal("expected evidence_*.json artifact")
	}
}

func TestLoopProviderFailureLeavesWorkspaceRecoverable(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "app.txt"), []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := testDispatcher(workDir, func(_ llm.PromptRequest) (llm.LLMResponse, error) {
		return llm.LLMResponse{}, &ProviderError{StatusCode: 500, Message: "boom"}
	})
	loop := NewLoop(workDir, "sess-fail", d)
	loop.RunID = "runfail"
	err := loop.Run(context.Background(), "doomed task")
	if err == nil {
		t.Fatal("expected Run error on 500")
	}
	if !IsRetryable(err) {
		t.Fatalf("expected retryable error chain, got %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(workDir, "app.txt")); string(got) != "v0" {
		t.Fatalf("workspace mutated on failure: %q", got)
	}
}

func TestLoopRejectsNonJSONPatch(t *testing.T) {
	workDir := t.TempDir()
	d := testDispatcher(workDir, func(req llm.PromptRequest) (llm.LLMResponse, error) {
		// Plan + smol succeed; default returns prose, not JSON.
		if strings.Contains(req.System, "planning") || strings.Contains(req.System, "Plan") {
			return llm.LLMResponse{Content: "plan"}, nil
		}
		if strings.Contains(strings.ToLower(req.System), "summarizer") || strings.Contains(strings.ToLower(req.System), "commit") {
			return llm.LLMResponse{Content: "msg"}, nil
		}
		return llm.LLMResponse{Content: "not json, just prose"}, nil
	})
	// Force the second call (default role) to return prose regardless.
	calls := 0
	d.Factory = func(_, _, _ string) (llm.LLMProvider, error) {
		return &mockProvider{name: "groq", fn: func(_ llm.PromptRequest) (llm.LLMResponse, error) {
			calls++
			if calls == 2 {
				return llm.LLMResponse{Content: "prose, not json"}, nil
			}
			return llm.LLMResponse{Content: fmt.Sprintf("resp-%d", calls)}, nil
		}}, nil
	}
	loop := NewLoop(workDir, "sess-badpatch", d)
	if err := loop.Run(context.Background(), "task"); err == nil {
		t.Fatal("expected error for non-JSON patch")
	}
}

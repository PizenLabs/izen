package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/session"
)

// mockProvider implements ai.Provider for testing.
type mockProvider struct {
	responses []*ai.Response
	callCount int
}

func (m *mockProvider) Name() string {
	return "mock"
}

func (m *mockProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	if m.callCount >= len(m.responses) {
		return nil, fmt.Errorf("unexpected call #%d (only %d responses configured)", m.callCount+1, len(m.responses))
	}
	resp := m.responses[m.callCount]
	m.callCount++
	return resp, nil
}

func (m *mockProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported in mock")
}

func TestProposeBuildPatch_RetriesOnAmbiguousSnippet(t *testing.T) {
	smallSnippet := "func main() {}\n"
	largeOriginalContent := "package main\n\n"
	for i := 0; i < 50; i++ {
		largeOriginalContent += fmt.Sprintf("// line %d\n", i)
	}
	largeOriginalContent += "func main() {\n\tfmt.Println(\"hello\")\n}\n"
	validPatch := "<<<<<<< SEARCH\n\tfmt.Println(\"hello\")\n=======\n\tfmt.Println(\"world\")\n>>>>>>>"

	t.Run("first try succeeds without retry", func(t *testing.T) {
		mock := &mockProvider{
			responses: []*ai.Response{
				{Content: validPatch},
			},
		}
		m := testModelWithProvider(mock)

		msg := m.proposeBuildPatch(&plan.Task{
			StepNum: 1,
			Type:    "FILE_MUTATE",
			Target:  t.TempDir() + "/nonexistent.go",
			Status:  "idle",
		})()

		result, ok := msg.(buildProposalReadyMsg)
		if !ok {
			t.Fatalf("expected buildProposalReadyMsg, got %T", msg)
		}
		if result.Err != nil {
			t.Fatalf("expected no error on valid patch, got: %v", result.Err)
		}
		_ = result
	})

	t.Run("retries on ambiguous snippet then succeeds", func(t *testing.T) {
		mock := &mockProvider{
			responses: []*ai.Response{
				// First call: ambiguous snippet (no markers, small)
				{Content: smallSnippet},
				// Second call: valid SEARCH/REPLACE patch
				{Content: validPatch},
			},
		}
		m := testModelWithProvider(mock)

		dir := t.TempDir()
		filePath := dir + "/main.go"
		// Write the large file to disk so IsAmbiguousSnippet detects the mismatch
		if err := writeFile(filePath, largeOriginalContent); err != nil {
			t.Fatal(err)
		}

		msg := m.proposeBuildPatch(&plan.Task{
			StepNum: 1,
			Type:    "FILE_MUTATE",
			Target:  filePath,
			Status:  "idle",
		})()

		result, ok := msg.(buildProposalReadyMsg)
		if !ok {
			t.Fatalf("expected buildProposalReadyMsg, got %T", msg)
		}
		if result.Err != nil {
			t.Fatalf("expected retry to succeed, got error: %v", result.Err)
		}
		if mock.callCount != 2 {
			t.Fatalf("expected 2 LLM calls (1 initial + 1 retry), got %d", mock.callCount)
		}
	})

	t.Run("fails after exhausting retries", func(t *testing.T) {
		mock := &mockProvider{
			responses: []*ai.Response{
				{Content: smallSnippet},
				{Content: smallSnippet},
				{Content: smallSnippet},
			},
		}
		m := testModelWithProvider(mock)

		dir := t.TempDir()
		filePath := dir + "/main.go"
		if err := writeFile(filePath, largeOriginalContent); err != nil {
			t.Fatal(err)
		}

		msg := m.proposeBuildPatch(&plan.Task{
			StepNum: 1,
			Type:    "FILE_MUTATE",
			Target:  filePath,
			Status:  "idle",
		})()

		result, ok := msg.(buildProposalReadyMsg)
		if !ok {
			t.Fatalf("expected buildProposalReadyMsg, got %T", msg)
		}
		if result.Err == nil {
			t.Fatal("expected error after exhausting retries, got nil")
		}
		if !errors.Is(result.Err, execution.ErrInvalidPatchFormat) {
			t.Fatalf("expected ErrInvalidPatchFormat, got: %v", result.Err)
		}
		if mock.callCount != 3 {
			t.Fatalf("expected 3 LLM calls (1 initial + 2 retries), got %d", mock.callCount)
		}
	})

	t.Run("non-ambiguous new file succeeds immediately", func(t *testing.T) {
		mock := &mockProvider{
			responses: []*ai.Response{
				{Content: "package main\n\nfunc main() {}\n"},
			},
		}
		m := testModelWithProvider(mock)

		dir := t.TempDir()
		filePath := dir + "/new.go"

		msg := m.proposeBuildPatch(&plan.Task{
			StepNum: 1,
			Type:    "FILE_MUTATE",
			Target:  filePath,
			Status:  "idle",
		})()

		result, ok := msg.(buildProposalReadyMsg)
		if !ok {
			t.Fatalf("expected buildProposalReadyMsg, got %T", msg)
		}
		if result.Err != nil {
			t.Fatalf("expected no error for new file, got: %v", result.Err)
		}
		if mock.callCount != 1 {
			t.Fatalf("expected 1 LLM call (no retry needed), got %d", mock.callCount)
		}
	})
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// testModelWithProvider creates a minimal model with a mock provider for testing.
func testModelWithProvider(p *mockProvider) *model {
	cfg := &config.Config{
		Models: config.ModelConfig{
			Default: "test-model",
		},
	}
	return &model{
		cfg:      cfg,
		provider: p,
		resolver: modes.NewResolver(),
		sess: &session.Session{
			ContextID: "test-context",
		},
	}
}

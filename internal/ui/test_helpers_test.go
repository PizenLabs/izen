package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/workflow"
)

// fakeSourceVerifier always passes source-hash verification.
type fakeSourceVerifier struct{}

func (fakeSourceVerifier) VerifySourceHash([]string, string) error { return nil }

// fakeCheckpointChecker always reports a valid checkpoint.
type fakeCheckpointChecker struct{}

func (fakeCheckpointChecker) HasCheckpoint() bool { return true }
func (fakeCheckpointChecker) LatestCheckpoint() (workflow.CheckpointRef, error) {
	return workflow.CheckpointRef("cp-test"), nil
}

// mockProvider implements ai.Provider for testing.
type mockProvider struct {
	responses []*ai.Response
	callCount int
	requests  []ai.Request
}

func (m *mockProvider) Name() string {
	return "mock"
}

func (m *mockProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	if m.callCount >= len(m.responses) {
		return nil, fmt.Errorf("unexpected call #%d (only %d responses configured)", m.callCount+1, len(m.responses))
	}
	m.requests = append(m.requests, req)
	resp := m.responses[m.callCount]
	m.callCount++
	return resp, nil
}

func (m *mockProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported in mock")
}

// drainCmds executes a tea.Cmd and returns every terminal message it yields,
// recursively expanding nested tea.BatchMsg groups. This mirrors how the
// Bubble Tea runtime dispatches multi-message commands.
func drainCmds(t *testing.T, c tea.Cmd) []tea.Msg {
	t.Helper()
	var out []tea.Msg
	var stack []tea.Cmd
	if c != nil {
		stack = append(stack, c)
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		msg := cur()
		if batch, ok := msg.(tea.BatchMsg); ok {
			stack = append(stack, batch...)
			continue
		}
		out = append(out, msg)
	}
	return out
}

// recordsText joins every record the model has pushed so tests can assert what
// was (and was not) rendered/logged.
func recordsText(m *model) string {
	var b strings.Builder
	for _, r := range m.records {
		b.WriteString(r.text)
		b.WriteString("\n")
	}
	return b.String()
}

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/projection/diff"
	"github.com/PizenLabs/izen/pkg/runtime/authorization"
	"github.com/PizenLabs/izen/pkg/runtime/orchestrator"
	"github.com/PizenLabs/izen/pkg/runtime/preflight"
	"github.com/PizenLabs/izen/pkg/runtime/target"
)

// stubLLM is a scriptable LLMProvider.
type stubLLM struct {
	content   string
	err       error
	gotSystem string
	gotPrompt string
	calls     int
}

func (l *stubLLM) Complete(_ context.Context, system, prompt string) (string, error) {
	l.calls++
	l.gotSystem = system
	l.gotPrompt = prompt
	return l.content, l.err
}

func TestWire(t *testing.T) {
	t.Parallel()

	stack := Wire(&stubLLM{}, t.TempDir(), strings.NewReader(""), &bytes.Buffer{})
	if stack == nil {
		t.Fatal("Wire returned nil")
	}
	for name, v := range map[string]any{
		"preflight":    stack.Preflight,
		"validator":    stack.Validator,
		"executor":     stack.Executor,
		"gate":         stack.Gate,
		"provider":     stack.Provider,
		"bridge":       stack.Bridge,
		"orchestrator": stack.Orchestrator,
	} {
		if v == nil {
			t.Errorf("Wire left %s nil", name)
		}
	}
	if stack.TokenBudget <= 0 {
		t.Errorf("TokenBudget = %d, want positive default", stack.TokenBudget)
	}
	if stack.Gate.CurrentSession() != nil {
		t.Error("a freshly wired stack must not have an approval session")
	}
}

func TestProposalProviderGenerateProposal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	t.Run("strips code fence and makes canonical absolute", func(t *testing.T) {
		t.Parallel()
		llm := &stubLLM{content: "```\n# Updated\nline2\n```\n"}
		p := NewProposalProvider(llm, dir)
		proposal, err := p.GenerateProposal(context.Background(), compiledRequestFor(t, dir, "README.md"))
		if err != nil {
			t.Fatalf("GenerateProposal: %v", err)
		}
		if proposal.ProposalID == "" {
			t.Error("expected a non-empty proposal ID")
		}
		if got := proposal.RawPatch; got != "# Updated\nline2" {
			t.Errorf("RawPatch = %q, want fenced content stripped", got)
		}
		if !filepath.IsAbs(proposal.TargetRef.Canonical) {
			t.Errorf("Canonical = %q, want absolute", proposal.TargetRef.Canonical)
		}
		if proposal.TargetRef.Canonical != filepath.Join(dir, "README.md") {
			t.Errorf("Canonical = %q, want %q", proposal.TargetRef.Canonical, filepath.Join(dir, "README.md"))
		}
		if !strings.Contains(llm.gotSystem, "README.md") {
			t.Errorf("system prompt = %q, want target identity", llm.gotSystem)
		}
		if !strings.Contains(llm.gotPrompt, "<izen_task>") {
			t.Errorf("prompt = %q, want compiled task envelope", llm.gotPrompt)
		}
	})

	t.Run("raw content passes through untouched", func(t *testing.T) {
		t.Parallel()
		llm := &stubLLM{content: "no fence at all"}
		p := NewProposalProvider(llm, dir)
		proposal, err := p.GenerateProposal(context.Background(), compiledRequestFor(t, dir, "a.txt"))
		if err != nil {
			t.Fatalf("GenerateProposal: %v", err)
		}
		if proposal.RawPatch != "no fence at all" {
			t.Errorf("RawPatch = %q, want pass-through", proposal.RawPatch)
		}
	})

	t.Run("empty response rejected", func(t *testing.T) {
		t.Parallel()
		p := NewProposalProvider(&stubLLM{content: "   \n"}, dir)
		if _, err := p.GenerateProposal(context.Background(), compiledRequestFor(t, dir, "a.txt")); err == nil {
			t.Fatal("expected error for empty model response")
		}
	})

	t.Run("nil llm rejected", func(t *testing.T) {
		t.Parallel()
		p := NewProposalProvider(nil, dir)
		if _, err := p.GenerateProposal(context.Background(), compiledRequestFor(t, dir, "a.txt")); err == nil {
			t.Fatal("expected error for nil llm")
		}
	})

	t.Run("nil receiver rejected", func(t *testing.T) {
		t.Parallel()
		var p *ProposalProviderCLI
		if _, err := p.GenerateProposal(context.Background(), compiledRequestFor(t, dir, "a.txt")); err == nil {
			t.Fatal("expected error for nil receiver")
		}
	})

	t.Run("missing target rejected", func(t *testing.T) {
		t.Parallel()
		p := NewProposalProvider(&stubLLM{content: "x"}, dir)
		req := compiledRequestFor(t, dir, "a.txt")
		req.TargetRef = nil
		if _, err := p.GenerateProposal(context.Background(), req); err == nil {
			t.Fatal("expected error for missing target reference")
		}
	})

	t.Run("llm error propagates", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("llm down")
		p := NewProposalProvider(&stubLLM{err: sentinel}, dir)
		if _, err := p.GenerateProposal(context.Background(), compiledRequestFor(t, dir, "a.txt")); !errors.Is(err, sentinel) {
			t.Errorf("error = %v, want %v", err, sentinel)
		}
	})
}

// compiledRequestFor builds a CompiledRequest whose canonical target is a
// relative path inside dir, mirroring what the preflight engine produces.
func compiledRequestFor(_ *testing.T, _ string, name string) *preflight.CompiledRequest {
	return &preflight.CompiledRequest{
		TargetRef: &target.TargetRef{
			Raw:       name,
			Canonical: name,
			Exists:    false,
			Tracked:   false,
			Source:    target.ResolutionRaw,
		},
		Risk:            preflight.RiskMedium,
		FormattedPrompt: "<izen_task>\n  <instruction>update</instruction>\n</izen_task>",
	}
}

func TestNewProposalID(t *testing.T) {
	t.Parallel()
	a, b := NewProposalID(), NewProposalID()
	if a == "" || b == "" {
		t.Fatal("expected non-empty proposal IDs")
	}
	if a == b {
		t.Error("expected distinct proposal IDs across calls")
	}
}

func TestStripCodeFence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{"```\na\nb\n```", "a\nb"},
		{"```go\npackage main\n```", "package main"},
		{"no fence", "no fence"},
		{"```\n```", ""},
	}
	for _, tt := range tests {
		if got := stripCodeFence(tt.in); got != tt.want {
			t.Errorf("stripCodeFence(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTerminalBridgeWaitForApproval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  authorization.ApprovalAction
	}{
		{name: "yes executes", input: "y\n", want: authorization.ActionExecute},
		{name: "yes word executes", input: "YES\n", want: authorization.ActionExecute},
		{name: "inspect word", input: "i\n", want: authorization.ActionInspect},
		{name: "no cancels", input: "n\n", want: authorization.ActionCancel},
		{name: "empty cancels", input: "\n", want: authorization.ActionCancel},
		{name: "enter cancels", input: "xyz\n", want: authorization.ActionCancel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			b := NewTerminalBridge(strings.NewReader(tt.input), &out)
			b.OnSessionArmed(authorization.InteractionEpoch(7))
			evt, err := b.WaitForApproval(context.Background())
			if err != nil {
				t.Fatalf("WaitForApproval: %v", err)
			}
			if evt.Epoch != 7 {
				t.Errorf("Epoch = %d, want 7", evt.Epoch)
			}
			if evt.Action != tt.want {
				t.Errorf("Action = %v, want %v", evt.Action, tt.want)
			}
			if !strings.Contains(out.String(), "[y/N]") {
				t.Errorf("prompt output = %q, want [y/N]", out.String())
			}
		})
	}
}

func TestTerminalBridgeGuardConditions(t *testing.T) {
	t.Parallel()

	t.Run("approval before arming rejected", func(t *testing.T) {
		t.Parallel()
		b := NewTerminalBridge(strings.NewReader("y\n"), &bytes.Buffer{})
		if _, err := b.WaitForApproval(context.Background()); err == nil {
			t.Fatal("expected error when session was never armed")
		}
	})

	t.Run("nil receiver rejected", func(t *testing.T) {
		t.Parallel()
		var b *TerminalBridge
		if _, err := b.WaitForApproval(context.Background()); err == nil {
			t.Fatal("expected error for nil bridge")
		}
		if err := b.RenderProposal(evidenceFixture(), viewportFixture()); err == nil {
			t.Fatal("expected error for nil bridge render")
		}
	})

	t.Run("unwired bridge rejected", func(t *testing.T) {
		t.Parallel()
		b := NewTerminalBridge(nil, nil)
		if _, err := b.WaitForApproval(context.Background()); err == nil {
			t.Fatal("expected error for unwired bridge")
		}
	})

	t.Run("context cancellation aborts wait", func(t *testing.T) {
		t.Parallel()
		// A reader that never returns keeps the wait blocked until the context
		// is cancelled.
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		defer func() { _ = pr.Close() }()
		defer func() { _ = pw.Close() }()
		b := NewTerminalBridge(pr, &bytes.Buffer{})
		b.OnSessionArmed(1)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, err := b.WaitForApproval(ctx); err == nil {
			t.Fatal("expected context cancellation error")
		}
	})
}

// failWriter is an io.Writer that always fails, used to exercise render error
// propagation.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestTerminalBridgeRenderProposal(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	b := NewTerminalBridge(strings.NewReader(""), &out)
	if err := b.RenderProposal(evidenceFixture(), viewportFixture()); err != nil {
		t.Fatalf("RenderProposal: %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "README.md") {
		t.Errorf("render output = %q, want target file name", rendered)
	}
	if !strings.Contains(rendered, "- old line") || !strings.Contains(rendered, "+ new line") {
		t.Errorf("render output = %q, want diff markers", rendered)
	}
}

func TestTerminalBridgeRenderProposalErrors(t *testing.T) {
	t.Parallel()

	t.Run("write failure propagates", func(t *testing.T) {
		t.Parallel()
		b := NewTerminalBridge(strings.NewReader(""), &failWriter{})
		if err := b.RenderProposal(evidenceFixture(), viewportFixture()); err == nil {
			t.Fatal("expected render error for failing writer")
		}
	})

	t.Run("nil output rejected", func(t *testing.T) {
		t.Parallel()
		b := NewTerminalBridge(strings.NewReader(""), nil)
		if err := b.RenderProposal(evidenceFixture(), viewportFixture()); err == nil {
			t.Fatal("expected render error for nil output")
		}
	})

	t.Run("nil receiver on session armed is a no-op", func(t *testing.T) {
		t.Parallel()
		var b *TerminalBridge
		b.OnSessionArmed(1) // must not panic
	})
}

func TestDiffMarkerModify(t *testing.T) {
	t.Parallel()
	if got := diffMarker(diff.MutationModify); got != " " {
		t.Errorf("diffMarker(Modify) = %q, want space", got)
	}
}

func TestStackRunEndToEnd(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	if err := os.WriteFile(path, []byte("# Old\n"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	llm := &stubLLM{content: "# New from model\n"}
	var out bytes.Buffer
	stack := Wire(llm, dir, strings.NewReader("y\n"), &out)

	res, err := stack.Run(context.Background(), dir, "README.md")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res == nil {
		t.Fatal("expected a non-nil result")
	}
	if !res.Committed {
		t.Fatal("expected the proposal to be committed after an explicit yes")
	}
	if res.Action != authorization.ActionExecute {
		t.Errorf("Action = %v, want execute", res.Action)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "# New from model\n" {
		t.Errorf("content = %q, want %q", string(data), "# New from model\n")
	}
	rendered := out.String()
	if !strings.Contains(rendered, "README.md") {
		t.Errorf("render output = %q, want target file name", rendered)
	}
	if !strings.Contains(rendered, "[y/N]") {
		t.Errorf("render output = %q, want approval prompt", rendered)
	}
}

func TestStackRunRejectionEndToEnd(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	if err := os.WriteFile(path, []byte("# Old\n"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	llm := &stubLLM{content: "# Would have changed\n"}
	var out bytes.Buffer
	stack := Wire(llm, dir, strings.NewReader("n\n"), &out)

	res, err := stack.Run(context.Background(), dir, "README.md")
	if !errors.Is(err, orchestrator.ErrExecutionRejected) {
		t.Fatalf("Run error = %v, want execution rejected", err)
	}
	if res == nil {
		t.Fatal("expected a result describing the rejection")
	}
	if res.Committed {
		t.Fatal("expected Committed = false after a no")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "# Old\n" {
		t.Errorf("content = %q, want untouched %q", string(data), "# Old\n")
	}
}

func TestStackRunNilStackAndNewFile(t *testing.T) {
	t.Parallel()

	t.Run("nil stack rejected", func(t *testing.T) {
		t.Parallel()
		var s *Stack
		if _, err := s.Run(context.Background(), ".", "x.txt"); err == nil {
			t.Fatal("expected error for nil stack")
		}
	})

	t.Run("zero token budget falls back to default", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Old\n"), 0o644); err != nil {
			t.Fatalf("write target: %v", err)
		}
		stack := Wire(&stubLLM{content: "# New\n"}, dir, strings.NewReader("y\n"), &bytes.Buffer{})
		stack.TokenBudget = 0
		res, err := stack.Run(context.Background(), dir, "README.md")
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
		if !res.Committed {
			t.Fatal("expected the proposal to be committed")
		}
	})

	t.Run("new file creation", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		llm := &stubLLM{content: "created content\n"}
		stack := Wire(llm, dir, strings.NewReader("y\n"), &bytes.Buffer{})
		res, err := stack.Run(context.Background(), dir, "fresh.txt")
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
		if !res.Committed {
			t.Fatal("expected the new file to be committed")
		}
		data, err := os.ReadFile(filepath.Join(dir, "fresh.txt"))
		if err != nil {
			t.Fatalf("read created file: %v", err)
		}
		if string(data) != "created content\n" {
			t.Errorf("content = %q, want %q", string(data), "created content\n")
		}
	})
}

func TestTokenEstimateAndActionLabel(t *testing.T) {
	t.Parallel()

	if got := tokenEstimate(0); got != 0 {
		t.Errorf("tokenEstimate(0) = %d, want 0", got)
	}
	if got := tokenEstimate(3); got != 1 {
		t.Errorf("tokenEstimate(3) = %d, want 1", got)
	}
	if got := tokenEstimate(4000); got != 1000 {
		t.Errorf("tokenEstimate(4000) = %d, want 1000", got)
	}

	tests := []struct {
		action authorization.ApprovalAction
		want   string
	}{
		{authorization.ActionExecute, "execute"},
		{authorization.ActionInspect, "inspect"},
		{authorization.ActionCancel, "cancel"},
		{authorization.ActionNone, "none"},
	}
	for _, tt := range tests {
		if got := ActionLabel(tt.action); got != tt.want {
			t.Errorf("ActionLabel(%v) = %q, want %q", tt.action, got, tt.want)
		}
	}
}

// evidenceFixture returns a small MutationEvidence for rendering tests.
func evidenceFixture() diff.MutationEvidence {
	return diff.MutationEvidence{
		TargetFile: "README.md",
		Lines: []diff.PatchLine{
			{Type: diff.MutationDelete, Content: "old line"},
			{Type: diff.MutationAdd, Content: "new line"},
		},
		Added:   1,
		Deleted: 1,
	}
}

// viewportFixture returns a bounded viewport so rendering never truncates.
func viewportFixture() diff.ViewportConfig {
	return diff.ViewportConfig{TermWidth: 100, TermHeight: 24, GutterWidth: 2, PrefixWidth: 1}
}

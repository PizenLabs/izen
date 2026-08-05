package context

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	stdctx "context"
)

func TestCollectorAssemblesInOrder(t *testing.T) {
	c := NewCollector()
	c.Register(ProviderPrompt, NewPromptProvider("hello"))
	c.Register(ProviderEnvironment, NewEnvironmentProvider())
	c.Register(ProviderFilesystem, NewFilesystemProvider(t.TempDir(), 0, true))

	pc, err := c.Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if pc.Len() != 3 {
		t.Fatalf("Len = %d, want 3", pc.Len())
	}
	chunks := pc.Chunks()
	if chunks[0].Provider != ProviderPrompt || chunks[2].Provider != ProviderFilesystem {
		t.Fatalf("order not preserved: %v", pc.Providers())
	}
	if got := pc.Prompt(); got != "hello" {
		t.Fatalf("Prompt() = %q", got)
	}
	if _, ok := pc.Get(ProviderEnvironment); !ok {
		t.Fatal("Get(environment) missing")
	}
	providers := pc.Providers()
	if len(providers) != 3 || providers[0] != ProviderEnvironment {
		t.Fatalf("Providers() not sorted: %v", providers)
	}
}

func TestCollectorWithoutAssumptionsInEmptyWorkspace(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "does-not-exist")
	c := NewCollector()
	c.Register(ProviderPrompt, NewPromptProvider("build a new service"))
	c.Register(ProviderEnvironment, NewEnvironmentProvider())
	c.Register(ProviderFilesystem, NewFilesystemProvider(empty, 0, true))
	c.Register(ProviderRepository, NewRepositoryProvider(empty))

	pc, err := c.Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("Collect must not fail on an empty workspace: %v", err)
	}
	if pc.Len() != 4 {
		t.Fatalf("Len = %d, want 4 (all providers degrade gracefully)", pc.Len())
	}
	fsChunk, ok := pc.Get(ProviderFilesystem)
	if !ok {
		t.Fatal("filesystem provider should contribute a chunk")
	}
	if !fsChunk.Empty() && fsChunk.Content != "" {
		t.Fatalf("filesystem chunk should be empty for missing root, got %q", fsChunk.Content)
	}
	if !fsChunk.Errored() {
		t.Fatal("filesystem chunk should record its absence as metadata, not an error")
	}
	if got := pc.Errors(); len(got) != 0 {
		t.Fatalf("provider absences must not surface as assembly errors: %v", got)
	}
}

func TestCollectorRecordsProviderFailures(t *testing.T) {
	c := NewCollector()
	c.Register(ProviderPrompt, NewPromptProvider("p"))
	boom := ProviderFunc(func(stdctx.Context) (ContextChunk, error) {
		return ContextChunk{}, errors.New("boom")
	})
	c.Register("flaky", boom)
	c.Register(ProviderEnvironment, NewEnvironmentProvider())

	pc, err := c.Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("provider failure must not abort assembly: %v", err)
	}
	if pc.Len() != 2 {
		t.Fatalf("Len = %d, want 2", pc.Len())
	}
	if len(pc.Errors()) != 1 {
		t.Fatalf("Errors = %v, want 1 recorded failure", pc.Errors())
	}
}

func TestCollectorNoProviders(t *testing.T) {
	if _, err := NewCollector().Collect(stdctx.Background()); err == nil {
		t.Fatal("expected error for empty collector")
	}
}

func TestCollectorCancellation(t *testing.T) {
	c := NewCollector()
	c.Register(ProviderPrompt, NewPromptProvider("p"))
	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	cancel()
	if _, err := c.Collect(ctx); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestFilesystemProviderListsFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "b.go"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden", "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewFilesystemProvider(root, 0, true)
	chunk, err := p.Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if chunk.Content != "a.txt\npkg/b.go" {
		t.Fatalf("filesystem content = %q, want a.txt + pkg/b.go only", chunk.Content)
	}

	p2 := NewFilesystemProvider(root, 0, false)
	chunk2, err := p2.Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("Collect (include hidden): %v", err)
	}
	if !strings.Contains(chunk2.Content, ".hidden/secret.txt") {
		t.Fatalf("hidden dir should be listed when skipHidden=false: %q", chunk2.Content)
	}

	p3 := NewFilesystemProvider(root, 1, true)
	chunk3, err := p3.Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("Collect (capped): %v", err)
	}
	if chunk3.GetMeta("truncated") != "true" || strings.Count(chunk3.Content, "\n") != 0 {
		t.Fatalf("capped chunk = %q truncated=%q", chunk3.Content, chunk3.GetMeta("truncated"))
	}
}

func TestFilesystemProviderNonDirRoot(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	chunk, err := NewFilesystemProvider(f, 0, true).Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !chunk.Errored() {
		t.Fatal("non-directory root should be reported as metadata")
	}
}

func TestEnvironmentProvider(t *testing.T) {
	chunk, err := NewEnvironmentProvider().Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !strings.Contains(chunk.Content, "goos=") || !strings.Contains(chunk.Content, "goruntime=") {
		t.Fatalf("environment content missing facts: %q", chunk.Content)
	}
}

func TestRepositoryProviderWithoutGitDir(t *testing.T) {
	chunk, err := NewRepositoryProvider(t.TempDir()).Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("Collect must not fail without a repo: %v", err)
	}
	if !chunk.Empty() || chunk.Content != "" {
		t.Fatalf("repo chunk should be empty, got %q", chunk.Content)
	}
	if chunk.GetMeta("git") != "unavailable" {
		t.Fatalf("repo meta git = %q, want unavailable", chunk.GetMeta("git"))
	}
}

func TestRepositoryProviderHermetic(t *testing.T) {
	root := t.TempDir()
	// Simulate a real checkout with a fake git runner; no git binary needed.
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := func(_ stdctx.Context, _ string, args ...string) (string, error) {
		switch {
		case strings.Contains(strings.Join(args, " "), "status"):
			return " M go.mod", nil
		case strings.Contains(strings.Join(args, " "), "branch"):
			return "main", nil
		case strings.Contains(strings.Join(args, " "), "remote"):
			return "https://example.com/repo.git", nil
		default:
			return "", errors.New("unexpected")
		}
	}
	chunk, err := NewRepositoryProvider(root, WithCmdRunner(runner)).Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, want := range []string{"status= M go.mod", "branch=main", "remote=https://example.com/repo.git"} {
		if !strings.Contains(chunk.Content, want) {
			t.Fatalf("repo content missing %q: %q", want, chunk.Content)
		}
	}
}

func TestPlanningContextImmutabilityAndMerge(t *testing.T) {
	c := NewCollector()
	c.Register(ProviderPrompt, NewPromptProvider("p"))
	c.Register(ProviderEnvironment, NewEnvironmentProvider())
	pc, err := c.Collect(stdctx.Background())
	if err != nil {
		t.Fatal(err)
	}

	other := NewCollector()
	other.Register(ProviderFilesystem, NewFilesystemProvider(t.TempDir(), 0, true))
	other.Register(ProviderPrompt, NewPromptProvider("overridden"))
	oc, err := other.Collect(stdctx.Background())
	if err != nil {
		t.Fatal(err)
	}

	merged := pc.Merge(oc)
	if merged.Len() != 3 {
		t.Fatalf("Merge Len = %d, want 3", merged.Len())
	}
	if got := merged.Prompt(); got != "p" {
		t.Fatalf("Merge kept the second prompt: %q", got)
	}
	if pc.Len() != 2 {
		t.Fatalf("Merge mutated the receiver: Len = %d", pc.Len())
	}

	// Mutating the returned chunk copy must not affect the context.
	chunks := pc.Chunks()
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	chunks[0].Content = "tampered"
	if pc.Prompt() == "tampered" {
		t.Fatal("PlanningContext was mutated through a returned copy")
	}
}

func TestRender(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewCollector()
	c.Register(ProviderPrompt, NewPromptProvider("build a service"))
	c.Register(ProviderFilesystem, NewFilesystemProvider(ws, 0, true))
	pc, err := c.Collect(stdctx.Background())
	if err != nil {
		t.Fatal(err)
	}
	rendered := pc.Render()
	if !strings.Contains(rendered, "### prompt") || !strings.Contains(rendered, "build a service") {
		t.Fatalf("Render output = %q", rendered)
	}
	if !strings.Contains(rendered, "### filesystem") {
		t.Fatalf("Render missing filesystem header: %q", rendered)
	}
	if got := pc.RenderChunk(ProviderPrompt); got != "build a service" {
		t.Fatalf("RenderChunk = %q", got)
	}
}

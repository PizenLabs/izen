package realworld

import (
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/providers"
)

// ── THE EXACT PRODUCTION BENCHMARK (opt-in, real provider) ──────────────────
//
// This arm is the acceptance benchmark named in the task brief:
//
//	$prompt Create a file named testfile.md with the content 'Hello World'.
//
// It drives the SAME production composition as every other realworld arm
// (compose.Wire → autonomy.Driver → RuntimeExecutor) and asserts only the real
// filesystem outcome plus the authority's verdict. It does not simulate the
// provider: the provider is the real configured one (OpenRouter by default, or
// Ollama with IZEN_BENCH_PROVIDER=ollama).
//
// It is opt-in via IZEN_LIVE_FORENSICS=1, exactly like the rest of test/realworld.

func benchmarkProvider(t *testing.T) (ai.Provider, *config.Config) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		t.Fatalf("load config: %v", err)
	}
	choice := strings.ToLower(strings.TrimSpace(os.Getenv("IZEN_BENCH_PROVIDER")))
	if choice == "" {
		choice = cfg.ActiveProviderName()
	}
	if choice == "ollama" {
		cfg.Bindings.Active.Provider = "ollama"
		cfg.Bindings.Active.Model = liveModel
		cfg.Models.SessionModel = ""
		return providers.NewOllamaProvider("http://127.0.0.1:11434/v1", "ollama", liveModel), cfg
	}
	name := choice
	provCfg := cfg.AI.Providers[name]
	baseURL := provCfg.BaseURL
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	apiKey := cfg.ResolveAPIKey(name)
	if apiKey == "" {
		apiKey = os.Getenv(config.EnvVarForProvider(name))
	}
	if apiKey == "" {
		t.Fatalf("no API key for provider %q", name)
	}
	model := cfg.ActiveModelName()
	switch name {
	case "openrouter":
		return providers.NewOpenRouterProvider(apiKey, model, baseURL), cfg
	case "openai":
		return providers.NewOpenAIProvider(apiKey, model), cfg
	case "groq":
		return providers.NewGroqProvider(apiKey, model, baseURL), cfg
	default:
		t.Fatalf("benchmark does not support provider %q", name)
		return nil, nil
	}
}

const benchmarkPrompt = "Create a file named testfile.md with the content 'Hello World'."

func TestBenchmark_CreateExactPrompt(t *testing.T) {
	if os.Getenv(liveOptIn) != "1" {
		t.Skipf("set %s=1 to run the real-world benchmark (needs a real provider)", liveOptIn)
	}
	provider, cfg := benchmarkProvider(t)
	rec := run(t, Task{
		ID:     "bench-create-exact",
		Name:   "BENCHMARK — " + benchmarkPrompt,
		Prompt: benchmarkPrompt,
		Files: map[string]string{
			"go.mod":    "module benchmarkfixture\n\ngo 1.21\n",
			"README.md": "# Sample Project\n\nA small fixture repository.\n",
		},
		AnswerApprovals: true,
		Provider:        provider,
		Config:          cfg,
	})

	// ── The decisive assertions: real bytes on the real filesystem. ─────
	body, ok := rec.ChangedContents["testfile.md"]
	if !ok {
		t.Fatalf("testfile.md was NOT created; delta=%v", rec.Delta)
	}
	if strings.TrimSpace(body) != "Hello World" {
		t.Fatalf("testfile.md content = %q, want %q", body, "Hello World")
	}
	// The authoritative mutation target must have been testfile.md, never a
	// workspace-inferred candidate.
	var mutated bool
	for _, m := range rec.Mutations {
		if strings.Contains(m, "target=testfile.md") && strings.Contains(m, "fs_changed=true") {
			mutated = true
		}
	}
	if !mutated {
		t.Fatalf("the mutation did not reach the requested target; mutations=%v delta=%v", rec.Mutations, rec.Delta)
	}
}

// ── CODE-QUOTED CREATE (the reported regression) ─────────────────────────────
//
// The observed trace named the target inside backticks:
//
//	$prompt Create a new file named `zuru.md` with the content "Hello everyone".
//
// The backticks hid the target from both extraction authorities, so the run
// compiled as a DEFERRED MODIFY, discovered the unrelated markdown files in the
// fixture and parked at a candidate-selection prompt. It must instead be a
// CREATE on zuru.md, and the unrelated files must not be touched.
const benchmarkCodeQuotedPrompt = "Create a new file named `zuru.md` with the content \"Hello everyone\"."

func TestBenchmark_CreateCodeQuotedPrompt(t *testing.T) {
	if os.Getenv(liveOptIn) != "1" {
		t.Skipf("set %s=1 to run the real-world benchmark (needs a real provider)", liveOptIn)
	}
	provider, cfg := benchmarkProvider(t)
	rec := run(t, Task{
		ID:     "bench-create-code-quoted",
		Name:   "BENCHMARK — " + benchmarkCodeQuotedPrompt,
		Prompt: benchmarkCodeQuotedPrompt,
		Files: map[string]string{
			"go.mod":      "module benchmarkfixture\n\ngo 1.21\n",
			"README.md":   "# Sample Project\n\nA small fixture repository.\n",
			"testfile.md": "an unrelated existing file\n",
		},
		AnswerApprovals: true,
		Provider:        provider,
		Config:          cfg,
	})

	// The named target — not a discovered candidate — must be the mutation.
	body, ok := rec.ChangedContents["zuru.md"]
	if !ok {
		t.Fatalf("zuru.md was NOT created; delta=%v mutations=%v boundary=%s/%q",
			rec.Delta, rec.Mutations, rec.BoundaryAction, rec.BoundaryReason)
	}
	if strings.TrimSpace(body) != "Hello everyone" {
		t.Fatalf("zuru.md content = %q, want %q", body, "Hello everyone")
	}
	if rec.BoundaryAction == "clarify" {
		t.Fatalf("an explicitly named target parked at candidate selection: %q options=%v",
			rec.BoundaryReason, rec.BoundaryOptions)
	}
	if got, ok := rec.ChangedContents["testfile.md"]; ok {
		t.Fatalf("an unrelated file was mutated: testfile.md = %q", got)
	}
	var mutated bool
	for _, m := range rec.Mutations {
		if strings.Contains(m, "target=zuru.md") && strings.Contains(m, "fs_changed=true") {
			mutated = true
		}
	}
	if !mutated {
		t.Fatalf("the mutation did not reach the requested target; mutations=%v delta=%v", rec.Mutations, rec.Delta)
	}
}

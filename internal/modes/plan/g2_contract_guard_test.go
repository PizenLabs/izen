package plan

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/discovery/recon"
)

func TestG2TruncatedProviderResponseStopsBeforePlanParsing(t *testing.T) {
	e := NewEngine(NewPlanStore())
	e.SetProvider(func(context.Context, ai.Request) (*ai.Response, error) {
		return &ai.Response{
			Content:      `{"architectural_strategy":"partial","atomic_tasks":[{"task_id":1,`,
			FinishReason: "length",
			Truncated:    true,
		}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "produce a plan", "vendor/model:free")
	if tasks != nil {
		t.Fatalf("truncated response produced fallback tasks: %+v", tasks)
	}
	if !errors.Is(err, ai.ErrOutputTruncated) {
		t.Fatalf("error = %v, want typed output truncation", err)
	}
}

func TestG2VanillaArchetypeSuppressesLanguageFallbacks(t *testing.T) {
	ledger := "cmd/api/main.go:1:1: no required module provides package github.com/example/pkg"
	task := Task{Type: "FILE_MUTATE", Target: "index.html"}

	forced := ForceShellExecOnCompileErrorForArchetype([]Task{task}, ledger, ledger, recon.VANILLA_WEB)
	for _, got := range forced {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(got.Target)), "go ") {
			t.Fatalf("VANILLA_WEB fallback contains Go command: %+v", got)
		}
	}

	validated := ValidateShellExecCommandsForArchetype([]Task{{Type: "SHELL_EXEC", Target: "go mod tidy"}}, ledger, recon.VANILLA_WEB)
	if len(validated) != 0 {
		t.Fatalf("VANILLA_WEB validation retained incompatible task: %+v", validated)
	}
	if ArchetypeAllowsCommand(recon.VANILLA_WEB, `sh -c "go mod tidy"`) {
		t.Fatal("VANILLA_WEB guard admitted a Go command through a shell wrapper")
	}
}

func TestG2SmallModelUsesCompactInteractionContract(t *testing.T) {
	var captured ai.Request
	e := NewEngine(NewPlanStore())
	e.SetProvider(func(_ context.Context, req ai.Request) (*ai.Response, error) {
		captured = req
		return &ai.Response{Content: `{"architectural_strategy":"x","atomic_tasks":[{"task_id":1,"strategy":"FILE_MUTATE","file":"index.html","description":"fix","rationale":"why","solution":"done"}]}`, FinishReason: "stop"}, nil
	})

	_, err := e.ProcessFromLedger(context.Background(), "", "fix the page", "vendor/nano:free")
	if err != nil {
		t.Fatalf("ProcessFromLedger: %v", err)
	}
	if captured.InteractionContract != "structured_completion" {
		t.Fatalf("interaction contract = %q, want structured_completion", captured.InteractionContract)
	}
	if captured.Contract == nil || captured.Contract.PromptProfile != "compact" {
		t.Fatalf("descriptor = %+v, want compact descriptor", captured.Contract)
	}
	if len(captured.Messages) == 0 || len(captured.Messages[0].Content) >= 1800 {
		t.Fatalf("small-model system prompt was not compact: %d chars", len(captured.Messages[0].Content))
	}
	if strings.Contains(captured.Messages[0].Content, "FORBIDDEN COMMANDS") {
		t.Fatal("small-model prompt contains the legacy negative command block")
	}
	for i, message := range captured.Messages {
		lower := strings.ToLower(message.Content)
		for _, forbidden := range []string{"forbidden", "do not", "never"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("small-model message %d contains negative contract wording %q: %s", i, forbidden, message.Content)
			}
		}
	}
}

func TestG2AuthoritativeLengthNeverReachesStructuralParser(t *testing.T) {
	e := NewEngine(NewPlanStore())
	e.SetProvider(func(context.Context, ai.Request) (*ai.Response, error) {
		return &ai.Response{
			Content:      `{"architectural_strategy":"complete bytes","atomic_tasks":[{"task_id":1,"strategy":"FILE_MUTATE","file":"index.html","description":"fix","rationale":"why","solution":"done"}]}`,
			FinishReason: "length",
			Truncated:    true,
		}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "fix the page", "vendor/model:free")
	if tasks != nil {
		t.Fatalf("authoritative length response produced tasks: %+v", tasks)
	}
	if !errors.Is(err, ai.ErrOutputTruncated) {
		t.Fatalf("error = %v, want typed output truncation", err)
	}
}

func TestG2InvestigationArchetypeMarkerWinsOverDiskDetection(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(root+"/go.mod", []byte("module example.test/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/index.html", []byte("<html><body>hi</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(NewPlanStore())
	e.SetRootPath(root)
	e.SetProvider(func(context.Context, ai.Request) (*ai.Response, error) {
		return &ai.Response{Content: `{"architectural_strategy":"frontend","atomic_tasks":[{"task_id":1,"strategy":"FILE_MUTATE","file":"index.html","description":"fix","rationale":"why","solution":"done"},{"task_id":2,"strategy":"SHELL_EXEC","file":"go mod tidy","description":"tidy","rationale":"why","solution":"done"}]}`}, nil
	})

	ledger := "ARCHETYPE: VANILLA_WEB\nproblem: frontend layout\n"
	tasks, err := e.ProcessFromLedger(context.Background(), ledger, "investigate the frontend layout", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger: %v", err)
	}
	for _, task := range tasks {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(task.Target)), "go ") {
			t.Fatalf("ledger archetype was overwritten and emitted Go task: %+v", task)
		}
	}
}

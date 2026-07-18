package plan

import (
	"strings"
	"testing"
)

func TestIsLocalModel(t *testing.T) {
	local := []string{
		"qwen2.5-coder:7b",
		"qwen2.5-coder:14b",
		"qwen2:7b",
		"llama3.1:8b",
		"codellama:13b",
		"phi3:mini",
		"gemma2:2b",
		"mistral:7b",
		"deepseek-coder:6.7b",
	}
	for _, m := range local {
		if !IsLocalModel(m) {
			t.Errorf("expected %q to be detected as local", m)
		}
	}

	cloud := []string{
		"claude-sonnet-4",
		"gpt-4o",
		"gpt-4o-mini",
		"gemini-1.5-pro",
		"",
	}
	for _, m := range cloud {
		if IsLocalModel(m) {
			t.Errorf("expected %q to NOT be detected as local", m)
		}
	}
}

func TestTruncateLedger_NoOpWhenSmall(t *testing.T) {
	small := "cmd/api/main.go:7:5: no required module provides package github.com/foo/bar"
	if got := TruncateLedger(small, MaxLedgerChars); got != small {
		t.Fatalf("expected small ledger unchanged, got %q", got)
	}
}

func TestTruncateLedger_EnforcesCeiling(t *testing.T) {
	var b strings.Builder
	b.WriteString("cmd/api/main.go:7:5: no required module provides package github.com/foo/bar\n")
	for i := 0; i < 200; i++ {
		b.WriteString("REDUNDANT STACK FRAME: goroutine 1 [running] in main.loop at /usr/local/go/src/runtime/proc.go:250 +0x10\n")
	}
	ledger := b.String()
	got := TruncateLedger(ledger, MaxLedgerChars)
	if len(got) > MaxLedgerChars {
		t.Fatalf("truncated ledger exceeded ceiling: %d > %d", len(got), MaxLedgerChars)
	}
	// Core error line must survive.
	if !strings.Contains(got, "no required module provides package github.com/foo/bar") {
		t.Fatalf("core error line lost during truncation:\n%s", got)
	}
}

func TestTruncateLedger_KeepsHypothesis(t *testing.T) {
	var b strings.Builder
	b.WriteString("hypothesis: missing go.mod module declaration\n")
	for i := 0; i < 100; i++ {
		b.WriteString("verbose env dump GOPATH=/root/go GOROOT=/usr/local/go PATH=...\n")
	}
	got := TruncateLedger(b.String(), MaxLedgerChars)
	if !strings.Contains(got, "hypothesis: missing go.mod module declaration") {
		t.Fatalf("hypothesis status lost during truncation:\n%s", got)
	}
}

func TestCoreErrorLine(t *testing.T) {
	ledger := "building project...\ncmd/api/main.go:7:5: no required module provides package github.com/foo/bar\nmore logs\n"
	if got := CoreErrorLine(ledger); !strings.Contains(got, "no required module provides package github.com/foo/bar") {
		t.Fatalf("CoreErrorLine returned %q", got)
	}
}

func TestExtractConclusionFromLedger(t *testing.T) {
	ledger := `### ANALYTICAL PACKETS (sequential, ID-addressed)
Total packets: 5

[PKT-1] kind=problem title="Investigation problem statement"
  import path error in cmd/api/main.go

[PKT-4] kind=root_cause title="Derived root cause"
  The code references docker/docker/client which is the legacy path

[PKT-5] kind=conclusion title="Investigation conclusion"
  Use github.com/moby/moby/client instead of github.com/docker/docker/client. Update the import path in cmd/api/main.go and run go mod tidy.

[PKT-6] kind=evidence title="Evidence [test]"
  test output`

	c := ExtractConclusionFromLedger(ledger)
	if c == "" {
		t.Fatal("expected conclusion to be extracted")
	}
	if !strings.Contains(c, "github.com/moby/moby/client") {
		t.Fatalf("expected conclusion to reference corrected path, got: %q", c)
	}
}

func TestExtractConclusionFromLedgerEmpty(t *testing.T) {
	ledger := `### ANALYTICAL PACKETS (sequential, ID-addressed)
Total packets: 2

[PKT-1] kind=problem title="Investigation problem statement"
  something went wrong`

	c := ExtractConclusionFromLedger(ledger)
	if c != "" {
		t.Fatalf("expected empty conclusion, got: %q", c)
	}
}

func TestIsCompilationOrDependencyError(t *testing.T) {
	if !IsCompilationOrDependencyError("cmd/api/main.go:7:5: no required module provides package github.com/foo/bar") {
		t.Fatal("expected compile/dep error to be detected")
	}
	if IsCompilationOrDependencyError("the user wants to refactor the auth handler for clarity") {
		t.Fatal("expected prose to NOT be a compile/dep error")
	}
}

func TestFastTrackPrompt(t *testing.T) {
	p := FastTrackPrompt("cmd/api/main.go:7:5: no required module provides package github.com/foo/bar", "")
	if !strings.Contains(p, "0 explicit code TODOs") {
		t.Fatalf("fast-track prompt missing 0-TODO framing: %q", p)
	}
	if !strings.Contains(p, "cmd/api/main.go:7:5") {
		t.Fatalf("fast-track prompt missing core error: %q", p)
	}
	if !strings.Contains(p, "SHELL_EXEC") || !strings.Contains(p, "shell commands ONLY") {
		t.Fatalf("fast-track prompt missing shell-exec instruction: %q", p)
	}
	if !strings.Contains(p, "relative/path/to/file.go") {
		t.Fatalf("fast-track prompt missing placeholder prohibition: %q", p)
	}
}

func TestFastTrackPromptWithConclusion(t *testing.T) {
	conclusion := "The correct import path is github.com/moby/moby/client, not the legacy docker/docker/client"
	p := FastTrackPrompt("cmd/api/main.go:7:5: no required module provides package github.com/docker/docker/client", conclusion)
	if !strings.Contains(p, "already diagnosed") {
		t.Fatalf("fast-track prompt missing conclusion cross-reference: %q", p)
	}
	if !strings.Contains(p, "github.com/moby/moby/client") {
		t.Fatalf("fast-track prompt missing conclusion injection: %q", p)
	}
}

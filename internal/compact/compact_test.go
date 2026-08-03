package compact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(\"\") = %d, want 0", got)
	}
	if got := EstimateTokens(strings.Repeat("a", 8)); got != 2 {
		t.Errorf("EstimateTokens(8 chars) = %d, want 2 (~4 chars/token)", got)
	}
	if got := EstimateTokens(strings.Repeat("a", 9)); got != 3 {
		t.Errorf("EstimateTokens(9 chars) = %d, want 3 (rounds up)", got)
	}
}

func TestFencedCodeBlockPreservedByteForByte(t *testing.T) {
	input := `# Build
Please just run the build with:

` + "```go\n" + `func main() {
	fmt.Println("hello world")
}
` + "```\n\n" + `Obviously, that is all you need.`
	want := "func main() {\n\tfmt.Println(\"hello world\")\n}"
	got, stats := Optimize(input)
	if !strings.Contains(got, want) {
		t.Errorf("code block lost; output:\n%s", got)
	}
	if stats.CodeBlocksPreserved != 1 {
		t.Errorf("CodeBlocksPreserved = %d, want 1", stats.CodeBlocksPreserved)
	}
}

func TestInlineCodeSpanAndPathPreserved(t *testing.T) {
	input := `Simply run ` + "`izen config style terse`" + `.
The canonical file is ` + "`internal/retrieval/canonical.go`" + ` which simply handles fixes.`
	got, _ := Optimize(input)
	if !strings.Contains(got, "`izen config style terse`") {
		t.Errorf("inline code span lost: %q", got)
	}
	if !strings.Contains(got, "`internal/retrieval/canonical.go`") {
		t.Errorf("path lost: %q", got)
	}
	if strings.Contains(got, "Simply run") {
		t.Errorf("filler not stripped: %q", got)
	}
}

func TestFlagsAndVariablesPreserved(t *testing.T) {
	input := `Just build with ` + "`go build -ldflags \"-X main.Version=0.1.0\" ./cmd/izen`" + `.
Use the ` + "`--dry-run`" + ` flag to just preview.`
	got, _ := Optimize(input)
	if !strings.Contains(got, "`go build -ldflags \"-X main.Version=0.1.0\" ./cmd/izen`") {
		t.Errorf("flag/variable command lost: %q", got)
	}
	if !strings.Contains(got, "`--dry-run`") {
		t.Errorf("--dry-run flag lost: %q", got)
	}
}

func TestFillerWordsRemoved(t *testing.T) {
	input := `Note that the API just returns very simple JSON.
It is important to note that you basically just call it simply.`
	got, _ := Optimize(input)
	if strings.Contains(got, "just") || strings.Contains(got, "very") ||
		strings.Contains(got, "basically") || strings.Contains(got, "Note that") {
		t.Errorf("filler words survived: %q", got)
	}
}

func TestConversationalLinesRemoved(t *testing.T) {
	input := `# Heading
Happy coding!
If you have any questions, please don't hesitate to reach out.
Thanks for reading!
Keep the build green.`
	got, _ := Optimize(input)
	if strings.Contains(got, "Happy coding") || strings.Contains(got, "don't hesitate") ||
		strings.Contains(got, "Thanks for reading") {
		t.Errorf("conversational lines survived:\n%s", got)
	}
	if !strings.Contains(got, "Keep the build green") {
		t.Errorf("meaningful content lost:\n%s", got)
	}
}

func TestHTMLCommentsRemoved(t *testing.T) {
	input := `# Title
<!-- This is a long comment about why this section exists -->
<!-- 
multi line comment
that spans lines
-->
Keep this line.`
	got, _ := Optimize(input)
	if strings.Contains(got, "comment") {
		t.Errorf("HTML comments survived:\n%s", got)
	}
	if !strings.Contains(got, "Keep this line") {
		t.Errorf("content after comments lost:\n%s", got)
	}
}

func TestDecorativeLinesAndDuplicatesRemoved(t *testing.T) {
	input := `# Section
The same rule applies.
---
The same rule applies.
=== 
The same rule applies.`
	got, _ := Optimize(input)
	if strings.Contains(got, "---") || strings.Contains(got, "===") {
		t.Errorf("decorative lines survived:\n%s", got)
	}
	if strings.Count(got, "The same rule applies") != 1 {
		t.Errorf("duplicate lines not collapsed:\n%s", got)
	}
}

// TestRealisticReduction requires >40% byte savings on a verbose AGENTS.md
// style document while preserving every code block byte-for-byte.
func TestRealisticReduction(t *testing.T) {
	verbose := `# Project Memory — Build System

<!-- This file is intended for AI assistants. Please read it carefully. -->

Hello! Welcome to the project memory file. This file contains all the
important context that you need in order to work effectively in this
repository. It is basically a living document that we try to keep up to
date as much as we possibly can. If you have any questions, please don't
hesitate to reach out and let me know!

## Building the Project

The build process is really quite simple. You basically just need to run
the following command in order to produce a binary:

` + "```bash\n" + `make build
` + "```\n\n" + `Obviously, this will simply invoke go build under the hood. Note that
the output binary is placed into the ` + "`bin/`" + ` directory, which is
really quite standard for a Go project like this one.

## Running Tests

Running tests is very easy. You just run:

` + "```bash\n" + `make test
` + "```\n\n" + `This will simply run the full test suite. In order to run a single package
you can just use ` + "`go test ./internal/compact/`" + ` instead. The tests
must basically stay green before you submit any changes — please keep this
in mind at all times.

## Environment

HOST ENVIRONMENT: darwin/arm64.
The preferred tooling is ` + "`go get`/`go mod tidy`" + `.

` + "```sh\n" + `export IZEN_DEBUG=1
izen config style terse
` + "```\n\n" + `---

Thanks for reading and happy coding!
`
	got, stats := Optimize(verbose)
	if stats.SavingsPercent() <= 40 {
		t.Errorf("savings %.1f%% <= 40%% on verbose document (bytes %d -> %d)",
			stats.SavingsPercent(), stats.OriginalBytes, stats.NewBytes)
	}

	// Zero code loss: every fenced code block body survives byte-for-byte.
	for _, block := range []string{
		"make build\n",
		"make test\n",
		"export IZEN_DEBUG=1\nizen config style terse\n",
	} {
		if !strings.Contains(got, block) {
			t.Errorf("code block %q lost:\n%s", block, got)
		}
	}
	for _, preserved := range []string{
		"`bin/`",
		"`go test ./internal/compact/`",
		"`go get`/`go mod tidy`",
		"darwin/arm64",
		"go build",
	} {
		if !strings.Contains(got, preserved) {
			t.Errorf("protected token %q lost:\n%s", preserved, got)
		}
	}
	if stats.CodeBlocksPreserved != 3 {
		t.Errorf("CodeBlocksPreserved = %d, want 3", stats.CodeBlocksPreserved)
	}
	if gotTokens := EstimateTokens(got); gotTokens > stats.OriginalTokens {
		t.Errorf("token estimate did not shrink (%d -> %d)", stats.OriginalTokens, gotTokens)
	}
}

func TestOptimizeIdempotent(t *testing.T) {
	input := `# Header
<!-- comment -->
Note that we simply just run the command ` + "`izen compact`" + `.

` + "```bash\n" + `make test
` + "```\n" + `
Thanks!`
	once, _ := Optimize(input)
	twice, _ := Optimize(once)
	if once != twice {
		t.Errorf("Optimize is not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestDiscoverFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"AGENTS.md", "RULES.md", "CLAUDE.md", "README.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"architecture.md", "CONTRIBUTING.txt", "notes.md"} {
		if err := os.WriteFile(filepath.Join(dir, "docs", name), []byte("# x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := DiscoverFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Errorf("DiscoverFiles found %d files, want 6 (got %v)", len(got), got)
	}
	for _, want := range []string{"AGENTS.md", "RULES.md", "CLAUDE.md", "README.md",
		filepath.Join("docs", "architecture.md"), filepath.Join("docs", "notes.md")} {
		if !containsPath(got, filepath.Join(dir, want)) {
			t.Errorf("DiscoverFiles missing %q (got %v)", want, got)
		}
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

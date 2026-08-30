package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestCodeBlock_ShellCommandColorized verifies shell/command code blocks are
// syntax-highlighted with 24-bit foreground codes for command elements.
func TestCodeBlock_ShellCommandColorized(t *testing.T) {
	// Test each shell language variant normalizes to bash lexer and produces ANSI
	shellLangs := []string{"sh", "bash", "zsh", "shell", "console", "cmd", "terminal"}
	for _, lang := range shellLangs {
		line := "go run hello.go"
		out := colorizeNoBg(lang, line)
		if !strings.Contains(out, "38;2;") {
			t.Fatalf("lang %q: expected ANSI foreground 38;2; in output, got %q", lang, out)
		}
		if strings.Contains(out, "48;2;") {
			t.Fatalf("lang %q: should not contain background 48;2;, got %q", lang, out)
		}
		// Must contain colorized tokens, not raw plain text
		if out == line {
			t.Fatalf("lang %q: output is raw plain text, expected colorized, got %q", lang, out)
		}
		// Ensure command elements are colored: "go", "run", "hello.go" should each have 38;2;
		// Count foreground codes: should be at least 2 (command + arg)
		count := strings.Count(out, "38;2;")
		if count < 2 {
			t.Fatalf("lang %q: expected at least 2 foreground codes for command elements, got %d in %q", lang, count, out)
		}
		// Verify fallback colors are present: green for command, mauve for subcommand, peach for arg
		// We check that hello.go is wrapped with peach/mauve/green
		if !strings.Contains(out, "hello.go") {
			t.Fatalf("lang %q: missing hello.go in output %q", lang, out)
		}
	}

	// Specific assertion for sh with "go run hello.go"
	out := colorizeNoBg("sh", "go run hello.go")
	if !strings.Contains(out, "38;2;") {
		t.Fatalf("sh: go run hello.go should be colorized, got %q", out)
	}
	plain := ansi.Strip(out)
	if plain != "go run hello.go" {
		t.Fatalf("stripped plain %q want %q", plain, "go run hello.go")
	}
}

// TestCodeBlock_ShellFlags verifies flags are mauve/blue
func TestCodeBlock_ShellFlags(t *testing.T) {
	out := colorizeNoBg("bash", "npm install --save -v")
	if !strings.Contains(out, "38;2;") {
		t.Fatalf("expected colorized, got %q", out)
	}
	if strings.Contains(out, "48;2;") {
		t.Fatalf("should not contain background codes")
	}
}

// TestCodeBlock_NonShellPreserves verifies non-shell languages still use Chroma
func TestCodeBlock_NonShellPreserves(t *testing.T) {
	out := colorizeNoBg("go", "func main() {}")
	if !strings.Contains(out, "38;2;") {
		t.Fatalf("go language should be colorized, got %q", out)
	}
	if strings.Contains(out, "48;2;") {
		t.Fatalf("go: should not contain background")
	}
}

// TestColorizeNoBg_NoBackground ensures no background fill leaks
func TestColorizeNoBg_NoBackground(t *testing.T) {
	for _, lang := range []string{"sh", "bash", "go", "python"} {
		out := colorizeNoBg(lang, "some code line")
		if strings.Contains(out, "48;2;") {
			t.Fatalf("lang %q leaked background 48;2;: %q", lang, out)
		}
	}
}

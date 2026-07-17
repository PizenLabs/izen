package context

import (
	"strings"
	"testing"
)

func TestSanitizeLedger(t *testing.T) {
	raw := "\x1b[31mERROR\x1b[0m\n" +
		"time=12:00:00 level=info pulling image sha256:abc\n" +
		"rootless docker not found\n" +
		"cmd/api/main.go:42\n" +
		"cmd/api/main.go:42\n" +
		"cmd/api/main.go:42\n" +
		"github.com/docker/docker/client\n" +
		"\n\n\n" +
		"goroutine 1 [running]:\n" +
		"some/pkg.Foo\n" +
		"some/pkg.Foo\n"

	out := SanitizeLedger(raw)

	if strings.Contains(out, "\x1b[") {
		t.Fatalf("ANSI not stripped: %q", out)
	}
	// Diagnostic signal must be PRESERVED — not dropped — to keep state
	// continuity between modes.
	if !strings.Contains(out, "time=12:00:00") {
		t.Fatalf("runtime log line dropped (signal lost): %q", out)
	}
	if !strings.Contains(out, "rootless docker not found") {
		t.Fatalf("environment blocker dropped (signal lost): %q", out)
	}
	if !strings.Contains(out, "github.com/docker/docker/client") {
		t.Fatalf("signal dependency dropped: %q", out)
	}
	// Stack frames are preserved (no longer collapsed) so the full call path
	// reaches the next mode.
	if n := strings.Count(out, "cmd/api/main.go:42"); n != 3 {
		t.Fatalf("expected all 3 stack frames preserved, got %d: %q", n, out)
	}
	// 3+ blank lines collapsed to a single blank line.
	if strings.Contains(out, "\n\n\n") {
		t.Fatalf("excess blank lines not collapsed: %q", out)
	}
}

func TestSanitizeLedgerEmpty(t *testing.T) {
	if got := SanitizeLedger(""); got != "" {
		t.Fatalf("expected empty passthrough, got %q", got)
	}
}

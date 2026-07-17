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
	if strings.Contains(out, "time=12:00:00") || strings.Contains(out, "pulling image") {
		t.Fatalf("runtime log noise not stripped: %q", out)
	}
	// Repeated stack frames collapsed to a single occurrence.
	if n := strings.Count(out, "cmd/api/main.go:42"); n != 1 {
		t.Fatalf("expected duplicated stack frame collapsed to 1, got %d: %q", n, out)
	}
	if !strings.Contains(out, "github.com/docker/docker/client") {
		t.Fatalf("signal dependency dropped: %q", out)
	}
}

func TestSanitizeLedgerEmpty(t *testing.T) {
	if got := SanitizeLedger(""); got != "" {
		t.Fatalf("expected empty passthrough, got %q", got)
	}
}

package signal

import (
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	s := New(SignalDepMissing, "test.source", map[string]string{"dependency": "github.com/foo/bar"})
	if s.Kind != SignalDepMissing {
		t.Fatalf("Kind = %q, want %q", s.Kind, SignalDepMissing)
	}
	if s.Source != "test.source" {
		t.Fatalf("Source = %q, want test.source", s.Source)
	}
	if s.PayloadValue("dependency") != "github.com/foo/bar" {
		t.Fatalf("payload dependency missing")
	}
	if s.Timestamp.IsZero() {
		t.Fatal("Timestamp not set")
	}
}

func TestHasKindAndFirst(t *testing.T) {
	signals := []Signal{
		New(SignalDepMissing, "a", nil),
		New(SignalBuildHalted, "b", nil),
	}
	if !HasKind(signals, SignalDepMissing) {
		t.Fatal("expected dep.missing present")
	}
	if HasKind(signals, SignalExecutionFailed) {
		t.Fatal("expected no execution.failed")
	}
	if f := First(signals, SignalBuildHalted); f == nil || f.Source != "b" {
		t.Fatalf("First(build.halted) = %+v, want source b", f)
	}
	if First(signals, SignalSymbolUndefined) != nil {
		t.Fatal("expected nil for absent kind")
	}
	if HasKind(nil, SignalDepMissing) || First(nil, SignalDepMissing) != nil {
		t.Fatal("nil slice must be safe")
	}
}

func TestDetect_Empty(t *testing.T) {
	if got := Detect("", "src"); got != nil {
		t.Fatalf("expected nil for empty content, got %d", len(got))
	}
}

func TestDetect_UndefinedSymbol(t *testing.T) {
	for _, in := range []string{
		"cmd/api/main.go:24:2: undefined: Log",
		"# go-template/cmd/api\ncmd/api/main.go:24:2: undefined: Log",
		"cmd/api/main.go:24: undefined: Log",
	} {
		got := Detect(in, "src")
		s := First(got, SignalSymbolUndefined)
		if s == nil {
			t.Fatalf("expected symbol.undefined for %q", in)
		}
		if s.PayloadValue("symbol") != "Log" {
			t.Fatalf("symbol = %q, want Log", s.PayloadValue("symbol"))
		}
		if s.PayloadValue("file") != "cmd/api/main.go" {
			t.Fatalf("file = %q", s.PayloadValue("file"))
		}
	}
}

func TestDetect_CanonicalMismatch(t *testing.T) {
	for _, in := range []string{
		"module declares its path as: \"example.com/new\" but was required as: \"example.com/old\"",
		"module declares its path as: example.com/new\nbut was required as: example.com/old",
	} {
		got := Detect(in, "src")
		s := First(got, SignalImportMismatch)
		if s == nil {
			t.Fatalf("expected import.mismatch for %q", in)
		}
		if s.PayloadValue("new_path") != "example.com/new" {
			t.Fatalf("new_path = %q", s.PayloadValue("new_path"))
		}
		if s.PayloadValue("old_path") != "example.com/old" {
			t.Fatalf("old_path = %q", s.PayloadValue("old_path"))
		}
	}
}

func TestDetect_DepMissing(t *testing.T) {
	cases := []string{
		"no required module provides package github.com/foo/bar",
		"no required modul",
		"missing Go module github.com/foo/bar",
		"finding module for package github.com/foo/bar",
		"main.go:7:5: no required",
		"cmd/api/main.go:12:3: could not import github.com/x",
	}
	for _, in := range cases {
		if !HasKind(Detect(in, "src"), SignalDepMissing) {
			t.Fatalf("expected dep.missing for %q", in)
		}
	}
}

func TestDetect_DepMissingNegative(t *testing.T) {
	// Plain *.go coordinate without an import/parse indicator is NOT a
	// dependency error (it is a logic bug).
	got := Detect("cmd/api/main.go:12:3: x := 5", "src")
	if HasKind(got, SignalDepMissing) {
		t.Fatalf("unexpected dep.missing for %q: %+v", "cmd/api/main.go:12:3: x := 5", got)
	}
	if HasKind(got, SignalBuildHalted) {
		t.Fatalf("unexpected build.halted for %q", "cmd/api/main.go:12:3: x := 5")
	}
}

func TestDetect_BlockerToken(t *testing.T) {
	in := "## REMOTE DEPENDENCY BLOCKER (lx bypassed): github.com/moby/moby/client"
	got := Detect(in, "src")
	s := First(got, SignalDepMissing)
	if s == nil {
		t.Fatalf("expected dep.missing from blocker token")
	}
	if s.PayloadValue("blocker") != "true" {
		t.Fatalf("blocker payload missing: %+v", s.Payload)
	}
	if s.PayloadValue("dependency") != "github.com/moby/moby/client" {
		t.Fatalf("dependency = %q", s.PayloadValue("dependency"))
	}
}

func TestDetect_BlockerTokenMarkdownLink(t *testing.T) {
	in := "## REMOTE DEPENDENCY BLOCKER (lx bypassed): [github.com/docker/docker/client](https://github.com/docker/docker/client)"
	s := First(Detect(in, "src"), SignalDepMissing)
	if s == nil || s.PayloadValue("dependency") != "github.com/docker/docker/client" {
		t.Fatalf("markdown-link dependency extraction failed: %+v", s)
	}
}

func TestDetect_BuildHalted(t *testing.T) {
	for _, in := range []string{
		"[build failed]",
		"build failed: undefined: Router",
		"syntax error: unexpected newline",
		"FAIL\tgithub.com/example/svc [build failed]",
		"exit status 2",
	} {
		if !HasKind(Detect(in, "src"), SignalBuildHalted) {
			t.Fatalf("expected build.halted for %q", in)
		}
	}
}

func TestDetect_ExecutionFailed(t *testing.T) {
	for _, in := range []string{
		"FAIL\tpkg [build failed] failed tests: 1\n",
		"panic: runtime error: nil pointer dereference",
		"goroutine stack trace",
	} {
		if !HasKind(Detect(in, "src"), SignalExecutionFailed) {
			t.Fatalf("expected execution.failed for %q", in)
		}
	}
}

func TestDetect_MultiSignalSet(t *testing.T) {
	in := "cmd/api/main.go:24:2: undefined: Log\nno required module provides package github.com/foo/bar\nFAIL\tpkg [build failed]"
	got := Detect(in, "src")
	if !HasKind(got, SignalSymbolUndefined) {
		t.Fatal("expected symbol.undefined")
	}
	if !HasKind(got, SignalDepMissing) {
		t.Fatal("expected dep.missing")
	}
	if !HasKind(got, SignalBuildHalted) {
		t.Fatal("expected build.halted")
	}
	// At most one signal per kind.
	counts := map[SignalKind]int{}
	for _, s := range got {
		counts[s.Kind]++
	}
	for k, n := range counts {
		if n != 1 {
			t.Fatalf("kind %s emitted %d times, want 1", k, n)
		}
	}
}

func TestDetect_NoSignalForProse(t *testing.T) {
	in := "the user wants to refactor the auth handler for clarity"
	if got := Detect(in, "src"); len(got) != 0 {
		t.Fatalf("expected no signals for prose, got %+v", got)
	}
}

func TestIsCompilationOrDependency(t *testing.T) {
	if !IsCompilationOrDependency(Detect("cmd/api/main.go:7:5: no required module provides package github.com/foo/bar", "src")) {
		t.Fatal("expected dep blocker to be compilation/dependency")
	}
	if !IsCompilationOrDependency(Detect("cmd/api/main.go:24:2: undefined: Log", "src")) {
		t.Fatal("expected undefined symbol to be compilation/dependency")
	}
	if !IsCompilationOrDependency(Detect("build failed: undefined: Router", "src")) {
		t.Fatal("expected build failure to be compilation/dependency")
	}
	if IsCompilationOrDependency(Detect("the user wants to refactor the auth handler for clarity", "src")) {
		t.Fatal("expected prose to NOT be compilation/dependency")
	}
}

func TestHasCompileFailureExcludesRuntime(t *testing.T) {
	// A pure runtime panic is an execution failure, not a compile failure.
	got := Detect("panic: runtime error: nil pointer dereference", "src")
	if HasCompileFailure(got) {
		t.Fatalf("runtime panic must not be a compile failure: %+v", got)
	}
	if !IsCompilationOrDependency(got) {
		t.Fatalf("runtime panic must still be a compilation/dependency-class failure")
	}
}

func TestDetect_SourceAttribution(t *testing.T) {
	got := Detect("build failed", "plan.ledger")
	if len(got) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(got))
	}
	if got[0].Source != "plan.ledger" {
		t.Fatalf("Source = %q", got[0].Source)
	}
}

func TestExtractDependencyToken(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"github.com/moby/moby/client", "github.com/moby/moby/client"},
		{"[github.com/docker/docker/client](https://github.com/docker/docker/client)", "github.com/docker/docker/client"},
		{"[g...](https://github.com/docker/docker/client)", "g..."},
		{"", ""},
	}
	for _, c := range cases {
		if got := extractDependencyToken(c.in); got != c.want {
			t.Fatalf("extractDependencyToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDetect_CanonicalMismatchAndDepMissing(t *testing.T) {
	// "module declares its path as" is both an import.mismatch signal and a
	// dep-class marker; the classifier must emit the import.mismatch signal.
	in := "go: example.com/app@v1: module declares its path as: example.com/actual\nbut was required as: example.com/legacy"
	got := Detect(in, "src")
	if !HasKind(got, SignalImportMismatch) {
		t.Fatalf("expected import.mismatch: %+v", got)
	}
	if !strings.HasPrefix(got[0].Kind.String(), "import") {
		t.Fatalf("dedupe order changed: %+v", got)
	}
}

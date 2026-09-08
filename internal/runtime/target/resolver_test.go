package target

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitEnv pins an identity so commits succeed without global git config.
var gitEnv = []string{
	"GIT_AUTHOR_NAME=test",
	"GIT_AUTHOR_EMAIL=test@example.com",
	"GIT_COMMITTER_NAME=test",
	"GIT_COMMITTER_EMAIL=test@example.com",
}

// requireGit skips the test when the git binary is unavailable.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

// runGit runs git in dir, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx := t.Context()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// initGitRepo creates a git repository in dir with an initial commit
// containing the given files (relative paths).
func initGitRepo(t *testing.T, dir string, files ...string) {
	t.Helper()
	runGit(t, dir, "init", "-b", "main")
	for _, f := range files {
		path := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", f, err)
		}
		if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")
}

// writeFile creates a file relative to dir, failing the test on error.
func writeFile(t *testing.T, dir, rel string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// assertRef verifies every field of a resolved TargetRef.
func assertRef(t *testing.T, ref *TargetRef, raw, canonical string, exists, tracked bool, source ResolutionSource) {
	t.Helper()
	if ref == nil {
		t.Fatal("expected a non-nil TargetRef")
	}
	if ref.Raw != raw {
		t.Fatalf("Raw: expected %q, got %q", raw, ref.Raw)
	}
	if ref.Canonical != canonical {
		t.Fatalf("Canonical: expected %q, got %q", canonical, ref.Canonical)
	}
	if ref.Exists != exists {
		t.Fatalf("Exists: expected %v, got %v", exists, ref.Exists)
	}
	if ref.Tracked != tracked {
		t.Fatalf("Tracked: expected %v, got %v", tracked, ref.Tracked)
	}
	if ref.Source != source {
		t.Fatalf("Source: expected %v, got %v", source, ref.Source)
	}
}

func TestGitIndexOverride(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initGitRepo(t, dir, "README.md")

	r := NewTargetResolver()
	ref, err := r.Resolve(dir, "readme.md")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertRef(t, ref, "readme.md", "README.md", true, true, ResolutionVCS)
}

func TestGitIndexCaseInsensitiveAcrossTree(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initGitRepo(t, dir, "src/README.md", "docs/Guide.md")

	tests := []struct {
		name      string
		workdir   string
		raw       string
		canonical string
	}{
		{"basename from repo root", dir, "readme.md", "src/README.md"},
		{"full path from repo root", dir, "src/readme.md", "src/README.md"},
		{"basename from subdir", filepath.Join(dir, "src"), "readme.md", "README.md"},
		{"upward path from subdir", filepath.Join(dir, "src"), "../src/readme.md", "README.md"},
		{"exact case still resolves", dir, "docs/Guide.md", "docs/Guide.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewTargetResolver()
			ref, err := r.Resolve(tt.workdir, tt.raw)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			assertRef(t, ref, tt.raw, tt.canonical, true, true, ResolutionVCS)
		})
	}
}

func TestGitIndexBasenameFallbackDisambiguates(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initGitRepo(t, dir, "src/README.md")

	// A bare basename that matches nothing in the index falls through to the
	// filesystem, and then to raw when the file does not exist on disk.
	r := NewTargetResolver()
	ref, err := r.Resolve(dir, "Guide.md")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertRef(t, ref, "Guide.md", "Guide.md", false, false, ResolutionRaw)
}

func TestUntrackedFilesystemPreservation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Config.toml")

	r := NewTargetResolver()
	ref, err := r.Resolve(dir, "config.toml")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertRef(t, ref, "config.toml", "Config.toml", true, false, ResolutionFilesystem)
}

func TestFilesystemSubdirectoryPreservation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "sub/Config.toml")

	r := NewTargetResolver()
	ref, err := r.Resolve(dir, "sub/config.toml")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertRef(t, ref, "sub/config.toml", "sub/Config.toml", true, false, ResolutionFilesystem)
}

func TestFilesystemExactCasePreserved(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md")

	r := NewTargetResolver()
	ref, err := r.Resolve(dir, "README.md")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertRef(t, ref, "README.md", "README.md", true, false, ResolutionFilesystem)
}

func TestNewFileRawFallback(t *testing.T) {
	dir := t.TempDir()
	r := NewTargetResolver()
	ref, err := r.Resolve(dir, "NewModule.go")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertRef(t, ref, "NewModule.go", "NewModule.go", false, false, ResolutionRaw)
}

func TestRawFallbackWhenParentDirMissing(t *testing.T) {
	dir := t.TempDir()
	r := NewTargetResolver()
	ref, err := r.Resolve(dir, "missing/NewModule.go")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertRef(t, ref, "missing/NewModule.go", "missing/NewModule.go", false, false, ResolutionRaw)
}

func TestRawFallbackForUnmatchedNameInExistingDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go")

	r := NewTargetResolver()
	ref, err := r.Resolve(dir, "util.go")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertRef(t, ref, "util.go", "util.go", false, false, ResolutionRaw)
}

func TestGitTrackedBeatsFilesystem(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	// Tracked under lower case in the index, with the exact same on-disk
	// name. The index authority still wins for a differently-cased query.
	initGitRepo(t, dir, "foo.txt")

	r := NewTargetResolver()
	ref, err := r.Resolve(dir, "Foo.TXT")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertRef(t, ref, "Foo.TXT", "foo.txt", true, true, ResolutionVCS)
}

func TestGitMissingFallsThroughToFilesystem(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	// A repo that tracks nothing relevant, plus an untracked file on disk.
	initGitRepo(t, dir, "tracked.go")
	writeFile(t, dir, "Config.toml")

	r := NewTargetResolver()
	ref, err := r.Resolve(dir, "config.toml")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertRef(t, ref, "config.toml", "Config.toml", true, false, ResolutionFilesystem)
}

func TestNonGitWorkdirSkipsVCSSource(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Config.toml")

	r := NewTargetResolver()
	ref, err := r.Resolve(dir, "config.toml")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertRef(t, ref, "config.toml", "Config.toml", true, false, ResolutionFilesystem)
}

func TestResolveRejectsEmptyWorkdir(t *testing.T) {
	r := NewTargetResolver()
	if _, err := r.Resolve("", "readme.md"); err == nil {
		t.Fatal("expected error for empty workdir")
	}
}

func TestResolveRejectsEmptyTarget(t *testing.T) {
	r := NewTargetResolver()
	if _, err := r.Resolve(t.TempDir(), ""); err == nil {
		t.Fatal("expected error for empty target")
	}
}

func TestResolveRejectsAbsoluteTarget(t *testing.T) {
	r := NewTargetResolver()
	_, err := r.Resolve(t.TempDir(), "/etc/passwd")
	if !errors.Is(err, ErrAbsoluteTarget) {
		t.Fatalf("expected ErrAbsoluteTarget, got %v", err)
	}
}

func TestResolutionSourceString(t *testing.T) {
	tests := []struct {
		source ResolutionSource
		want   string
	}{
		{ResolutionVCS, "vcs"},
		{ResolutionFilesystem, "filesystem"},
		{ResolutionRaw, "raw"},
		{ResolutionSource(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.source.String(); got != tt.want {
			t.Fatalf("String() for %d: expected %q, got %q", tt.source, tt.want, got)
		}
	}
}

func TestResolverInterfaceAssertion(t *testing.T) {
	var _ Resolver = NewTargetResolver()
}

package extractor

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/ir"
)

func TestNormalizeArtifactCRLFToLF(t *testing.T) {
	art := ir.NewFile("main.go", []byte("package main\r\n\r\nfunc main() {}\r\n"))
	got := NormalizeArtifact(art)
	if strings.Contains(string(got.Content), "\r") {
		t.Errorf("content still contains CR: %q", got.Content)
	}
	if want := "package main\n\nfunc main() {}\n"; string(got.Content) != want {
		t.Errorf("content = %q, want %q", got.Content, want)
	}
	if got.Hash != ir.ComputeHash(got.Content) {
		t.Error("hash not recomputed from normalized content")
	}
}

func TestNormalizeArtifactStripsBOM(t *testing.T) {
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte("package main\n")...)
	art := ir.NewFile("main.go", raw)
	got := NormalizeArtifact(art)
	if len(got.Content) == 0 || got.Content[0] == 0xEF {
		t.Errorf("BOM not stripped: %x", got.Content)
	}
	if string(got.Content) != "package main\n" {
		t.Errorf("content = %q, want BOM stripped", got.Content)
	}
}

func TestNormalizeArtifactEnforcesTrailingNewline(t *testing.T) {
	art := ir.NewFile("a.txt", []byte("no trailing newline"))
	got := NormalizeArtifact(art)
	if string(got.Content) != "no trailing newline\n" {
		t.Errorf("content = %q, want enforced trailing newline", got.Content)
	}

	withNL := ir.NewFile("a.txt", []byte("already has newline\n"))
	if got := NormalizeArtifact(withNL); string(got.Content) != "already has newline\n" {
		t.Errorf("existing trailing newline must be preserved, got %q", got.Content)
	}
}

func TestNormalizeArtifactEmptyContentNoSpuriousNewline(t *testing.T) {
	art := ir.NewFile("empty.go", nil)
	got := NormalizeArtifact(art)
	if len(got.Content) != 0 {
		t.Errorf("empty content must stay empty, got %q", got.Content)
	}
}

func TestNormalizeArtifactPathCleaning(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"src//main.go", "src/main.go"},
		{`src\main.go`, "src/main.go"},
		{"./main.go", "main.go"},
		{"/main.go", "main.go"},
		{"src/./nested/file.go", "src/nested/file.go"},
		{"", ""},
		{"..", ""},
		{"../escape.go", ""},
		{".", ""},
	}
	for _, c := range cases {
		art := ir.NewFile(c.in, []byte("x\n"))
		got := NormalizeArtifact(art)
		if got.Path != c.want {
			t.Errorf("NormalizeArtifact(%q).Path = %q, want %q", c.in, got.Path, c.want)
		}
	}
}

func TestNormalizeArtifactDoesNotMutateInput(t *testing.T) {
	src := []byte("hello\r\nworld")
	art := ir.NewFile("a.txt", src)
	_ = NormalizeArtifact(art)
	if string(src) != "hello\r\nworld" {
		t.Error("NormalizeArtifact must not mutate the caller's content")
	}
}

func TestNormalizeArtifactNonFileKindUntouched(t *testing.T) {
	art := ir.NewArtifact("", "link", ir.ArtifactSymlink, []byte("target\r\n"))
	got := NormalizeArtifact(art)
	if string(got.Content) != "target\r\n" {
		t.Errorf("symlink content must not be newline-normalized, got %q", got.Content)
	}
}

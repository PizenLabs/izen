package ir

import (
	"strings"
	"testing"
)

func TestNewFileHashAndDefaults(t *testing.T) {
	a := NewFile("main.go", []byte("package main"))
	if a.ID != "main.go" {
		t.Errorf("ID = %q, want default to path", a.ID)
	}
	if a.Path != "main.go" {
		t.Errorf("Path = %q", a.Path)
	}
	if a.Kind != ArtifactFile {
		t.Errorf("Kind = %s, want file", a.Kind)
	}
	want := ComputeHash([]byte("package main"))
	if a.Hash != want {
		t.Errorf("Hash = %q, want %q", a.Hash, want)
	}
	if len(a.Hash) != 64 {
		t.Errorf("hash must be hex SHA-256 (64 chars), got %d", len(a.Hash))
	}
}

func TestNewArtifactKindHandling(t *testing.T) {
	a := NewArtifact("", "link", ArtifactSymlink, []byte("target"))
	if a.Kind != ArtifactSymlink {
		t.Errorf("Kind = %s, want symlink", a.Kind)
	}
	b := NewArtifact("id", "p", ArtifactKind("bogus"), []byte("x"))
	if b.Kind != ArtifactFile {
		t.Errorf("invalid kind must default to file, got %s", b.Kind)
	}
	if b.ID != "id" {
		t.Errorf("explicit ID must be preserved, got %q", b.ID)
	}
}

func TestArtifactContentIsolation(t *testing.T) {
	src := []byte("original")
	a := NewFile("f.txt", src)
	src[0] = 'X'
	if string(a.Content) != "original" {
		t.Error("artifact must not alias the caller's content slice")
	}
	a.Content[0] = 'Y'
	if string(src) != "Xriginal" {
		t.Error("caller must not observe artifact mutation")
	}
}

func TestArtifactKindValidity(t *testing.T) {
	for k, want := range map[ArtifactKind]bool{
		ArtifactFile:        true,
		ArtifactSymlink:     true,
		ArtifactMeta:        true,
		ArtifactKind(""):    false,
		ArtifactKind("dir"): false,
	} {
		if got := k.Valid(); got != want {
			t.Errorf("Valid(%q) = %v, want %v", k, got, want)
		}
	}
	if ArtifactFile.String() != "file" {
		t.Errorf("String() = %q", ArtifactFile.String())
	}
}

func TestArtifactString(t *testing.T) {
	a := NewFile("a.txt", []byte("hello"))
	s := a.String()
	if !strings.Contains(s, "a.txt") || !strings.Contains(s, "5 bytes") {
		t.Errorf("String() = %q, want path and size", s)
	}
}

package project

import (
	"testing"
)

func TestDetectGoProject(t *testing.T) {
	det := Detect("testdata/goapp")
	if det.Primary == nil {
		t.Fatal("expected to detect Go project")
	}
	if string(det.Primary.ID) != "go" {
		t.Fatalf("expected Go, got %s", det.Primary.Name)
	}
	if det.Confidence <= 0 {
		t.Fatal("expected positive confidence")
	}
}

func TestDetectJavaScriptProject(t *testing.T) {
	det := Detect("testdata/tsapp")
	if det.Primary == nil {
		t.Fatal("expected to detect project")
	}
}

func TestDetectRustProject(t *testing.T) {
	det := Detect("testdata/rustapp")
	if det.Primary == nil {
		t.Fatal("expected to detect Rust project")
	}
	if string(det.Primary.ID) != "rust" {
		t.Fatalf("expected Rust, got %s", det.Primary.Name)
	}
}

func TestDetectEmptyDir(t *testing.T) {
	det := Detect("testdata/empty")
	if det.Primary != nil {
		t.Fatal("expected nil primary for empty dir")
	}
}

func TestDetectNonExistentDir(t *testing.T) {
	det := Detect("testdata/nonexistent")
	if det.Primary != nil {
		t.Fatal("expected nil primary for nonexistent dir")
	}
}

func TestFallbackProjectContext(t *testing.T) {
	ctx := FallbackProjectContext()
	if ctx == nil {
		t.Fatal("FallbackProjectContext must not return nil")
	}
	if ctx.Name != "generic" {
		t.Fatalf("expected Name=generic, got %s", ctx.Name)
	}
	if ctx.Type != "unknown" {
		t.Fatalf("expected Type=unknown, got %s", ctx.Type)
	}
}

func TestDetectEmptyDirReturnsFallbackProjectContext(t *testing.T) {
	det := Detect("testdata/empty")
	if det.Primary != nil {
		t.Fatal("expected nil primary for empty dir")
	}
	// When Primary is nil, the UI should use FallbackProjectContext
	ctx := FallbackProjectContext()
	if ctx.Name != "generic" || ctx.Type != "unknown" {
		t.Fatalf("expected fallback context {Name=generic, Type=unknown}, got %+v", ctx)
	}
}

func TestFallbackRepoConfig(t *testing.T) {
	repo := FallbackRepoConfig("/tmp")
	if repo == nil {
		t.Fatal("FallbackRepoConfig must not return nil")
	}
	if repo.Root != "/tmp" {
		t.Fatalf("expected Root=/tmp, got %s", repo.Root)
	}
	if repo.IsGitRepo {
		t.Fatal("expected IsGitRepo=false for fallback")
	}
	if repo.DefaultBranch != "main" {
		t.Fatalf("expected DefaultBranch=main, got %s", repo.DefaultBranch)
	}
}

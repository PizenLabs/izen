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

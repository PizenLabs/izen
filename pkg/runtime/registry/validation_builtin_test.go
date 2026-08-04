package registry

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGofmtValidatorCleanAndDirty(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not installed")
	}
	root := t.TempDir()
	clean := filepath.Join(root, "clean.go")
	if err := os.WriteFile(clean, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty := filepath.Join(root, "dirty.go")
	// 4 spaces instead of a tab; gofmt flags it.
	if err := os.WriteFile(dirty, []byte("package p\nfunc x() {\n    return\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	v := GofmtValidator{Root: root}
	cleanReport, err := v.Validate(context.Background(), clean)
	if err != nil {
		t.Fatal(err)
	}
	if !cleanReport.OK {
		t.Errorf("clean file should pass, got %q", cleanReport.Output)
	}

	dirtyReport, err := v.Validate(context.Background(), dirty)
	if err != nil {
		t.Fatal(err)
	}
	if dirtyReport.OK {
		t.Error("unformatted file should fail gofmt")
	}

	// Non-Go paths are skipped, not failed.
	readme := filepath.Join(root, "README.md")
	if err := os.WriteFile(readme, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	mdReport, err := v.Validate(context.Background(), readme)
	if err != nil {
		t.Fatal(err)
	}
	if !mdReport.OK {
		t.Error("non-Go path should be skipped with OK")
	}
}

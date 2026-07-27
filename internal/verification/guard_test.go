package verification

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsGoProject_WithGoMod(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	if !IsGoProject(dir) {
		t.Error("IsGoProject should return true when go.mod exists")
	}
}

func TestIsGoProject_WithGoFile(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	if !IsGoProject(dir) {
		t.Error("IsGoProject should return true when .go files exist")
	}
}

func TestIsGoProject_WithGoSum(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "go.sum"), []byte{}, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if !IsGoProject(dir) {
		t.Error("IsGoProject should return true when go.sum exists")
	}
}

func TestIsGoProject_WithGoWork(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "go.work"), []byte{}, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if !IsGoProject(dir) {
		t.Error("IsGoProject should return true when go.work exists")
	}
}

func TestIsGoProject_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	if IsGoProject(dir) {
		t.Error("IsGoProject should return false for an empty directory")
	}
}

func TestIsGoProject_StaticHTML(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"index.html", "styles.css", "script.js"} {
		err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0644)
		if err != nil {
			t.Fatal(err)
		}
	}
	if IsGoProject(dir) {
		t.Error("IsGoProject should return false for a static HTML/CSS/JS project")
	}
}

func TestIsEnvironmentSetupError_MissingGoMod(t *testing.T) {
	cases := []string{
		"pattern ./... does not contain main module",
		"go: cannot find main module",
		"go.mod file not found",
		"no go files in /path/to/dir",
		"build constraints exclude all go files",
		"go: go.mod file does not exist",
	}
	for _, output := range cases {
		if !IsEnvironmentSetupError(output) {
			t.Errorf("IsEnvironmentSetupError should return true for: %q", output)
		}
	}
}

func TestIsEnvironmentSetupError_NotSetupError(t *testing.T) {
	cases := []string{
		"undefined: something",
		"cannot find package",
		"test failed",
	}
	for _, output := range cases {
		if IsEnvironmentSetupError(output) {
			t.Errorf("IsEnvironmentSetupError should return false for: %q", output)
		}
	}
}

func TestIsEnvironmentSetupError_Empty(t *testing.T) {
	if IsEnvironmentSetupError("") {
		t.Error("IsEnvironmentSetupError should return false for empty output")
	}
}

func TestIsSetupFailure_NonGoProject(t *testing.T) {
	if !IsSetupFailure("pattern ./... does not contain main module", false) {
		t.Error("IsSetupFailure should return true for non-Go project with setup error output")
	}
}

func TestIsSetupFailure_GoProjectNoError(t *testing.T) {
	if IsSetupFailure("", true) {
		t.Error("IsSetupFailure should return false for Go project with empty output")
	}
}

func TestIsSetupFailure_GoProjectWithSetupError(t *testing.T) {
	if !IsSetupFailure("go: cannot find main module", true) {
		t.Error("IsSetupFailure should return true for Go project with setup error output")
	}
}

func TestFormatSkipMessage(t *testing.T) {
	msg := FormatSkipMessage("HTML/JS/CSS")
	expected := "[SKIP] No test runner configured for HTML/JS/CSS static assets"
	if msg != expected {
		t.Errorf("FormatSkipMessage(HTML/JS/CSS) = %q, want %q", msg, expected)
	}
}

func TestFormatSkipMessage_Empty(t *testing.T) {
	msg := FormatSkipMessage("")
	expected := "[SKIP] No test runner configured for  static assets"
	if msg != expected {
		t.Errorf("FormatSkipMessage(\"\") = %q, want %q", msg, expected)
	}
}

package verification

import (
	"os"
	"path/filepath"
	"strings"
)

// IsGoProject returns true if the workspace at root contains Go project
// indicators: go.mod, go.sum, go.work, or any *.go file in the root.
// This prevents running Go-specific verification commands on static
// asset projects (HTML/CSS/JS) that have no Go toolchain.
func IsGoProject(root string) bool {
	indicators := []string{"go.mod", "go.sum", "go.work"}
	for _, ind := range indicators {
		if _, err := os.Stat(filepath.Join(root, ind)); err == nil {
			return true
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".go") {
			return true
		}
	}
	return false
}

// IsEnvironmentSetupError returns true when a verification command failed
// because the Go toolchain environment is not configured for this workspace
// (e.g. missing go.mod, no Go files, not a Go module). These errors are
// environment/setup failures — not code defects — and must NOT trigger
// auto-recovery or be routed to the LLM.
func IsEnvironmentSetupError(output string) bool {
	if output == "" {
		return false
	}
	lower := strings.ToLower(output)
	indicators := []string{
		"pattern ./... does not contain main module",
		"go: cannot find main module",
		"go.mod file not found",
		"no go files in",
		"build constraints exclude all go files",
		"go: go.mod file does not exist",
		"go: missing go.sum entry for module",
		"go: module provides package",
		"go test: cannot run *_test.go files",
	}
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
}

// IsSetupFailure returns true when the verification command failed due to
// an environment/setup issue rather than a code defect. This includes
// missing go.mod, missing Go files, or non-Go project type being subjected
// to Go-specific verification commands.
func IsSetupFailure(output string, isGoProject bool) bool {
	if output == "" {
		return false
	}
	if !isGoProject {
		return true
	}
	return IsEnvironmentSetupError(output)
}

// FormatSkipMessage returns a compact skip message for verification
// commands that are not applicable to the current project type.
func FormatSkipMessage(platform string) string {
	return "[SKIP] No test runner configured for " + platform + " static assets"
}

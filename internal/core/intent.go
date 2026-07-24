package core

import (
	"path/filepath"
	"strings"
)

// ExecutionMode categorises the user's intent into two distinct execution paths.
// Diagnostic requires running go test, stack trace analysis, and deep forensics.
// DirectMutation forbids test runners and compilers — only file edits, renames,
// documentation updates, config tweaks, and simple text replacements.
type ExecutionMode int

const (
	// ExecutionModeDiagnostic is for code bugs, runtime errors, compile failures.
	// Requires running go test, stack trace analysis, and deep forensics.
	ExecutionModeDiagnostic ExecutionMode = iota
	// ExecutionModeDirectMutation is for file renames, documentation updates,
	// config tweaks, and simple text replacements. Strictly forbids invoking
	// test runners or compilers.
	ExecutionModeDirectMutation
)

func (m ExecutionMode) String() string {
	switch m {
	case ExecutionModeDiagnostic:
		return "diagnostic"
	case ExecutionModeDirectMutation:
		return "direct-mutation"
	default:
		return "unknown"
	}
}

// IsDiagnostic returns true when the mode requires full forensic treatment.
func (m ExecutionMode) IsDiagnostic() bool { return m == ExecutionModeDiagnostic }

// IsDirectMutation returns true when the mode forbids test runners/compilers.
func (m ExecutionMode) IsDirectMutation() bool { return m == ExecutionModeDirectMutation }

// directMutationFileExtensions are file types whose edits never require
// running a test suite or compiler — they are purely structural or textual.
var directMutationFileExtensions = []string{
	".md", ".txt", ".json", ".yaml", ".yml", ".toml",
	".cfg", ".ini", ".conf", ".env", ".editorconfig",
	".gitignore", ".gitattributes",
	".dockerignore", ".dockerfile", "Dockerfile",
	".svg", ".png", ".jpg", ".jpeg", ".gif", ".ico",
	".sh", ".bat", ".ps1",
	".xml", ".html", ".css", ".scss", ".less",
	".proto", ".graphql", ".sql",
}

// directMutationPrefixes are file path prefixes that indicate config/doc files.
var directMutationPrefixes = []string{
	".github/", ".gitlab/",
	"docs/", "documentation/",
	".vscode/", ".idea/",
}

// directMutationKeywords are user-intent keywords that signal a direct mutation.
var directMutationKeywords = []string{
	"rename", "move file", "delete file",
	"update readme", "update doc", "fix typo", "fix spelling",
	"edit config", "update config", "change config",
	"bump version", "update version",
	"add comment", "update comment",
	"change description", "update description",
	"format file", "pretty print",
}

// ClassifyExecutionMode analyses the user input and optional file context to
// determine whether the intent is diagnostic (needs test/compile) or a direct
// mutation (file-only, no test runner).
func ClassifyExecutionMode(userInput string, targetFiles []string) ExecutionMode {
	input := strings.ToLower(strings.TrimSpace(userInput))

	// Check target file extensions first — if ALL targets are doc/config,
	// this is a direct mutation regardless of keywords.
	if len(targetFiles) > 0 {
		allDirect := true
		for _, f := range targetFiles {
			if !IsDirectMutationFile(f) {
				allDirect = false
				break
			}
		}
		if allDirect {
			return ExecutionModeDirectMutation
		}
	}

	// Check keywords for direct mutation intent.
	for _, kw := range directMutationKeywords {
		if strings.Contains(input, kw) {
			return ExecutionModeDirectMutation
		}
	}

	// Diagnostic keywords — these trigger the full forensic pipeline.
	diagnosticKeywords := []string{
		"bug", "crash", "panic", "error", "fail", "fix",
		"compile", "build", "test", "undefined", "broken",
		"not working", "doesn't work", "regression",
		"stack trace", "nil pointer", "race",
	}
	for _, kw := range diagnosticKeywords {
		if strings.Contains(input, kw) {
			return ExecutionModeDiagnostic
		}
	}

	// Default to diagnostic when uncertainty exists — safer to run tests
	// unnecessarily than to skip a necessary diagnostic.
	return ExecutionModeDiagnostic
}

// IsDirectMutationFile reports whether the given file path is a documentation,
// config, or non-code asset that never requires test/compile verification.
func IsDirectMutationFile(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	for _, de := range directMutationFileExtensions {
		if ext == de || strings.HasSuffix(strings.ToLower(filePath), strings.ToLower(de)) {
			return true
		}
	}
	base := strings.ToLower(filepath.Base(filePath))
	if base == "dockerfile" || base == "makefile" || strings.HasPrefix(base, "dockerfile") {
		return true
	}
	lower := strings.ToLower(filePath)
	for _, p := range directMutationPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

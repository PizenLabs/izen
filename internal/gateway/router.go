package gateway

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/command"
)

// commandPrefixPattern matches known router/CLI prefixes like $prompt, /plan, etc.
var commandPrefixPattern = regexp.MustCompile(`^(?:\$prompt\s+|\$ask\s+|/plan\s+|/build\s+)?`)

// fileRefPattern matches @filename references (e.g. @LICENSE, @README.md).
var fileRefPattern = regexp.MustCompile(`@(\S+)`)

// directMutationVerbs are verbs/phrases that signal an intent to edit a file
// rather than diagnose or analyse. Order matters: longer phrases first so
// "fix typo" matches before the bare "fix".
var directMutationVerbs = []string{
	"fix typo", "fix spelling", "fix grammar",
	"move file", "delete file",
	"bump version", "update version",
	"add comment", "update comment",
	"change description", "update description",
	"format file", "pretty print",
	"edit config", "update config", "change config",
	"update doc", "update readme",
	"rename",
	"update",
	"change",
	"modify",
	"replace",
	"set",
	"add",
	"remove",
	"delete",
	"bump",
	"format",
	"correct",
	"capitalize",
	"lowercase",
	"uppercase",
	"fix",
}

// diagnosticPatterns are regex patterns that signal a bug-hunting or diagnostic
// intent — these should NOT be fast-tracked even when a doc file is referenced.
// Patterns with word boundaries (\b) avoid false positives from substrings
// (e.g. "bug" inside "debug").
var diagnosticPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bwhy\s+is\b`),
	regexp.MustCompile(`\bwhy\s+does\b`),
	regexp.MustCompile(`\bwhy\s+isn't\b`),
	regexp.MustCompile(`\bwhat\s+caused\b`),
	regexp.MustCompile(`\bwhat\s+cause\b`),
	regexp.MustCompile(`\binvestigate\b`),
	regexp.MustCompile(`\bis\s+broken\b`),
	regexp.MustCompile(`\bis\s+crashing\b`),
	regexp.MustCompile(`\bis\s+failing\b`),
	regexp.MustCompile(`\bstack\s+trace\b`),
	regexp.MustCompile(`\bbacktrace\b`),
	regexp.MustCompile(`\broot\s+cause\b`),
	regexp.MustCompile(`\bcrash\b`),
	regexp.MustCompile(`\bpanic\b`),
	regexp.MustCompile(`\bbug\b`),
}

// directMutationFileExts are file extensions that are always safe for
// direct mutation (no test/compile required).
var directMutationFileExts = []string{
	".md", ".txt", ".json", ".yaml", ".yml", ".toml",
	".cfg", ".ini", ".conf", ".env", ".editorconfig",
	".gitignore", ".gitattributes",
	".dockerignore",
	".svg", ".png", ".jpg", ".jpeg", ".gif", ".ico",
	".sh", ".bat", ".ps1",
	".xml", ".html", ".css", ".scss", ".less",
	".proto", ".graphql", ".sql",
}

// directMutationBareFiles are filenames (without path) that are always safe
// for direct mutation. These are matched when no extension check applies
// (e.g. "LICENSE", "Dockerfile", "Makefile").
var directMutationBareFiles = []string{
	"license", "licence",
	"readme",
	"dockerfile", "makefile",
	"contributing", "contributing.md",
	"changelog", "changelog.md",
}

// ClassifyDirectMutation inspects user input to determine whether it is a
// simple single-file text mutation that should bypass the Senior Architect
// pipeline and route directly to the /build engine as a FILE_MUTATE task.
//
// Returns the fast-track FallbackPlanTarget and true when the input qualifies.
// Returns an empty target and false when normal processing should continue.
func ClassifyDirectMutation(input string) (command.FallbackPlanTarget, bool) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return command.FallbackPlanTarget{}, false
	}

	// Strip known command prefixes to get the actual message.
	msg := commandPrefixPattern.ReplaceAllString(raw, "")
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return command.FallbackPlanTarget{}, false
	}

	// If the input carries diagnostic intent, never fast-track.
	if hasDiagnosticIntent(msg) {
		return command.FallbackPlanTarget{}, false
	}

	// Check for a direct mutation verb.
	if !hasDirectMutationVerb(msg) {
		return command.FallbackPlanTarget{}, false
	}

	// Extract the target filename.
	files := extractFileRefs(msg)
	if len(files) == 0 {
		files = extractBareFilenames(msg)
	}
	if len(files) == 0 {
		return command.FallbackPlanTarget{}, false
	}

	// Verify every referenced file is a direct-mutation target.
	for _, f := range files {
		if !isDirectMutationTarget(f) {
			return command.FallbackPlanTarget{}, false
		}
	}

	target := command.FallbackPlanTarget{
		File:        files[0],
		Description: raw,
		TaskType:    "FILE_MUTATE",
	}
	return target, true
}

// hasDiagnosticIntent reports whether the message contains diagnostic patterns.
func hasDiagnosticIntent(msg string) bool {
	lower := strings.ToLower(msg)
	for _, p := range diagnosticPatterns {
		if p.MatchString(lower) {
			return true
		}
	}
	return false
}

// hasDirectMutationVerb reports whether the message contains a known
// direct-mutation verb.
func hasDirectMutationVerb(msg string) bool {
	lower := strings.ToLower(msg)
	for _, v := range directMutationVerbs {
		if strings.Contains(lower, v) {
			return true
		}
	}
	return false
}

// extractFileRefs extracts filenames from @ref patterns (e.g. @LICENSE, @README.md).
func extractFileRefs(msg string) []string {
	matches := fileRefPattern.FindAllStringSubmatch(msg, -1)
	if len(matches) == 0 {
		return nil
	}
	files := make([]string, 0, len(matches))
	seen := make(map[string]bool)
	for _, m := range matches {
		name := strings.TrimSpace(m[1])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		files = append(files, name)
	}
	return files
}

// extractBareFilenames attempts to detect bare filenames (without @ prefix)
// mentioned in the message, such as "LICENSE", "README.md", ".env".
// This is a fallback when no @refs are found.
func extractBareFilenames(msg string) []string {
	lower := strings.ToLower(msg)
	var files []string
	seen := make(map[string]bool)

	fields := strings.Fields(lower)
	for _, f := range fields {
		clean := strings.Trim(f, `.,;:'"!?()`)
		if clean == "" || seen[clean] {
			continue
		}
		// Check extension-based detection.
		ext := filepath.Ext(clean)
		for _, de := range directMutationFileExts {
			if ext == de {
				seen[clean] = true
				files = append(files, clean)
				break
			}
		}
		// Check bare filename detection (e.g. "license", "makefile").
		if !seen[clean] {
			for _, bf := range directMutationBareFiles {
				if clean == bf {
					seen[clean] = true
					files = append(files, clean)
					break
				}
			}
		}
	}

	return files
}

// isDirectMutationTarget reports whether the given filename is a
// documentation, config, or non-code asset that never requires
// test/compile verification.
func isDirectMutationTarget(name string) bool {
	lower := strings.ToLower(name)

	// Check extension-based matches.
	ext := filepath.Ext(lower)
	for _, de := range directMutationFileExts {
		if ext == de {
			return true
		}
	}

	// Check bare filename matches (no extension).
	base := filepath.Base(lower)
	for _, bf := range directMutationBareFiles {
		if base == bf {
			return true
		}
	}

	return false
}

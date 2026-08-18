package gateway

import (
	"path/filepath"
	"regexp"
	"strings"
)

// commandPrefixPattern matches known router/CLI prefixes like $prompt, /plan, etc.
var commandPrefixPattern = regexp.MustCompile(`^(?:\$prompt\s+|\$ask\s+|/plan\s+|/build\s+)?`)

// fileRefPattern matches @filename references (e.g. @LICENSE, @README.md).
var fileRefPattern = regexp.MustCompile(`@(\S+)`)

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

// knownConventions maps lowercased filenames to their canonical (correct-case)
// form. All lookups should be done with the lowercased key; the value
// preserves the conventional casing (e.g. "LICENSE", "README.md").
var knownConventions = map[string]string{
	"license":         "LICENSE",
	"licence":         "LICENSE",
	"readme":          "README.md",
	"readme.md":       "README.md",
	"dockerfile":      "Dockerfile",
	"makefile":        "Makefile",
	"env":             ".env",
	".env":            ".env",
	"env.example":     ".env.example",
	".env.example":    ".env.example",
	"gitignore":       ".gitignore",
	".gitignore":      ".gitignore",
	"dockerignore":    ".dockerignore",
	".dockerignore":   ".dockerignore",
	"contributing":    "CONTRIBUTING.md",
	"contributing.md": "CONTRIBUTING.md",
	"changelog":       "CHANGELOG.md",
	"changelog.md":    "CHANGELOG.md",
}

// CanonicalizeFileName maps a file path returned by the LLM to its
// conventional-cased form. It checks each path segment against known
// conventions (e.g. "license" → "LICENSE", "readme" → "README.md").
// Non-convention paths are returned unchanged.
func CanonicalizeFileName(path string) string {
	if path == "" {
		return path
	}
	// Try the full path first (handles ".env", ".gitignore", etc.)
	lower := strings.ToLower(path)
	if canon, ok := knownConventions[lower]; ok {
		return canon
	}
	// Check bare filename (last segment)
	base := filepath.Base(path)
	baseLower := strings.ToLower(base)
	if canon, ok := knownConventions[baseLower]; ok {
		dir := filepath.Dir(path)
		if dir == "." || dir == "" {
			return canon
		}
		return filepath.Join(dir, canon)
	}
	return path
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

// ExtractDirectMutationTargets extracts all filenames mentioned in a
// direct mutation prompt. It handles comma-separated file lists
// (e.g. "index.html, styles.css, script.js") as well as bare filenames.
// Returns an empty slice when no target files can be found.
func ExtractDirectMutationTargets(input string) []string {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return nil
	}

	msg := commandPrefixPattern.ReplaceAllString(raw, "")
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return nil
	}

	// First try @ref-style file references.
	fileRefs := extractFileRefs(msg)
	for i, f := range fileRefs {
		fileRefs[i] = strings.TrimRight(f, `,;`)
	}
	if len(fileRefs) > 0 {
		return fileRefs
	}

	// Include common web/JS extensions for bare filename detection
	// in comma-separated lists, since .js/.ts are standard front-end targets.
	allExts := append([]string{".js", ".ts", ".jsx", ".tsx"}, directMutationFileExts...)

	seen := make(map[string]bool)
	var targets []string

	parts := strings.Split(msg, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Check each word in the segment for filenames.
		fields := strings.Fields(part)
		for _, f := range fields {
			clean := strings.Trim(f, `.,;:'"!?()`)
			if clean == "" || seen[clean] {
				continue
			}
			ext := filepath.Ext(clean)
			isExt := false
			for _, de := range allExts {
				if ext == de {
					isExt = true
					break
				}
			}
			if isExt {
				seen[clean] = true
				targets = append(targets, clean)
				break
			}
			base := filepath.Base(clean)
			baseLower := strings.ToLower(base)
			for _, bf := range directMutationBareFiles {
				if baseLower == bf {
					canon := knownConventions[baseLower]
					if canon == "" {
						canon = base
					}
					if !seen[canon] {
						seen[canon] = true
						targets = append(targets, canon)
					}
					break
				}
			}
		}
	}

	return targets
}

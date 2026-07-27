package workspace

import (
	"os"
	"path/filepath"
	"strings"
)

// TargetFileResolver performs deterministic file matching against the
// workspace file tree. It scans user prompt keywords against repo paths
// and explicitly resolves target file paths, never returning generic
// strings like "workspace".
type TargetFileResolver struct {
	root string
}

// NewTargetFileResolver creates a resolver rooted at workspaceRoot.
func NewTargetFileResolver(workspaceRoot string) *TargetFileResolver {
	return &TargetFileResolver{root: workspaceRoot}
}

// Resolve scans the user prompt for file path tokens and keywords,
// then matches them against the workspace file tree. Returns explicit
// repo-relative paths only. Returns empty string when no match is found.
func (r *TargetFileResolver) Resolve(prompt string) string {
	if r.root == "" {
		return ""
	}

	// Pass 1: extract explicit file path tokens from the prompt.
	// These are tokens containing "/" or "." with a file extension.
	candidates := extractPathTokens(prompt)
	for _, c := range candidates {
		if isValidTarget(c) {
			return c
		}
	}

	// Pass 2: scan the workspace file tree for files whose path or name
	// matches prompt keywords.
	matched := keywordMatch(r.root, prompt)
	if matched != "" {
		return matched
	}

	return ""
}

// extractPathTokens pulls potential file path tokens from a prompt string.
func extractPathTokens(prompt string) []string {
	var tokens []string
	words := strings.Fields(prompt)
	for _, w := range words {
		w = strings.Trim(w, `'"(),;`)
		if strings.Contains(w, "/") || strings.Contains(w, ".") {
			tokens = append(tokens, w)
		}
	}
	return tokens
}

// isValidTarget reports whether a candidate path is a valid explicit
// file target. Rejects bare "workspace", empty strings, and paths
// that are clearly not file references.
func isValidTarget(path string) bool {
	if path == "" || strings.EqualFold(path, "workspace") {
		return false
	}
	// Must contain a path separator or a file extension.
	if !strings.Contains(path, "/") && !strings.Contains(path, ".") {
		return false
	}
	return true
}

// keywordMatch scans the workspace file tree for files whose name or
// path contains any of the prompt's significant keywords.
func keywordMatch(root, prompt string) string {
	lower := strings.ToLower(prompt)
	keywords := extractKeywords(lower)
	if len(keywords) == 0 {
		return ""
	}

	var match string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return nil
		}
		// Skip hidden/dot directories and the .izen metadata dir.
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return filepath.SkipDir
		}
		if strings.HasPrefix(rel, ".izen/") || strings.HasPrefix(rel, ".") {
			return nil
		}

		relLower := strings.ToLower(rel)
		baseLower := strings.ToLower(info.Name())
		for _, kw := range keywords {
			if strings.Contains(relLower, kw) || strings.Contains(baseLower, kw) {
				if match == "" || len(rel) < len(match) {
					match = rel
				}
				break
			}
		}
		return nil
	})

	return match
}

// extractKeywords pulls significant keywords from a lowercased prompt,
// filtering out common filler words and Go keywords that are too generic
// to be useful for file matching.
func extractKeywords(lower string) []string {
	filler := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "can": true, "shall": true,
		"to": true, "of": true, "in": true, "for": true, "on": true,
		"with": true, "at": true, "by": true, "from": true, "as": true,
		"into": true, "through": true, "during": true, "before": true,
		"after": true, "above": true, "below": true, "between": true,
		"out": true, "off": true, "over": true, "under": true,
		"and": true, "or": true, "not": true, "no": true, "but": true,
		"if": true, "then": true, "else": true, "when": true,
		"move": true, "change": true, "update": true, "fix": true,
		"refactor": true, "convert": true, "replace": true, "delete": true,
		"add": true, "remove": true, "rename": true, "modify": true,
		"how": true, "what": true, "why": true, "where": true,
	}

	words := strings.Fields(lower)
	var keywords []string
	for _, w := range words {
		w = strings.Trim(w, `'".,;:()[]{}!?`)
		if w == "" || filler[w] {
			continue
		}
		// Skip tokens that are just numbers or too short.
		if len(w) < 2 {
			continue
		}
		// Skip Go keywords.
		if isGoKeyword(w) {
			continue
		}
		keywords = append(keywords, w)
	}
	return keywords
}

// isGoKeyword reports whether s is a Go reserved keyword or common
// predeclared identifier that would produce false matches in file scanning.
func isGoKeyword(s string) bool {
	goKeywords := map[string]bool{
		"break": true, "case": true, "continue": true, "default": true,
		"defer": true, "else": true, "fallthrough": true, "for": true,
		"func": true, "go": true, "goto": true, "if": true, "import": true,
		"interface": true, "map": true, "package": true, "range": true,
		"return": true, "select": true, "struct": true, "switch": true,
		"type": true, "var": true, "bool": true, "byte": true, "complex64": true,
		"complex128": true, "error": true, "float32": true, "float64": true,
		"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
		"rune": true, "string": true, "uint": true, "uint8": true, "uint16": true,
		"uint32": true, "uint64": true, "uintptr": true, "true": true, "false": true,
		"iota": true, "nil": true, "append": true, "cap": true, "close": true,
		"copy": true, "delete": true, "len": true, "make": true, "new": true,
		"panic": true, "print": true, "println": true, "recover": true,
	}
	return goKeywords[s]
}

// ValidateTarget guards against invalid target file paths.
// Returns the target if valid, or empty string if the target is
// "workspace", empty, or otherwise invalid.
func ValidateTarget(target string) string {
	if target == "" || strings.EqualFold(strings.TrimSpace(target), "workspace") {
		return ""
	}
	return target
}

// ResolveOrEmpty resolves a target path, returning "" if the target
// is "workspace" or empty. This is the hard guard for target file resolution.
func (r *TargetFileResolver) ResolveOrEmpty(target string) string {
	if target == "" || strings.EqualFold(strings.TrimSpace(target), "workspace") {
		return ""
	}
	return target
}

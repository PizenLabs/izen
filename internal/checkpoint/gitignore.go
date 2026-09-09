package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
)

// gitignorePattern is one parsed .gitignore rule.
type gitignorePattern struct {
	raw     string
	negated bool
	dirOnly bool
	pattern string
}

// loadGitignorePatterns reads workDir/.gitignore (best-effort) and returns
// the parsed rules in file order. A missing or unreadable .gitignore yields
// no patterns.
func loadGitignorePatterns(workDir string) []gitignorePattern {
	data, err := os.ReadFile(filepath.Join(workDir, ".gitignore"))
	if err != nil {
		return nil
	}
	var out []gitignorePattern
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Escaped leading \# or \!.
		if strings.HasPrefix(line, "\\#") || strings.HasPrefix(line, "\\!") {
			line = line[1:]
			trimmed = strings.TrimSpace(line)
		}
		p := gitignorePattern{raw: trimmed}
		if strings.HasPrefix(trimmed, "!") {
			p.negated = true
			trimmed = strings.TrimSpace(trimmed[1:])
			if trimmed == "" {
				continue
			}
		}
		if strings.HasSuffix(trimmed, "/") {
			p.dirOnly = true
			trimmed = strings.TrimSuffix(trimmed, "/")
		}
		// A leading slash anchors to the root; strip it since rel paths are
		// already root-relative.
		trimmed = strings.TrimPrefix(trimmed, "/")
		if trimmed == "" {
			continue
		}
		p.pattern = trimmed
		out = append(out, p)
	}
	return out
}

// matchesGitignore reports whether rel (slash-separated, workspace-relative)
// is ignored by the given patterns using git's last-match-wins semantics.
func matchesGitignore(rel string, patterns []gitignorePattern) bool {
	if len(patterns) == 0 {
		return false
	}
	rel = filepath.ToSlash(rel)
	ignored := false
	for _, p := range patterns {
		if gitignoreMatch(p, rel) {
			if p.negated {
				ignored = false
			} else {
				ignored = true
			}
		}
	}
	return ignored
}

// gitignoreMatch tests a single rule against rel.
func gitignoreMatch(p gitignorePattern, rel string) bool {
	pat := p.pattern
	if strings.Contains(pat, "/") {
		// Anchored to the root: match the full relative path. A dir-only
		// rule also matches everything beneath the directory.
		if p.dirOnly {
			return rel == pat || strings.HasPrefix(rel, pat+"/")
		}
		if strings.Contains(pat, "**") {
			return matchDoublestar(pat, rel)
		}
		ok, err := filepath.Match(pat, rel)
		if err == nil && ok {
			return true
		}
		// A file pattern also matches paths beneath a matched directory
		// only when the pattern itself names the directory; otherwise try
		// segment-wise matching for patterns like "build/output".
		return false
	}
	// No slash: match the basename at any depth.
	base := rel
	if idx := strings.LastIndex(rel, "/"); idx >= 0 {
		base = rel[idx+1:]
	}
	if p.dirOnly {
		// Directory name at any level: match that segment or anything under it.
		for _, seg := range strings.Split(rel, "/") {
			if ok, err := filepath.Match(pat, seg); err == nil && ok {
				return true
			}
		}
		return false
	}
	if strings.Contains(pat, "**") {
		return matchDoublestar(pat, base) || matchDoublestar(pat, rel)
	}
	ok, err := filepath.Match(pat, base)
	return err == nil && ok
}

// matchDoublestar matches patterns containing ** across path separators.
func matchDoublestar(pat, s string) bool {
	patSegs := strings.Split(pat, "/")
	sSegs := strings.Split(s, "/")
	return doublestarSegments(patSegs, sSegs)
}

func doublestarSegments(pat, s []string) bool {
	if len(pat) == 0 {
		return len(s) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(s); i++ {
			if doublestarSegments(pat[1:], s[i:]) {
				return true
			}
		}
		return false
	}
	if len(s) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], s[0])
	if err != nil || !ok {
		return false
	}
	return doublestarSegments(pat[1:], s[1:])
}

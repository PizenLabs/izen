package gateway

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/command"
)

// hotPrefixPattern matches the $hot fast-track prefix optionally followed by a modifier.
var hotPrefixPattern = regexp.MustCompile(`^\$hot(?:\s|$)`)

// commandPrefixPattern matches known router/CLI prefixes like $prompt, /plan, etc.
var commandPrefixPattern = regexp.MustCompile(`^(?:\$prompt\s+|\$ask\s+|/plan\s+|/build\s+)?`)

// fileRefPattern matches @filename references (e.g. @LICENSE, @README.md).
var fileRefPattern = regexp.MustCompile(`@(\S+)`)

// trivialMutationPhrases are operations that are always safe to fast-track
// because they are single-token, single-file, and never require AST/CSS
// inspection or /plan synthesis. Only these patterns may bypass /plan.
// Order matters: longer phrases first so "fix typo" matches before "fix".
var trivialMutationPhrases = []string{
	"fix typo", "fix spelling", "fix grammar",
	"add comment", "update comment",
	"change description", "update description",
	"bump version", "update version",
}

// trivialMutationBareVerbs are single-word verbs that, when combined with a
// doc-only file reference and NO frontend/UI context, qualify as trivial.
// These MUST be checked against IsFrontendUI first — if the input is UI-related,
// these verbs are NOT trivial.
var trivialMutationBareVerbs = []string{
	"rename",
	"format",
	"correct",
	"capitalize",
	"lowercase",
	"uppercase",
}

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
	"refactor", "rename", "change", "convert", "replace", "update", "modify",
	"reformat", "transform", "switch", "migrate", "change to",
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
	"create",
	"generate",
	"make",
	"write",
	"touch",
	"init",
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

// frontendUIWordPatterns match short UI keywords with word boundaries so
// "ui" does not match inside "build", "layout" inside "download_layout_data", etc.
var frontendUIWordPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bmove\b`),
	regexp.MustCompile(`\bposition\b`),
	regexp.MustCompile(`\btop\b`),
	regexp.MustCompile(`\bbottom\b`),
	regexp.MustCompile(`\bleft\b`),
	regexp.MustCompile(`\bright\b`),
	regexp.MustCompile(`\bcss\b`),
	regexp.MustCompile(`\bflexbox\b`),
	regexp.MustCompile(`\bflex\b`),
	regexp.MustCompile(`\bgrid\b`),
	regexp.MustCompile(`\blayout\b`),
	regexp.MustCompile(`\bresponsive\b`),
	regexp.MustCompile(`\bviewport\b`),
	regexp.MustCompile(`\bheader\b`),
	regexp.MustCompile(`\bnav\b`),
	regexp.MustCompile(`\bnavigation\b`),
	regexp.MustCompile(`\bnavbar\b`),
	regexp.MustCompile(`\bsidebar\b`),
	regexp.MustCompile(`\bfooter\b`),
	regexp.MustCompile(`\bhero\b`),
	regexp.MustCompile(`\bbanner\b`),
	regexp.MustCompile(`\bstyling\b`),
	regexp.MustCompile(`\bstyle\b`),
	regexp.MustCompile(`\bstylesheet\b`),
	regexp.MustCompile(`\bpadding\b`),
	regexp.MustCompile(`\bmargin\b`),
	regexp.MustCompile(`\bborder\b`),
	regexp.MustCompile(`\bspacing\b`),
	regexp.MustCompile(`\bz-index\b`),
	regexp.MustCompile(`\bzindex\b`),
	regexp.MustCompile(`\babsolute\b`),
	regexp.MustCompile(`\brelative\b`),
	regexp.MustCompile(`\bsticky\b`),
	regexp.MustCompile(`\bfixed\b`),
	regexp.MustCompile(`\bfloat\b`),
	regexp.MustCompile(`\bclearfix\b`),
	regexp.MustCompile(`\boverflow\b`),
	regexp.MustCompile(`\balignment\b`),
	regexp.MustCompile(`\balign\b`),
	regexp.MustCompile(`\bjustify\b`),
	regexp.MustCompile(`\bbreakpoint\b`),
	regexp.MustCompile(`\bui\b`),
	regexp.MustCompile(`\bdom\b`),
	regexp.MustCompile(`\bcomponent\b`),
	regexp.MustCompile(`\brearrange\b`),
	regexp.MustCompile(`\breorder\b`),
	regexp.MustCompile(`\babove\b`),
	regexp.MustCompile(`\bbelow\b`),
	regexp.MustCompile(`\bbeneath\b`),
	regexp.MustCompile(`\bwebpage\b`),
	regexp.MustCompile(`\brender\b`),
	regexp.MustCompile(`\brendering\b`),
}

// frontendUIPhrases are longer phrases that do not need word boundaries.
var frontendUIPhrases = []string{
	"user interface", "user-interface",
	"media query", "media queries",
	"dom tree",
	"z index",
	"web page",
	"re-order",
}

// IsFrontendUI reports whether the input describes a UI/Layout/Styling task
// that requires AST + CSS/DOM inspection before any code mutation.
func IsFrontendUI(input string) bool {
	lower := strings.ToLower(input)
	for _, p := range frontendUIWordPatterns {
		if p.MatchString(lower) {
			return true
		}
	}
	for _, p := range frontendUIPhrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// IsTrivialMutation reports whether the input describes a genuinely trivial,
// single-token mutation that can bypass /plan. Returns true ONLY for:
//   - typo/spelling/grammar fixes
//   - comment updates
//   - description/version bumps
//   - single bare verb (rename, format, correct) on a doc-only file
//   - NO frontend UI context
func IsTrivialMutation(input string) bool {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return false
	}
	msg := commandPrefixPattern.ReplaceAllString(raw, "")
	msg = strings.TrimSpace(msg)

	// If it's a frontend UI task, it's NEVER trivial.
	if IsFrontendUI(msg) {
		return false
	}

	lower := strings.ToLower(msg)

	// Check multi-word trivial mutation phrases.
	// When a phrase matches, also verify the file references (if any) are doc files.
	for _, p := range trivialMutationPhrases {
		if strings.Contains(lower, p) {
			files := extractFileRefs(msg)
			if len(files) == 0 {
				files = extractBareFilenames(msg)
			}
			if len(files) > 0 {
				allDoc := true
				for _, fn := range files {
					if !isDirectMutationTarget(fn) {
						allDoc = false
						break
					}
				}
				return allDoc
			}
			return true
		}
	}

	// Check bare trivial mutation verbs (renames, format, etc.)
	// These must reference a doc-only file.
	fields := strings.Fields(lower)
	for _, f := range fields {
		clean := strings.Trim(f, `.,;:'"!?()`)
		for _, v := range trivialMutationBareVerbs {
			if clean == v {
				// Must have a doc file reference to qualify.
				files := extractFileRefs(msg)
				if len(files) == 0 {
					files = extractBareFilenames(msg)
				}
				if len(files) > 0 {
					allDoc := true
					for _, fn := range files {
						if !isDirectMutationTarget(fn) {
							allDoc = false
							break
						}
					}
					return allDoc
				}
			}
		}
	}

	return false
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

// ClassifyDirectMutation inspects user input to determine whether it is a
// simple single-file text mutation that should bypass the Senior Architect
// pipeline and route directly to the /build engine as a FILE_MUTATE task.
//
// STRICT POLICY (REFORM):
//   - Only IsTrivialMutation inputs qualify (typo fixes, comment updates,
//     version bumps, single renames on doc files).
//   - FRONTEND_UI tasks NEVER fast-track — they MUST route through /plan.
//   - Multi-file or architectural requests NEVER fast-track.
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

	// STRICT REFORM: only trivial mutations bypass /plan.
	if !IsTrivialMutation(msg) {
		return command.FallbackPlanTarget{}, false
	}

	// If the input carries diagnostic intent, never fast-track.
	if hasDiagnosticIntent(msg) {
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

// IsHotTrack reports whether the input carries the $hot fast-track prefix.
// $hot bypasses ALL plan generation, diagnostic loops, and Senior Architect
// analysis, routing directly to the /build engine for instant execution.
func IsHotTrack(input string) bool {
	return hotPrefixPattern.MatchString(strings.TrimSpace(input))
}

// HasHighIntentFlag reports whether the input explicitly requests high-intent
// analysis via --high or /intent high.
func HasHighIntentFlag(input string) bool {
	lower := strings.ToLower(input)
	return strings.Contains(lower, "--high") || strings.Contains(lower, "/intent high")
}

// ClassifyIntentMode determines whether a non-diagnostic user request should
// route through /investigate (bug diagnostics), /plan (architectural/UI work),
// or go directly to /build (trivial trivial mutations).
//
// REFORM RULES:
//   - Diagnostic patterns (why, what caused, investigate, crash, bug) → investigate
//   - FRONTEND_UI tasks (layout, styling, positioning) → plan (NEVER build)
//   - Architectural keywords (migrate, redesign, architecture) → plan
//   - Trivial mutations only (typo fix, comment update, rename on doc) → build
//   - All other mutation intents → plan (safer default — forces /plan analysis)
//   - $hot prefix → build (explicit bypass)
func ClassifyIntentMode(input string) string {
	if IsHotTrack(input) {
		return "build"
	}
	lower := strings.ToLower(input)

	// Diagnostic patterns → investigate.
	for _, p := range diagnosticPatterns {
		if p.MatchString(lower) {
			return "investigate"
		}
	}

	if hasDiagnosticIntent(input) {
		return "investigate"
	}

	// FRONTEND_UI tasks → plan (enforces Layer 3 Hybrid Search before edits).
	if IsFrontendUI(input) {
		return "plan"
	}

	// Architectural keywords → plan.
	architecturalPatterns := []string{
		"architecture", "migrate", "redesign", "restructure",
		"cross-cutting", "schema change", "database migration",
	}
	for _, p := range architecturalPatterns {
		if strings.Contains(lower, p) {
			return "plan"
		}
	}

	// Trivial mutations only → build (fast-track).
	if IsTrivialMutation(input) {
		return "build"
	}

	// Mutation intent + file ref — route to plan unless truly trivial.
	if hasDirectMutationVerb(input) {
		return "plan"
	}

	// Default: let the mode resolver decide.
	return ""
}

// StripHotPrefix removes the $hot prefix from the input, returning the clean
// command string. If no $hot prefix is found, returns the input unchanged.
func StripHotPrefix(input string) string {
	return hotPrefixPattern.ReplaceAllString(input, "")
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

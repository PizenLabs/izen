package plan

import (
	"regexp"
	"strings"
)

// localModelPrefixes lists model name prefixes that identify local/on-device
// SLMs (Ollama, llama.cpp, etc.). Local 7B-class models choke on large forensic
// context, so the handoff mapper must aggressively truncate the ledger before
// dispatch. Mirrors the conservative ceilings in budget.ModelTokenBudget.
var localModelPrefixes = []string{
	"qwen2.5-coder:",
	"qwen2:",
	"qwen:",
	"llama",
	"codellama:",
	"deepseek-coder:",
	"phi3:",
	"phi4:",
	"gemma2:",
	"gemma:",
	"mistral:",
	"mixtral:",
	"tinyllama:",
	"orca:",
	"vicuna:",
	"stable-code:",
	"starcoder2:",
	"starcoder:",
}

// IsLocalModel reports whether modelName refers to a local/on-device SLM that
// requires aggressive context truncation to avoid first-token timeouts.
func IsLocalModel(modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if name == "" {
		return false
	}
	for _, p := range localModelPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	// Default ollama model without explicit prefix still indicates a local SLM.
	if name == "qwen2.5-coder:7b" {
		return true
	}
	return false
}

// MaxLedgerChars is the hard ceiling (in characters) for the forensic ledger
// injected into a local model. ~4 chars/token yields a ~1.5k token budget, which
// fits comfortably under budget.ModelTokenBudget for 7B-class models so the
// first token reliably arrives within the 8s guard.
const MaxLedgerChars = 4000

// compilationErrorMarkers identify ledger content that can be resolved purely
// through environment/dependency setup rather than a deep architectural plan.
// The list is deliberately broad and includes truncated prefixes so that
// partially-clipped terminal output (e.g. "no required modul…") still trips the
// detector instead of forcing a manual /investigate re-run.
var compilationErrorMarkers = []string{
	"no required module",
	"no required module provides package",
	// Fuzzy/truncated prefixes — the UI may slice the ledger mid-word, so we
	// match the surviving fragment rather than the full phrase.
	"no required modul",
	"no required mod",
	"missing Go module",
	"missing module",
	"finding module for package",
	"cannot find module",
	"missing go.sum entry",
	"go: go.mod",
	"build failed",
	"build error",
	"command-line-arguments",
	"undefined:",
	"could not import",
	"package ",
	"go mod",
	"module provides package",
	"module declares its path as",
	"failed to load",
	"compilation failed",
	"compile error",
	"exit status",
	"error:",
	"not found",
	"rootless docker",
	"container runtime",
	"gcc:",
	"cc1:",
	"ld:",
	"failed tests:",
	"stacktrace",
	"stack trace",
}

// hypothesisMarkers capture confirmed root-cause / hypothesis status lines so
// they survive truncation even when buried among verbose stack dumps.
var hypothesisMarkers = []string{
	"hypothesis:",
	"root cause:",
	"confirmed:",
	"status:",
	"conclusion:",
	"diagnosis:",
	"likely cause:",
	"resolved hypothesis",
}

// coordinateRe matches file:line:col error coordinates embedded in compiler
// output (e.g. cmd/api/main.go:7:5: no required module...).
var coordinateRe = regexp.MustCompile(`[^\s:]+\.(go|ts|js|py|rs|cpp|c|h|java):\d+:\d+:`)

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TruncateLedger compresses a raw forensic ledger to a hard character ceiling
// while preserving the signal a local model actually needs: the core error line
// and any confirmed hypothesis / root-cause status. Redundant stack traces,
// repeating build footers, and verbose environment dumps are discarded.
//
// The algorithm is a greedy keep-drop pass:
//  1. Every line is scored; high-value lines (error coordinates, hypothesis
//     status, module/dependency messages) are always retained first.
//  2. Remaining lines are appended in order until the hard MaxLedgerChars
//     ceiling is hit, after which everything else is dropped.
//  3. If the ceiling was exceeded, a single compact core-error summary is
//     used so the model never loses the canonical failure coordinate.
func TruncateLedger(ledger string, maxChars int) string {
	ledger = strings.TrimSpace(ledger)
	if ledger == "" {
		return ""
	}
	if maxChars <= 0 {
		maxChars = MaxLedgerChars
	}
	if len(ledger) <= maxChars {
		return ledger
	}

	lines := strings.Split(ledger, "\n")
	kept := make([]string, 0, len(lines))
	total := 0

	enough := func(add string) bool {
		return total+len(add)+1 > maxChars
	}

	// Pass 1: always keep high-value diagnostic lines first.
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			continue
		}
		if isHighValueLine(trimmed) {
			if enough(trimmed) {
				break
			}
			kept = append(kept, trimmed)
			total += len(trimmed) + 1
		}
	}

	// Pass 2: fill remaining budget with the earliest contextual lines
	// (file:line:col coordinates and short frames) in original order.
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			continue
		}
		if containsAny(trimmed, hypothesisMarkers) || isHighValueLine(trimmed) {
			continue // already retained in pass 1
		}
		if enough(trimmed) {
			break
		}
		kept = append(kept, trimmed)
		total += len(trimmed) + 1
	}

	if len(kept) == 0 {
		// Extreme case: every line individually exceeded the budget. Keep the
		// single most diagnostic substring so the failure coordinate survives.
		return CoreErrorLine(ledger)
	}

	result := strings.Join(kept, "\n")
	if len(result) > maxChars {
		result = result[:maxChars]
	}
	return result
}

// isHighValueLine reports whether a ledger line carries a core error coordinate
// or a confirmed hypothesis status worth preserving under hard truncation.
func isHighValueLine(line string) bool {
	if containsAny(line, hypothesisMarkers) {
		return true
	}
	if containsAny(line, compilationErrorMarkers) {
		return true
	}
	// file:line:col error coordinates (e.g. cmd/api/main.go:7:5: no required module)
	return coordinateRe.MatchString(line)
}

// CoreErrorLine extracts the single most diagnostic line from a raw ledger —
// the first line that carries an error coordinate or a module/dependency
// message. Falls back to the first non-empty line when no marker matches.
func CoreErrorLine(ledger string) string {
	for _, ln := range strings.Split(ledger, "\n") {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			continue
		}
		if isHighValueLine(trimmed) {
			return trimmed
		}
	}
	for _, ln := range strings.Split(ledger, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}

// IsCompilationOrDependencyError reports whether the ledger contains only
// compilation / dependency / environment errors that can be resolved via
// module/setup commands rather than a deep architectural plan.
// goFileDependencyRe matches a compiler coordinate (e.g. `main.go:7:5:`) whose
// message begins with a dependency/import error fragment. This catches both the
// full phrase and the truncated "no required modul…" variant that the UI may
// have clipped from the ledger.
var goFileDependencyRe = regexp.MustCompile(`[^\s:]+\.go:\d+:\d+:\s*no required`)

// goFileParseRe matches any *.go compiler coordinate that is followed by a
// parsing or import failure indicator, signalling a module/environment
// discrepancy rather than a pure source-logic bug.
var goFileParseRe = regexp.MustCompile(`[^\s:]+\.go:\d+:\d+:`)

// parseErrorIndicators are the secondary signals that, when paired with a *.go
// coordinate, imply a module/import resolution failure.
var parseErrorIndicators = []string{
	"no required",
	"could not import",
	"missing module",
	"cannot find module",
	"undefined:",
	"import",
	"package ",
	"build failed",
	"compilation failed",
}

// hasGoFileParseError reports whether the content contains a *.go compile
// coordinate paired with a parsing/import error indicator — i.e. evidence of a
// raw source-file compile failure that implies a module/environment issue.
func hasGoFileParseError(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	for _, line := range strings.Split(trimmed, "\n") {
		if goFileParseRe.MatchString(line) && containsAny(line, parseErrorIndicators) {
			return true
		}
	}
	return false
}

// content describes a build/dependency blocker that can be resolved via module
// tooling (go mod tidy / go get) rather than a deep architectural plan.
//
// Detection is intentionally fuzzy: it accepts strict phrases, truncated
// prefixes, the `*.go:N:M: no required` coordinate regex, and any *.go compile
// coordinate combined with a parsing/import error indicator, so partially
// clipped terminal output still trips the detector.
func IsCompilationOrDependencyError(ledger string) bool {
	trimmed := strings.TrimSpace(ledger)
	if trimmed == "" {
		return false
	}
	if containsAny(trimmed, compilationErrorMarkers) {
		return true
	}
	// Coordinate form: `main.go:7:5: no required module provides package ...`
	if goFileDependencyRe.MatchString(trimmed) {
		return true
	}
	// Any *.go coordinate paired with an import/parse indicator.
	if goFileParseRe.MatchString(trimmed) {
		for _, line := range strings.Split(trimmed, "\n") {
			if goFileParseRe.MatchString(line) && containsAny(line, parseErrorIndicators) {
				return true
			}
		}
	}
	return false
}

// ExtractConclusionFromLedger scans a formatted ledger string (as produced by
// FormatPacketsForPlan / FormatForPlan) for a conclusion packet and returns
// its payload. Returns empty string if no conclusion is found.
//
// The conclusion packet appears in the format:
//
//	[PKT-N] kind=conclusion title="Investigation conclusion"
//	  <indented payload lines>
func ExtractConclusionFromLedger(ledger string) string {
	lines := strings.Split(ledger, "\n")
	inConclusion := false
	var payloadLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[PKT-") && strings.Contains(trimmed, "kind=conclusion") {
			inConclusion = true
			continue
		}
		if inConclusion {
			if strings.HasPrefix(trimmed, "[PKT-") {
				break
			}
			if trimmed != "" && !strings.HasPrefix(trimmed, "Total packets:") &&
				!strings.HasPrefix(trimmed, "### ANALYTICAL PACKETS") {
				payloadLines = append(payloadLines, trimmed)
			}
		}
	}
	if len(payloadLines) > 0 {
		return strings.Join(payloadLines, " ")
	}
	return ""
}

// FastTrackPrompt builds the lightweight shell-execution prompt used when the
// user has 0 explicit TODOs but faces a dependency/compilation blocker. The
// heavy architectural plan-generation loop is skipped in favour of an immediate,
// minimal resolution plan for a local SLM.
//
// When conclusion is non-empty (the investigation's resolved diagnosis), it is
// injected BEFORE the raw error so the model sees the corrected architectural
// path — preventing the model from re-deriving a stale or incorrect fix from
// raw error text alone.
//
// It explicitly requests the strict - [ ] SHELL_EXEC: <cmd> | <desc> task syntax
// so the plan parser (ParseMarkdownToTasks) can extract actionable tasks without
// the JSON-schema enforcement used by the full path. It also forbids placeholder
// paths (relative/path/to/file.go, file_test.go, etc.) and FILE_MUTATE tasks,
// since a dependency/compilation blocker is resolved purely via shell commands.
func FastTrackPrompt(coreError, conclusion string) string {
	if coreError == "" {
		coreError = "an unknown dependency/compilation blocker"
	}
	prompt := "The user has 0 explicit code TODOs but faces a dependency/compilation " +
		"blocker: " + coreError + "."
	if conclusion != "" {
		prompt += "\n\nThe investigation already diagnosed this issue and reached " +
			"a conclusion. Cross-reference your plan against this diagnosis:\n" +
			conclusion +
			"\n\nIf the conclusion identifies a corrected dependency path " +
			"(e.g. 'github.com/moby/moby/client'), your SHELL_EXEC target MUST use " +
			"that corrected path — do NOT re-derive from raw error text."
	}
	prompt += " Resolve it with shell commands ONLY. " +
		"Output ONLY a markdown checklist in this EXACT format, no preamble, no " +
		"code fences, no extra text:\n" +
		"- [ ] SHELL_EXEC: <exact shell command> | <one-line why>\n" +
		"- [ ] SHELL_EXEC: <exact shell command> | <one-line why>\n" +
		"- [ ] SHELL_EXEC: <exact shell command> | <one-line why>\n" +
		"RULES: use ONLY the SHELL_EXEC task type. NEVER emit FILE_MUTATE tasks. " +
		"NEVER use placeholder paths like 'relative/path/to/file.go' or " +
		"'file_test.go'. The SHELL_EXEC target must be a real, runnable command " +
		"(e.g. 'go get github.com/foo/bar' or 'go mod tidy')."
	return prompt
}

package context

import (
	"regexp"
	"strings"
)

// ── Ledger Handoff Sanitizer ──────────────────────────────────────────────────
//
// handoffLedgerContent (FormatLedgerForPlan output) can be large and repetitive:
// interleaved system stack traces, container/runtime setup logs, ANSI escapes,
// and duplicated diagnostic blocks. Feeding that raw into the plan engine
// inflates the prompt and risks stalling local LLM generation loops.
//
// SanitizeLedger collapses that noise into a compact, structured handoff:
// it strips ANSI, repeated stack frames, verbose runtime/container logs, and
// boilerplate, keeping only the signal the engine needs (the failure scope,
// the failing file/package, the offending dependency, and the actionable
// stack tail). It is intentionally file-agnostic — it does NOT hardcode any
// specific error or path.

var (
	ansiRE          = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	stackFrameRE    = regexp.MustCompile(`^\s*(?:[a-zA-Z0-9_./\-]+\.(?:go|rs|js|ts|py):\d+|[a-zA-Z0-9_./\-]+\([^)]*\)|\t[a-zA-Z0-9_.\*/]+\.[a-zA-Z0-9_]+)`)
	runtimeLogRE    = regexp.MustCompile(`(?im)^\s*(?:time=|level=|container (?:id|setup)|rootless|docker[d]? (?:daemon|engine)|pulling image|downloading|extracting layer|golang\.org)|pulling image|^\s*goroutine \d+ \[`)
	dupBlankLineRE  = regexp.MustCompile(`\n{3,}`)
	keyValueNoiseRE = regexp.MustCompile(`(?im)^\s*(?:(?:trace|debug|info|warn|verbose)\s*[:=].*)$`)
)

// SanitizeLedger returns a slimmed, de-duplicated version of the raw ledger
// handoff content. It never hardcodes specific errors; it applies structural
// noise reduction only. Empty / already-clean input passes through unchanged.
func SanitizeLedger(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}

	// 1. Strip ANSI escapes.
	clean := ansiRE.ReplaceAllString(raw, "")

	// 2. Drop per-line noise: log-level chatter and verbose runtime/container
	//    setup lines that carry no failure signal. Whole lines are removed.
	clean = keyValueNoiseRE.ReplaceAllString(clean, "")
	clean = dropMatchingLines(clean, runtimeLogRE)

	// 3. Collapse repeated stack frames. Stack traces repeat the same frames
	//    across panic/retry boundaries; keep each unique frame once, preserving
	//    first-seen order so the actionable tail survives.
	clean = collapseStackFrames(clean)

	// 4. Normalize whitespace: collapse 3+ blank lines, trim trailing space.
	clean = dupBlankLineRE.ReplaceAllString(clean, "\n\n")
	clean = strings.TrimSpace(clean)

	return clean
}

// dropMatchingLines removes any line that matches re entirely, collapsing the
// resulting gap so the output stays compact.
func dropMatchingLines(s string, re *regexp.Regexp) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if re.MatchString(line) {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// collapseStackFrames removes duplicate stack-frame lines while preserving the
// first occurrence (which keeps the most relevant call path). Non-frame lines
// are passed through verbatim.
func collapseStackFrames(s string) string {
	var b strings.Builder
	seen := make(map[string]struct{})
	for _, line := range strings.Split(s, "\n") {
		if stackFrameRE.MatchString(line) {
			key := strings.TrimSpace(line)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

package investigate

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

type EvidenceSource string

const (
	EvSourceGraph     EvidenceSource = "graph"
	EvSourceSemantic  EvidenceSource = "semantic"
	EvSourceRipgrep   EvidenceSource = "ripgrep"
	EvSourceRead      EvidenceSource = "read"
	EvSourceTest      EvidenceSource = "test"
	EvSourceStack     EvidenceSource = "stacktrace"
	EvSourceUser      EvidenceSource = "user"
	EvSourceLog       EvidenceSource = "log"
	EvSourceExecution EvidenceSource = "execution"
)

type ErrorCategory string

const (
	ErrCatCompilation ErrorCategory = "compilation"
	ErrCatEnvironment ErrorCategory = "environment"
	ErrCatTestFailure ErrorCategory = "test_failure"
	ErrCatUnknown     ErrorCategory = "unknown"
)

func ClassifyLogOutput(content string) []ErrorCategory {
	cats := make(map[ErrorCategory]bool)

	lower := strings.ToLower(content)
	if strings.Contains(lower, "no required module provides package") ||
		strings.Contains(lower, "cannot find package") ||
		strings.Contains(lower, "undefined:") ||
		strings.Contains(lower, "go.mod") ||
		strings.Contains(lower, "no required module") ||
		strings.Contains(lower, "missing dependency") ||
		strings.Contains(lower, "package is not in") ||
		strings.Contains(lower, "module declares its path as") {
		cats[ErrCatCompilation] = true
	}

	if strings.Contains(lower, "rootless docker not found") ||
		strings.Contains(lower, "docker daemon") ||
		strings.Contains(lower, "failed to create docker provider") ||
		strings.Contains(lower, "could not start") ||
		strings.Contains(lower, "container runtime") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "testcontainers") {
		cats[ErrCatEnvironment] = true
	}

	if strings.Contains(lower, "--- fail") ||
		strings.Contains(lower, "panic:") ||
		strings.Contains(lower, "assertion failed") ||
		strings.Contains(lower, "test failed") ||
		strings.Contains(lower, "fail") {
		cats[ErrCatTestFailure] = true
	}

	if len(cats) == 0 {
		cats[ErrCatUnknown] = true
	}

	result := make([]ErrorCategory, 0, len(cats))
	for c := range cats {
		result = append(result, c)
	}
	return result
}

const (
	maxLogInputBytes = 256 * 1024 // 256KB ceiling before truncation
	maxStackFrames   = 30         // max frames to keep after pre-processing
	maxLineLength    = 2000       // single line truncation threshold
	maxOutputLines   = 500        // total output lines cap
)

// ── Compiler Diagnostic Patterns ──────────────────────────────────────────────
// These patterns identify lines in a Go build/test output that carry actionable
// diagnostic signal: error locations (file:line:col), error messages, and
// explicit mitigation directives. Lines NOT matching any signal pattern are
// considered ambient noise (container logs, download progress, etc.) and are
// aggressively pruned when compiler diagnostics are detected.

var compilerSignalPrefixes = []string{
	"# ", // Go build package header
	"no required module",
	"cannot find package",
	"package is not in",
	"missing dependency",
	"to add it:",
	"suggestion:",
	"did you mean?",
	"undefined:",
	"go: finding",
	"go: downloading",
	"go: extracting",
}

func isCompilerSignalLine(s string) bool {
	if isGoErrorLocation(s) {
		return true
	}
	lower := strings.ToLower(s)
	for _, p := range compilerSignalPrefixes {
		if strings.Contains(lower, p) {
			return true
		}
	}
	// Go build error continuations: lines starting with whitespace that
	// follow a compiler signal (carried over in the two-pass scan).
	return false
}

// isGoErrorLocation matches Go compiler error tokens of the form
// "file.go:line:col:" or "file.go:line:" — the canonical Go error locus.
func isGoErrorLocation(s string) bool {
	// Match: <path>/<file>.go:<digits>[:<digits>]:
	// e.g. "cmd/api/main.go:7:2:" or "./foo.go:42:"
	idx := strings.Index(s, ".go:")
	if idx < 0 {
		return false
	}
	// After ".go:" there must be at least one digit (line number).
	after := s[idx+4:]
	if len(after) == 0 || after[0] < '0' || after[0] > '9' {
		return false
	}
	// Digit run for line number.
	colStart := 1
	for colStart < len(after) && after[colStart] >= '0' && after[colStart] <= '9' {
		colStart++
	}
	// After the line number, expect ':' or ' ' or end-of-string (for col number or message).
	if colStart < len(after) && after[colStart] == ':' {
		return true
	}
	return true
}

// extractCompilerDiagnostics performs aggressive signal-only extraction when Go
// compiler diagnostics are detected. It keeps ONLY:
//   - Error location tokens (file:line:col)
//   - Compiler error messages (undefined:, cannot use, etc.)
//   - Mitigation directives (to add it:, suggestion:, did you mean?)
//   - Package header markers (# pkg/path)
//
// ALL ambient noise (container runtime setup, Docker logs, download progress,
// environment checks) is stripped. This reduces the context footprint to the
// minimal token set the local model needs for high-speed plan synthesis.
func extractCompilerDiagnostics(raw string) string {
	lines := strings.Split(raw, "\n")
	signalLines := make(map[int]string)

	// Pass 1: identify signal lines and their 1-line lookahead context.
	for i, line := range lines {
		trimmed := strings.TrimRightFunc(line, unicode.IsSpace)
		clean := stripANSICodes(trimmed)
		if clean == "" {
			continue
		}
		if isCompilerSignalLine(clean) || isGoErrorLocation(clean) {
			signalLines[i] = clean
			continue
		}
		// Check the lower-cased clean version for known signal substrings.
		lower := strings.ToLower(clean)
		for _, p := range compilerSignalPrefixes {
			if strings.Contains(lower, p) {
				signalLines[i] = clean
				break
			}
		}
	}

	if len(signalLines) == 0 {
		return raw
	}

	// Pass 2: collect signals in order, with 1-line lookahead context for
	// multi-line error descriptions (e.g. "to add it: go get ..." may be on
	// the next line after the file:line:col token).
	var out []string
	maxIdx := len(lines) - 1
	for i := 0; i <= maxIdx; i++ {
		if _, ok := signalLines[i]; ok {
			out = append(out, signalLines[i])
			// Include the NEXT line if it exists and carries continuation.
			if i+1 <= maxIdx {
				nextClean := stripANSICodes(strings.TrimRightFunc(lines[i+1], unicode.IsSpace))
				if nextClean != "" && !isNoiseLine(nextClean) {
					if _, nextIsSignal := signalLines[i+1]; !nextIsSignal {
						out = append(out, nextClean)
						i++ // skip on next iteration
					}
				}
			}
		}
	}

	condensed := strings.Join(out, "\n")
	condensed = strings.TrimSpace(condensed)
	if condensed == "" {
		return raw
	}
	return condensed
}

// BoundedLogPreprocessor intercepts raw terminal/CI failure output and
// extracts only the diagnostic signal — stack traces, error messages, and
// test failure markers — while stripping high-volume noise (ANSI codes,
// build cache logs, repetitive progress bars, Go module downloads, etc.).
// Returns a token-safe condensed payload.
//
// When Go compiler diagnostics are detected, the preprocessor switches to
// aggressive signal-only mode: it extracts only error location tokens
// (file:line:col), error messages, and mitigation directives, stripping ALL
// ambient environment/container setup logs.
func BoundedLogPreprocessor(raw string) string {
	if len(raw) > maxLogInputBytes {
		raw = raw[:maxLogInputBytes]
	}

	// ── COMPILER-DIAGNOSTIC-FAST-PATH ──────────────────────────────────
	// If the raw output contains Go compiler diagnostics, switch to
	// aggressive signal-only extraction. This strips ambient noise
	// (container logs, Docker setup, download progress) that bloats the
	// context window and causes local LLM timeouts in /plan synthesis.
	if hasCompilerDiagnostics(raw) {
		result := extractCompilerDiagnostics(raw)
		if result != raw && strings.TrimSpace(result) != "" {
			return result
		}
	}

	lines := strings.Split(raw, "\n")
	if len(lines) > maxOutputLines {
		lines = lines[:maxOutputLines]
	}

	var out []string
	var stackFrameCount int
	inNoiseBlock := false
	noiseBlockLines := 0

	for _, line := range lines {
		trimmed := strings.TrimRightFunc(line, unicode.IsSpace)
		clean := stripANSICodes(trimmed)

		if clean == "" {
			if inNoiseBlock {
				noiseBlockLines++
				if noiseBlockLines > 3 {
					continue
				}
			}
			out = append(out, "")
			continue
		}

		if isNoiseLine(clean) {
			if !inNoiseBlock {
				inNoiseBlock = true
				noiseBlockLines = 0
			}
			noiseBlockLines++
			if noiseBlockLines > 2 {
				continue
			}
			continue
		}
		inNoiseBlock = false
		noiseBlockLines = 0

		if isStackFrameLine(clean) {
			stackFrameCount++
			if stackFrameCount > maxStackFrames {
				continue
			}
		}

		if len(clean) > maxLineLength {
			clean = clean[:maxLineLength] + "..."
		}

		out = append(out, clean)
	}

	condensed := strings.Join(out, "\n")
	condensed = strings.TrimSpace(condensed)
	if condensed == "" {
		return raw
	}
	return condensed
}

// hasCompilerDiagnostics checks whether the raw output contains any Go
// compiler diagnostic patterns that trigger aggressive signal-only extraction.
func hasCompilerDiagnostics(raw string) bool {
	lower := strings.ToLower(raw)
	// Core Go compiler patterns that signal actionable compile errors.
	return strings.Contains(lower, "no required module provides package") ||
		strings.Contains(lower, "cannot find package") ||
		strings.Contains(lower, "undefined:") ||
		strings.Contains(lower, "to add it:") ||
		(strings.Contains(lower, "compile") && (containsGoErrorLocation(raw) || strings.Contains(lower, "go build")))
}

// containsGoErrorLocation returns true if the string contains at least one
// Go error location token (<file>.go:<line>).
func containsGoErrorLocation(s string) bool {
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		if isGoErrorLocation(line) {
			return true
		}
	}
	return false
}

func stripANSICodes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 'A' || s[j] > 'Z') && (s[j] < 'a' || s[j] > 'z') {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isNoiseLine(s string) bool {
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "ok  ") || strings.HasPrefix(lower, "?   "):
		return false
	case strings.Contains(lower, "download") && strings.Contains(lower, "go"):
		return true
	case strings.Contains(lower, "cache") && strings.Contains(lower, "generated"):
		return true
	case strings.HasPrefix(lower, "progress"):
		return true
	case strings.HasPrefix(lower, "#") && strings.Contains(lower, "downloading"):
		return true
	case strings.Contains(lower, "go: finding module"):
		return true
	case strings.Contains(lower, "go: downloading"):
		return true
	case strings.Contains(lower, "go: extracting"):
		return true
	case strings.Count(lower, ".") > 20 && len(lower) > 100:
		return true
	default:
		return false
	}
}

func isStackFrameLine(s string) bool {
	switch {
	case strings.Contains(s, ".go:"):
		return true
	case strings.Contains(s, "File ") && strings.Contains(s, "line "):
		return true
	case strings.HasPrefix(s, "at ") && strings.Contains(s, ".go:"):
		return true
	case strings.HasPrefix(s, "\tat "):
		return true
	default:
		return false
	}
}

type Evidence struct {
	ID         string          `json:"id"`
	ContextID  string          `json:"context_id,omitempty"`
	Source     EvidenceSource  `json:"source"`
	Content    string          `json:"content"`
	File       string          `json:"file,omitempty"`
	Line       int             `json:"line,omitempty"`
	Confidence float64         `json:"confidence"`
	Strategy   string          `json:"strategy,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	Label      string          `json:"label,omitempty"`
	Categories []ErrorCategory `json:"categories,omitempty"`
}

type EvidenceStore struct {
	evidence []Evidence
	nextID   int
}

func NewEvidenceStore() *EvidenceStore {
	return &EvidenceStore{}
}

func (es *EvidenceStore) Add(source EvidenceSource, content, file string, line int, confidence float64) *Evidence {
	return es.AddWithContext("", source, content, file, line, confidence)
}

func (es *EvidenceStore) AddWithContext(ctxID string, source EvidenceSource, content, file string, line int, confidence float64) *Evidence {
	es.nextID++
	ev := &Evidence{
		ID:         fmt.Sprintf("EV%d", es.nextID),
		ContextID:  ctxID,
		Source:     source,
		Content:    content,
		File:       file,
		Line:       line,
		Confidence: confidence,
		CreatedAt:  time.Now(),
		Categories: ClassifyLogOutput(content),
	}
	es.evidence = append(es.evidence, *ev)
	return &es.evidence[len(es.evidence)-1]
}

func (es *EvidenceStore) AddWithStrategy(source EvidenceSource, content, file string, line int, confidence float64, strategy string) *Evidence {
	ev := es.AddWithContext("", source, content, file, line, confidence)
	ev.Strategy = strategy
	return ev
}

func (es *EvidenceStore) Get(id string) *Evidence {
	for i := range es.evidence {
		if es.evidence[i].ID == id {
			return &es.evidence[i]
		}
	}
	return nil
}

func (es *EvidenceStore) All() []Evidence {
	return es.evidence
}

func (es *EvidenceStore) BySource(source EvidenceSource) []Evidence {
	var filtered []Evidence
	for _, ev := range es.evidence {
		if ev.Source == source {
			filtered = append(filtered, ev)
		}
	}
	return filtered
}

func (es *EvidenceStore) ByCategory(cat ErrorCategory) []Evidence {
	var filtered []Evidence
	for _, ev := range es.evidence {
		for _, c := range ev.Categories {
			if c == cat {
				filtered = append(filtered, ev)
				break
			}
		}
	}
	return filtered
}

func (es *EvidenceStore) ByFile(file string) []Evidence {
	var filtered []Evidence
	for _, ev := range es.evidence {
		if ev.File == file {
			filtered = append(filtered, ev)
		}
	}
	return filtered
}

func (es *EvidenceStore) HighConfidence(threshold float64) []Evidence {
	var filtered []Evidence
	for _, ev := range es.evidence {
		if ev.Confidence >= threshold {
			filtered = append(filtered, ev)
		}
	}
	return filtered
}

func (es *EvidenceStore) HasCategory(cat ErrorCategory) bool {
	for _, ev := range es.evidence {
		for _, c := range ev.Categories {
			if c == cat {
				return true
			}
		}
	}
	return false
}

func (es *EvidenceStore) ByAnyCategory() map[ErrorCategory][]Evidence {
	classified := make(map[ErrorCategory][]Evidence)
	for _, ev := range es.evidence {
		for _, c := range ev.Categories {
			classified[c] = append(classified[c], ev)
		}
	}
	return classified
}

func (es *EvidenceStore) Count() int {
	return len(es.evidence)
}

func (es *EvidenceStore) Clear() {
	es.evidence = nil
	es.nextID = 0
}

func (es *EvidenceStore) Summary() string {
	sources := make(map[EvidenceSource]int)
	for _, ev := range es.evidence {
		sources[ev.Source]++
	}
	return fmt.Sprintf("Total evidence: %d", len(es.evidence))
}

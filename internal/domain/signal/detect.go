package signal

import (
	"regexp"
	"strings"
)

// All substring / regex matching on free-text output for control-plane routing
// is confined to this classifier. Detect converts raw compiler, test and
// investigation-ledger output into typed Signal values so routing code in
// plan/investigate/build/ui evaluates SignalKind instead of re-parsing
// terminal text.
//
// The classifier is deliberately comprehensive and mirrors the legacy marker
// sets (truncate.go's compilationErrorMarkers, update.go's heuristic
// detectors) so behaviour is preserved while routing moves onto typed signals.

var (
	// undefinedSymbolRE matches Go compiler "undefined: Symbol" errors:
	//
	//	cmd/api/main.go:24:2: undefined: Log
	//	# go-template/cmd/api\ncmd/api/main.go:24:2: undefined: Log
	//	cmd/api/main.go:24: undefined: Log
	undefinedSymbolRE = regexp.MustCompile(
		`(?m)^\s*(?:#\s*\S+\s+)?([^:\s]+):(\d+)(?::(\d+))?:\s*undefined:\s*([A-Za-z0-9_]+)`)

	// canonicalMismatchRE matches Go canonical import path mismatch errors:
	//
	//	module declares its path as: "example.com/new" but was required as: "example.com/old"
	//	module declares its path as: example.com/new
	//	but was required as: example.com/old
	canonicalMismatchRE = regexp.MustCompile(
		`(?i)module declares its path as:\s*["']?([^\s"']+)["']?(?:\s*but was required as:\s*["']?([^\s"']+)["']?)?`)

	// blockerTokenRE matches the investigate "lx bypassed" remote dependency
	// blocker token that plan consumes for deterministic go get short-circuits:
	//
	//	## REMOTE DEPENDENCY BLOCKER (lx bypassed): github.com/moby/moby/client
	blockerTokenRE = regexp.MustCompile(
		`(?i)##\s*REMOTE DEPENDENCY BLOCKER\s*\(lx bypassed\):\s*(.+)`)

	// goDependencyCoordinateRE matches a *.go compiler coordinate immediately
	// followed by a dependency-resolution fragment:
	//
	//	main.go:7:5: no required module provides package ...
	goDependencyCoordinateRE = regexp.MustCompile(`[^\s:]+\.go:\d+:\d+:\s*no required`)

	// goCoordinateRE matches any *.go compiler coordinate:
	//
	//	cmd/api/main.go:12:3: message
	goCoordinateRE = regexp.MustCompile(`[^\s:]+\.go:\d+:\d+:`)
)

// depMissingMarkers are free-text fragments that identify a missing dependency,
// module or package that module tooling (go get / go mod tidy) can resolve.
// Fuzzy/truncated prefixes are included deliberately: the UI may slice the
// ledger mid-word and the surviving fragment must still trip the classifier.
var depMissingMarkers = []string{
	"no required module provides package",
	"no required module",
	"no required modul", // clipped "module"
	"no required mod",   // clipped "module"
	"missing Go module",
	"missing module",
	"finding module for package",
	"cannot find module",
	"cannot find module providing package",
	"cannot find package",
	"missing go.sum entry",
	"to add it: go get",
	"go: go.mod",
	"go mod",
	"module provides package",
	"could not import",
	"package ",
}

// buildHaltedMarkers are free-text fragments that identify a halted build or
// compilation failure.
var buildHaltedMarkers = []string{
	"[build failed]",
	"build failed",
	"build error",
	"compilation failed",
	"compile error",
	"command-line-arguments",
	"syntax error",
	"compilation error",
	"expected declaration",
	"non-declaration statement outside function body",
	"expected ';'",
	"not enough arguments",
	"too many errors",
	"exit status",
	"gcc:",
	"cc1:",
	"ld:",
}

// executionFailedMarkers are free-text fragments that identify a runtime/test
// execution failure (as opposed to a compile-time halt).
var executionFailedMarkers = []string{
	"failed tests:",
	"stacktrace",
	"stack trace",
	"panic:",
	"nil pointer dereference",
	"index out of range",
	"divide by zero",
	"fatal error",
	"error:",
	"rootless docker",
	"container runtime",
}

// parseErrorIndicators are secondary signals that, when paired with a *.go
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

// Detect scans raw compiler / test / investigation-ledger output and returns
// the set of canonical system signals it contains. Free-text matching is
// confined to this function: routing code evaluates the returned Signal kinds.
//
// The result is a set, not an exclusive classification: the same content may
// carry multiple signals (e.g. a build that fails on both a missing module and
// an undefined symbol). At most one Signal is emitted per kind. Content is
// matched case-insensitively; the blocker token and canonical-mismatch paths
// extract structured payload keys (blocker, dependency, new_path, old_path).
func Detect(content, source string) []Signal {
	if content == "" {
		return nil
	}
	out := make([]Signal, 0, 4)

	// ── Canonical import path mismatch ─────────────────────────
	if m := canonicalMismatchRE.FindStringSubmatch(content); m != nil {
		payload := map[string]string{"new_path": cleanPath(m[1])}
		if len(m) > 2 && m[2] != "" {
			payload["old_path"] = cleanPath(m[2])
		}
		out = append(out, New(SignalImportMismatch, source, payload))
	}

	// ── Undefined symbol (strict compiler form) ────────────────
	if m := undefinedSymbolRE.FindStringSubmatch(content); m != nil {
		out = append(out, New(SignalSymbolUndefined, source, map[string]string{
			"file":   m[1],
			"line":   m[2],
			"symbol": m[4],
		}))
	}

	// ── Remote dependency blocker token (investigate handoff) ──
	if m := blockerTokenRE.FindStringSubmatch(content); m != nil {
		payload := map[string]string{"blocker": "true"}
		if dep := extractDependencyToken(m[1]); dep != "" {
			payload["dependency"] = dep
		}
		out = append(out, New(SignalDepMissing, source, payload))
	}

	lower := strings.ToLower(content)

	// ── Missing dependency / module ────────────────────────────
	if hasAny(lower, depMissingMarkers) || goDependencyCoordinateRE.MatchString(lower) {
		out = append(out, New(SignalDepMissing, source, nil))
	}

	// ── Build halted ───────────────────────────────────────────
	if hasAny(lower, buildHaltedMarkers) {
		out = append(out, New(SignalBuildHalted, source, nil))
	}

	// ── Execution failed ───────────────────────────────────────
	if hasAny(lower, executionFailedMarkers) {
		out = append(out, New(SignalExecutionFailed, source, nil))
	}

	// ── *.go coordinate paired with an import/parse indicator ──
	for _, line := range strings.Split(lower, "\n") {
		line = strings.TrimSpace(line)
		if goCoordinateRE.MatchString(line) && hasAny(line, parseErrorIndicators) {
			out = append(out, New(SignalDepMissing, source, nil))
			break
		}
	}

	return dedupe(out)
}

// IsCompilationOrDependency reports whether any detected signal is a
// compilation, dependency or environment failure that module/setup tooling can
// resolve. It is the signal-level equivalent of the legacy
// IsCompilationOrDependencyError detector.
func IsCompilationOrDependency(signals []Signal) bool {
	return HasCompileFailure(signals) || HasKind(signals, SignalExecutionFailed)
}

// HasCompileFailure reports whether any detected signal indicates a compile or
// structural error (as opposed to a runtime/execution failure).
func HasCompileFailure(signals []Signal) bool {
	for i := range signals {
		switch signals[i].Kind {
		case SignalDepMissing, SignalSymbolUndefined, SignalImportMismatch, SignalBuildHalted:
			return true
		}
	}
	return false
}

// dedupe collapses the signal set to at most one signal per kind, preserving
// first-occurrence order (the blocker-token signal, which carries payload,
// wins over a bare dep-missing signal for SignalDepMissing).
func dedupe(signals []Signal) []Signal {
	seen := make(map[SignalKind]bool, len(signals))
	out := signals[:0]
	for i := range signals {
		if seen[signals[i].Kind] {
			continue
		}
		seen[signals[i].Kind] = true
		out = append(out, signals[i])
	}
	return out
}

// hasAny reports whether s contains any of the given substrings.
func hasAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// cleanPath strips surrounding quotes from a matched module path.
func cleanPath(s string) string {
	return strings.Trim(s, `"'`)
}

// extractDependencyToken isolates the dependency package from the tail of a
// remote-dependency-blocker token. It unwraps a markdown link ([pkg](url)) and
// falls back to the first non-placeholder token.
func extractDependencyToken(tail string) string {
	tail = strings.TrimSpace(tail)
	if tail == "" {
		return ""
	}
	if open := strings.Index(tail, "["); open >= 0 {
		rest := tail[open+1:]
		if closeIdx := strings.Index(rest, "]"); closeIdx >= 0 {
			if text := strings.TrimSpace(rest[:closeIdx]); text != "" {
				// Preserve visually-clipped fragments (e.g. "g...") verbatim so
				// consumers can recognize them as non-resolvable.
				return text
			}
		}
	}
	for _, tok := range strings.Fields(tail) {
		t := strings.Trim(tok, `"'(),:;.`)
		if t != "" && !strings.Contains(t, "<") {
			return t
		}
	}
	return ""
}

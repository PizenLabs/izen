// Package signal defines the canonical, strongly-typed system signals of the
// Izen control plane.
//
// The package answers exactly one question: "What machine/system signal
// occurred?" It performs no policy evaluation, capability checking, or decision
// routing. Consumers (plan, investigate, build, ui) evaluate the typed
// SignalKind values and decide what to do about them; they never re-scan raw
// compiler/test output for routing purposes.
//
// Signal is a pure value type and Detect (detect.go) is a self-contained
// classifier built on the standard library: internal/domain has zero
// dependencies on internal sub-packages, and dependency flows strictly DOWN
// (events / adapters -> domain).
package signal

import "time"

// SignalKind discriminates the canonical machine/system signals produced by
// the Izen control plane. These replace the legacy free-text tokens and
// substring signatures (e.g. "REMOTE DEPENDENCY BLOCKER", "undefined:") that
// previously drove routing between investigate, plan and build.
type SignalKind string

const (
	// SignalDepMissing indicates a missing dependency: a Go module/package
	// that the build could not resolve (e.g. "no required module provides
	// package X", "cannot find module providing package X", or the remote
	// dependency blocker recorded by investigate).
	SignalDepMissing SignalKind = "dep.missing"
	// SignalSymbolUndefined indicates an undefined symbol / identifier in the
	// code (e.g. "file.go:line:col: undefined: Symbol"), including standard
	// library case-sensitivity mismatches.
	SignalSymbolUndefined SignalKind = "symbol.undefined"
	// SignalImportMismatch indicates a canonical import path conflict where
	// go.mod declares a module path that differs from the path used in source
	// files ("module declares its path as: X but was required as: Y").
	SignalImportMismatch SignalKind = "import.mismatch"
	// SignalExecutionFailed indicates a runtime/test execution failure (a test
	// suite failing, a panic, a stacktrace) as opposed to a compile-time halt.
	SignalExecutionFailed SignalKind = "execution.failed"
	// SignalBuildHalted indicates that the build/compile halted with a
	// structural failure (syntax error, compile error, build failure).
	SignalBuildHalted SignalKind = "build.halted"
)

// String returns the canonical discriminator.
func (k SignalKind) String() string { return string(k) }

// Signal is a canonical, strongly-typed system signal. It is a value: it
// carries the fact that a machine/system condition occurred, nothing more.
type Signal struct {
	Kind      SignalKind        `json:"kind"`
	Payload   map[string]string `json:"payload,omitempty"`
	Source    string            `json:"source"`
	Timestamp time.Time         `json:"timestamp"`
}

// New constructs a Signal with a UTC timestamp.
func New(kind SignalKind, source string, payload map[string]string) Signal {
	return Signal{
		Kind:      kind,
		Payload:   payload,
		Source:    source,
		Timestamp: time.Now().UTC(),
	}
}

// PayloadValue returns the value for a payload key, or "" when the signal has
// no payload or the key is absent.
func (s Signal) PayloadValue(key string) string {
	if s.Payload == nil {
		return ""
	}
	return s.Payload[key]
}

// HasKind reports whether the signal set contains at least one signal of the
// given kind.
func HasKind(signals []Signal, kind SignalKind) bool {
	for i := range signals {
		if signals[i].Kind == kind {
			return true
		}
	}
	return false
}

// First returns the first signal of the given kind, or nil when none is
// present.
func First(signals []Signal, kind SignalKind) *Signal {
	for i := range signals {
		if signals[i].Kind == kind {
			return &signals[i]
		}
	}
	return nil
}

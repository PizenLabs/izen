// Package planner implements the Boundary-2 expansion: the SUB-TASK
// DECOMPOSER & DAG PLANNER. When the preflight guard (I5) refuses a
// full-file rewrite because EstimatedTokens exceeds max_output, this package
// partitions the target into individually feasible sub-tasks staged as a
// validated ExecutionDAG.
//
// Decomposition is DETERMINISTIC and fail-closed:
//
//   - Structural (AST-like) splitting for Go/Rust/TS sources: sections are
//     cut at top-level declaration boundaries.
//
//   - Block splitting for HTML/Markdown/Config formats: sections are cut at
//     top-level block boundaries (tags, headings, section headers).
//
//   - Every SubTask obeys the strict budget rule
//
//     SubTask.EstimatedTokens <= max_output × SubTaskBudgetFactor (0.7)
//
//     which is STRICTLY inside the Boundary-2 ceiling, so every generated
//     sub-task passes EvaluatePreflight individually by construction.
//
//   - A target whose single indivisible section still exceeds the sub-task
//     budget yields NO DAG (ErrNotDecomposable) — never an oversized plan.
//
// The package is pure planning: it never reads the workspace, never invokes a
// provider, and never mutates anything. The autonomy driver owns execution of
// the staged DAG inside an atomic transaction loop.
package planner

import (
	"bytes"
	"errors"
	"fmt"
)

// SplitKind names the structural strategy used to partition one target.
type SplitKind string

const (
	// SplitAST is structural declaration-boundary splitting (Go/Rust/TS).
	SplitAST SplitKind = "ast_structural"
	// SplitBlock is top-level block splitting (HTML/MD/Config).
	SplitBlock SplitKind = "block"
)

// String returns the canonical label of the split kind.
func (k SplitKind) String() string { return string(k) }

// Region is an inclusive 1-indexed line window [StartLine, EndLine] over the
// original artifact. Regions are contiguous and non-overlapping by
// construction; the union of all sub-task regions covers the whole source.
type Region struct {
	StartLine int // 1-indexed inclusive
	EndLine   int // 1-indexed inclusive
}

// Lines returns the number of lines covered by the region.
func (r Region) Lines() int {
	if r.EndLine < r.StartLine {
		return 0
	}
	return r.EndLine - r.StartLine + 1
}

// String renders the human-readable window form ("line 7" / "lines 3–9").
func (r Region) String() string {
	if r.StartLine == r.EndLine {
		return fmt.Sprintf("line %d", r.StartLine)
	}
	return fmt.Sprintf("lines %d–%d", r.StartLine, r.EndLine)
}

// SliceLines returns the bytes of the region's line window from source,
// newline-terminated per line. It is the single authority for turning a
// Region back into content.
func SliceLines(source []byte, r Region) []byte {
	lines := splitKeepNewline(source)
	if len(lines) == 0 {
		return nil
	}
	if r.StartLine < 1 {
		r.StartLine = 1
	}
	if r.EndLine > len(lines) {
		r.EndLine = len(lines)
	}
	if r.StartLine > r.EndLine {
		return nil
	}
	var out bytes.Buffer
	for i := r.StartLine - 1; i < r.EndLine; i++ {
		out.Write(lines[i])
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// LineCount returns the number of lines in source (no phantom final line).
func LineCount(source []byte) int {
	return len(splitKeepNewline(source))
}

// Section is one candidate decomposition unit produced by a Decomposer: a
// contiguous line window plus its bounded human-readable identity.
type Section struct {
	Region Region
	Label  string // e.g. `func (s *Server) Start`, "<section id=\"nav\">", "# Usage"
}

// Decomposer partitions one class of artifacts into candidate sections at
// their natural structural boundaries. Implementations must be deterministic:
// identical inputs always yield identical sections.
type Decomposer interface {
	// Supports reports whether this decomposer handles the named target.
	Supports(target string) bool
	// Split partitions non-empty source content into ordered sections whose
	// union covers the whole input. An indivisible artifact returns exactly
	// one section covering everything — the caller decides feasibility.
	Split(target string, source []byte) ([]Section, error)
}

// Package-level planning errors. They are fail-closed sentinels: a caller
// that receives any of them MUST NOT proceed with a partial plan.
var (
	// ErrNoDecomposer: no registered decomposer handles the target's format.
	ErrNoDecomposer = errors.New("planner: no decomposer registered for target")
	// ErrNotDecomposable: a single indivisible section already exceeds the
	// per-sub-task budget, so no valid DAG can exist for this budget.
	ErrNotDecomposable = errors.New("planner: indivisible section exceeds the sub-task budget")
	// ErrInvalidDAG: the staged DAG failed validation (budget, dependency or
	// ordering invariant). Never execute an invalid DAG.
	ErrInvalidDAG = errors.New("planner: invalid execution dag")
	// ErrEmptySource: decomposition was requested for empty content.
	ErrEmptySource = errors.New("planner: empty source content")
)

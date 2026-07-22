package retrieval

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/internal/lynx"
)

// Canonical mismatch pattern from Go compiler:
//
//	module declares its path as: <NEW>
//	but was required as: <OLD>
//
// May appear as two separate lines or combined.
var canonicalMismatchRe = regexp.MustCompile(
	`module declares its path as:\s*(\S+)\s*` +
		`(?:but was required as:\s*(\S+)|$)`)

// canonicalMismatchSingleRe matches the condensed single-line form:
//
//	module declares its path as: "example.com/new" but was required as: "example.com/old"
var canonicalMismatchSingleRe = regexp.MustCompile(
	`module declares its path as:\s*["']?([^\s"']+)["']?\s*` +
		`but was required as:\s*["']?([^\s"']+)["']?`)

// CanonicalMismatch holds the parsed result of a Go canonical import path error.
type CanonicalMismatch struct {
	NewPath string // the correct module path (module declares its path as: <NEW>)
	OldPath string // the incorrect path used in imports (but was required as: <OLD>)
	Line    int    // source line number if available from error coordinate
	File    string // source file path if available from error coordinate
	Raw     string // the raw error line(s)
}

// String returns a compact representation suitable for LLM context injection.
func (m *CanonicalMismatch) String() string {
	if m.File != "" && m.Line > 0 {
		return fmt.Sprintf("%s:%d: module path mismatch: replace %q with %q",
			m.File, m.Line, m.OldPath, m.NewPath)
	}
	return fmt.Sprintf("module path mismatch: replace %q with %q", m.OldPath, m.NewPath)
}

// cleanPath strips surrounding quotes from a matched module path.
func cleanPath(s string) string {
	return strings.Trim(s, `"'`)
}

// ParseCanonicalMismatch scans raw compiler output for a canonical import path
// mismatch error. Returns nil if no mismatch is found.
//
// The Go compiler emits this error when go.mod declares a module path that
// differs from the import path used in source files:
//
//	module declares its path as: "example.com/new"
//	but was required as: "example.com/old"
//
// Or as two sequential lines in compiler output.
func ParseCanonicalMismatch(output string) *CanonicalMismatch {
	if output == "" {
		return nil
	}

	lines := strings.Split(output, "\n")
	var combined strings.Builder

	// First pass: try single-line condensed form.
	for _, line := range lines {
		if m := canonicalMismatchSingleRe.FindStringSubmatch(line); m != nil {
			result := &CanonicalMismatch{
				NewPath: cleanPath(m[1]),
				OldPath: cleanPath(m[2]),
				Raw:     line,
			}
			// Extract file:line coordinate from the same line if present.
			if file, lineNum, ok := extractErrorCoordinate(line); ok {
				result.File = file
				result.Line = lineNum
			}
			return result
		}
	}

	// Second pass: try two-line form.
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.Contains(trimmed, "module declares its path as") {
			continue
		}
		combined.WriteString(trimmed)
		combined.WriteString(" ")
		if i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if strings.Contains(next, "but was required as") {
				combined.WriteString(next)
			}
		}
		if m := canonicalMismatchRe.FindStringSubmatch(combined.String()); m != nil {
			result := &CanonicalMismatch{
				NewPath: cleanPath(m[1]),
				Raw:     combined.String(),
			}
			if len(m) > 2 {
				result.OldPath = cleanPath(m[2])
			}

			// If OldPath is still empty, derive it from the but was required fragment.
			// The multi-line regex may not have captured it if newlines broke it.
			if result.OldPath == "" {
				for j := 0; j < len(lines); j++ {
					if j == i || j == i+1 {
						continue
					}
					if strings.Contains(lines[j], "but was required as") {
						if m2 := canonicalMismatchSingleRe.FindStringSubmatch(lines[j]); m2 != nil {
							result.NewPath = cleanPath(m2[1])
							result.OldPath = cleanPath(m2[2])
						}
					}
				}
			}

			if result.OldPath == "" {
				idx := strings.Index(combined.String(), "but was required as:")
				if idx >= 0 {
					rest := combined.String()[idx+len("but was required as:"):]
					rest = strings.TrimSpace(rest)
					rest = strings.Trim(rest, `"'`)
					if rest != "" {
						result.OldPath = strings.Fields(rest)[0]
					}
				}
			}

			// Check the preceding lines for a file:line error coordinate.
			for j := i - 1; j >= 0 && j >= i-3; j-- {
				if file, line, ok := extractErrorCoordinate(lines[j]); ok {
					result.File = file
					result.Line = line
					break
				}
			}

			// If no preceding coordinate, check the same line before the mismatch text.
			if result.File == "" {
				before, _, _ := strings.Cut(trimmed, "module declares its path as")
				if file, line, ok := extractErrorCoordinate(before); ok {
					result.File = file
					result.Line = line
				}
			}

			return result
		}
	}

	return nil
}

// extractErrorCoordinate extracts file:line from a Go compiler error line.
// Matches patterns like:
//
//	cmd/api/main.go:7:2: text
//	cmd/api/main.go:7: text
//
// The match is NOT anchored to the start of string, so it can find coordinates
// within a longer line (e.g., before the canonical mismatch marker).
func extractErrorCoordinate(line string) (string, int, bool) {
	re := regexp.MustCompile(`([^:\s]+\.go):(\d+)(?::(\d+))?:`)
	m := re.FindStringSubmatch(line)
	if m == nil {
		return "", 0, false
	}
	var lineNum int
	lineNum, _ = strconv.Atoi(m[2])
	return m[1], lineNum, true
}

// ── LX Coordinate Resolver ──────────────────────────────────────────────────

// LXCoordinateRef holds a single coordinate reference resolved by lx.
type LXCoordinateRef struct {
	File       string
	StartLine  int
	EndLine    int
	SymbolName string
	Content    string
}

// LXCoordinateResolver uses the lynx daemon to resolve canonical import path
// mismatches to exact file:line coordinates without loading full files.
type LXCoordinateResolver struct {
	ctrl *lynx.Controller
}

// NewLXCoordinateResolver creates a resolver backed by the global lynx controller.
// Returns nil if the controller is not available.
func NewLXCoordinateResolver() *LXCoordinateResolver {
	ctrl := GetLynxController()
	if ctrl == nil {
		return nil
	}
	return &LXCoordinateResolver{ctrl: ctrl}
}

// NewLXCoordinateResolverFromController creates a resolver from a specific controller.
func NewLXCoordinateResolverFromController(ctrl *lynx.Controller) *LXCoordinateResolver {
	return &LXCoordinateResolver{ctrl: ctrl}
}

// ResolveCanonicalMismatch resolves a canonical import mismatch to exact file
// coordinates by searching for all references to the old (incorrect) module path
// in the workspace. Returns a list of coordinate refs — each representing a
// single location where the old path needs to be replaced.
//
// Token budget: < 100 tokens for the full result set (coordinates only, no
// full-file content).
func (r *LXCoordinateResolver) ResolveCanonicalMismatch(mismatch *CanonicalMismatch) ([]LXCoordinateRef, error) {
	if r == nil || mismatch == nil {
		return nil, nil
	}

	if err := r.ctrl.EnsureStarted(); err != nil {
		return nil, fmt.Errorf("lx ensure started: %w", err)
	}

	// Strategy 1: If we have a file:line coordinate, use lx related to find
	// the exact AST block at that position.
	var refs []LXCoordinateRef
	if mismatch.File != "" && mismatch.Line > 0 {
		related, err := r.ctrl.FindRelatedRaw(mismatch.File, mismatch.Line)
		if err == nil && len(related) > 0 {
			for _, res := range related {
				if res.FilePath == "" {
					continue
				}
				refs = append(refs, LXCoordinateRef{
					File:       res.FilePath,
					StartLine:  res.StartLine,
					EndLine:    res.EndLine,
					SymbolName: res.SymbolName,
					Content:    res.Content,
				})
			}
		}

		// Also try to resolve the exact symbol at the error coordinate.
		resolved, err := r.ctrl.ResolveSymbolRaw(mismatch.OldPath)
		if err == nil && len(resolved) > 0 {
			for _, res := range resolved {
				if res.FilePath == "" {
					continue
				}
				// Deduplicate by file+startLine.
				dup := false
				for _, existing := range refs {
					if existing.File == res.FilePath && existing.StartLine == res.StartLine {
						dup = true
						break
					}
				}
				if !dup {
					refs = append(refs, LXCoordinateRef{
						File:       res.FilePath,
						StartLine:  res.StartLine,
						EndLine:    res.EndLine,
						SymbolName: res.SymbolName,
						Content:    res.Content,
					})
				}
			}
		}
	}

	// Strategy 2: Use canonical path components to search for import statements.
	// Extract the last path segment of the old module path as a search token.
	if len(refs) == 0 {
		searchToken := lastPathSegment(mismatch.OldPath)
		if searchToken != "" {
			results, err := r.ctrl.SearchRaw(searchToken)
			if err == nil && len(results) > 0 {
				for _, res := range results {
					if res.FilePath == "" {
						continue
					}
					refs = append(refs, LXCoordinateRef{
						File:       res.FilePath,
						StartLine:  res.StartLine,
						EndLine:    res.EndLine,
						SymbolName: res.SymbolName,
						Content:    res.Content,
					})
				}
			}
		}
	}

	// Strategy 3: If we still have no refs but have the old path, resolve
	// the full old path as a symbol.
	if len(refs) == 0 {
		resolved, err := r.ctrl.ResolveSymbolRaw(mismatch.OldPath)
		if err == nil && len(resolved) > 0 {
			for _, res := range resolved {
				if res.FilePath == "" {
					continue
				}
				refs = append(refs, LXCoordinateRef{
					File:       res.FilePath,
					StartLine:  res.StartLine,
					EndLine:    res.EndLine,
					SymbolName: res.SymbolName,
					Content:    res.Content,
				})
			}
		}
	}

	return refs, nil
}

// FormatAsLedgerPayload produces a compact (<100 token) context snippet from
// the resolved coordinates, suitable for direct injection into the LLM prompt
// or forensic ledger.
func FormatCanonicalFixLedger(mismatch *CanonicalMismatch, refs []LXCoordinateRef) string {
	var b strings.Builder
	b.WriteString("## CANONICAL IMPORT MISMATCH (lx resolved)\n")
	fmt.Fprintf(&b, "Fix: replace %q → %q\n", mismatch.OldPath, mismatch.NewPath)

	if len(refs) == 0 {
		b.WriteString("Locations: (unresolved — falling back to shell resolution)\n")
		return b.String()
	}

	b.WriteString("Locations:\n")
	for _, ref := range refs {
		if ref.EndLine > 0 && ref.EndLine != ref.StartLine {
			fmt.Fprintf(&b, "  %s:%d-%d", ref.File, ref.StartLine, ref.EndLine)
		} else {
			fmt.Fprintf(&b, "  %s:%d", ref.File, ref.StartLine)
		}
		if ref.SymbolName != "" {
			fmt.Fprintf(&b, " (%s)", ref.SymbolName)
		}
		b.WriteString("\n")
	}

	b.WriteString("Strategy: FILE_EDIT at coordinates above + SHELL_EXEC go mod tidy\n")
	return b.String()
}

// ── Undefined Symbol Resolution ──────────────────────────────────────────────

// undefinedSymbolRe matches Go compiler "undefined: Symbol" errors with
// optional build-prefix line (e.g. "# go-template/cmd/api"). Uses named
// groups for clarity and (?m) for multi-line matching.
//
// Patterns matched:
//
//	cmd/api/main.go:24:2: undefined: Log
//	# go-template/cmd/api\ncmd/api/main.go:24:2: undefined: Log
//	cmd/api/main.go:24: undefined: Log
var undefinedSymbolRe = regexp.MustCompile(
	`(?m)^\s*(?:#\s*\S+\s+)?(?P<file>[^:\s]+):(?P<line>\d+)(?::(?P<col>\d+))?:\s*undefined:\s*(?P<symbol>[A-Za-z0-9_]+)`)

// UndefinedSymbol holds the parsed result of a Go undefined symbol error.
type UndefinedSymbol struct {
	File   string // source file (e.g., cmd/api/main.go)
	Line   int    // source line number
	Symbol string // the undefined symbol name (e.g., Log)
	Raw    string // the raw error line
}

// ParseUndefinedSymbol scans output for the first undefined symbol error.
// Returns nil if none found. Uses a multi-line regex that handles optional
// build-prefix lines (e.g. "# go-template/cmd/api") before the error line.
//
// Formats matched:
//
//	cmd/api/main.go:24:2: undefined: Log
//	# go-template/cmd/api\ncmd/api/main.go:24:2: undefined: Log
//	cmd/api/main.go:24: undefined: Log
func ParseUndefinedSymbol(output string) *UndefinedSymbol {
	if output == "" {
		return nil
	}
	m := undefinedSymbolRe.FindStringSubmatch(output)
	if m == nil {
		return nil
	}
	fileIdx := undefinedSymbolRe.SubexpIndex("file")
	lineIdx := undefinedSymbolRe.SubexpIndex("line")
	symIdx := undefinedSymbolRe.SubexpIndex("symbol")
	lineNum, _ := strconv.Atoi(m[lineIdx])
	return &UndefinedSymbol{
		File:   m[fileIdx],
		Line:   lineNum,
		Symbol: m[symIdx],
		Raw:    m[0],
	}
}

// HasUndefinedSymbol reports whether output contains an undefined symbol error.
func HasUndefinedSymbol(output string) bool {
	return ParseUndefinedSymbol(output) != nil
}

// ── Standard Library Case-Sensitivity Correction ─────────────────────────────

// stdlibPackageNames maps a package name (lowercase) to its full import path
// for Go standard library packages commonly confused due to case sensitivity.
// E.g., "Log" → lowercase first letter → "log" → import "log".
var stdlibPackageNames = map[string]string{
	"log":      "log",
	"fmt":      "fmt",
	"os":       "os",
	"time":     "time",
	"io":       "io",
	"http":     "net/http",
	"json":     "encoding/json",
	"xml":      "encoding/xml",
	"csv":      "encoding/csv",
	"strings":  "strings",
	"strconv":  "strconv",
	"bytes":    "bytes",
	"bufio":    "bufio",
	"errors":   "errors",
	"context":  "context",
	"sort":     "sort",
	"sync":     "sync",
	"math":     "math",
	"flag":     "flag",
	"regexp":   "regexp",
	"net":      "net",
	"path":     "path",
	"filepath": "path/filepath",
	"url":      "net/url",
	"slog":     "log/slog",
	"sql":      "database/sql",
	"testing":  "testing",
	"reflect":  "reflect",
	"html":     "html",
	"mime":     "mime",
	"image":    "image",
	"os/exec":  "os/exec",
}

// CheckStdlibCaseCorrection checks whether an undefined symbol can be fixed by
// lowering its first letter to match a Go standard library package. Returns the
// corrected package name (e.g., "log") and its full import path (e.g., "log").
// Returns matched=false if no standard library package matches.
func CheckStdlibCaseCorrection(symbol string) (pkgName string, importPath string, matched bool) {
	if symbol == "" {
		return "", "", false
	}
	// Lower the first letter of the symbol.
	runes := []rune(symbol)
	lowered := strings.ToLower(string(runes[0])) + string(runes[1:])
	path, ok := stdlibPackageNames[lowered]
	if !ok {
		return "", "", false
	}
	return lowered, path, true
}

// FormatStdlibFixLedger produces a compact (<100 token) context snippet for
// a standard library case-sensitivity fix.
func FormatStdlibFixLedger(undef *UndefinedSymbol, pkgName, importPath string) string {
	var b strings.Builder
	b.WriteString("## UNDEFINED SYMBOL (stdlib case correction)\n")
	fmt.Fprintf(&b, "Symbol: %s at %s:%d\n", undef.Symbol, undef.File, undef.Line)
	fmt.Fprintf(&b, "Fix: replace %q with %q. and add import %q\n", undef.Symbol, pkgName, importPath)
	b.WriteString("Strategy: FILE_EDIT (case correction + import) + SHELL_EXEC go test ./...\n")
	return b.String()
}

// ResolveUndefinedSymbol resolves an undefined symbol error to exact file
// coordinates by using lx resolve and lx related. Returns coordinates that
// describe both the error site and the symbol definition (if found).
func (r *LXCoordinateResolver) ResolveUndefinedSymbol(undef *UndefinedSymbol) ([]LXCoordinateRef, error) {
	if r == nil || undef == nil {
		return nil, nil
	}

	if err := r.ctrl.EnsureStarted(); err != nil {
		return nil, fmt.Errorf("lx ensure started: %w", err)
	}

	var refs []LXCoordinateRef

	// Strategy 1: Resolve the undefined symbol to find its definition.
	resolved, err := r.ctrl.ResolveSymbolRaw(undef.Symbol)
	if err == nil && len(resolved) > 0 {
		for _, res := range resolved {
			if res.FilePath == "" {
				continue
			}
			refs = append(refs, LXCoordinateRef{
				File:       res.FilePath,
				StartLine:  res.StartLine,
				EndLine:    res.EndLine,
				SymbolName: res.SymbolName,
				Content:    res.Content,
			})
		}
	}

	// Strategy 2: Use lx related at the error coordinate for context.
	if undef.File != "" && undef.Line > 0 {
		related, err := r.ctrl.FindRelatedRaw(undef.File, undef.Line)
		if err == nil && len(related) > 0 {
			for _, res := range related {
				if res.FilePath == "" {
					continue
				}
				dup := false
				for _, existing := range refs {
					if existing.File == res.FilePath && existing.StartLine == res.StartLine {
						dup = true
						break
					}
				}
				if !dup {
					refs = append(refs, LXCoordinateRef{
						File:       res.FilePath,
						StartLine:  res.StartLine,
						EndLine:    res.EndLine,
						SymbolName: res.SymbolName,
						Content:    res.Content,
					})
				}
			}
		}
	}

	return refs, nil
}

// FormatUndefinedFixLedger produces a compact (<100 token) context snippet
// from the resolved undefined symbol coordinates.
func FormatUndefinedFixLedger(undef *UndefinedSymbol, refs []LXCoordinateRef) string {
	var b strings.Builder
	b.WriteString("## UNDEFINED SYMBOL (lx resolved)\n")
	fmt.Fprintf(&b, "Symbol: %s at %s:%d\n", undef.Symbol, undef.File, undef.Line)

	if len(refs) == 0 {
		b.WriteString("Definition: (not found in workspace — may be standard library or missing import)\n")
		return b.String()
	}

	b.WriteString("Definition locations:\n")
	for _, ref := range refs {
		if ref.EndLine > 0 && ref.EndLine != ref.StartLine {
			fmt.Fprintf(&b, "  %s:%d-%d", ref.File, ref.StartLine, ref.EndLine)
		} else {
			fmt.Fprintf(&b, "  %s:%d", ref.File, ref.StartLine)
		}
		if ref.SymbolName != "" {
			fmt.Fprintf(&b, " (%s)", ref.SymbolName)
		}
		b.WriteString("\n")
	}

	b.WriteString("Strategy: FILE_EDIT at error coordinate + SHELL_EXEC go test ./...\n")
	return b.String()
}

// lastPathSegment returns the last component of a module/package path.
func lastPathSegment(path string) string {
	path = strings.Trim(path, `"'`)
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// HasCanonicalMismatch reports whether the content contains a canonical import
// path mismatch error signal.
func HasCanonicalMismatch(content string) bool {
	return ParseCanonicalMismatch(content) != nil
}

// compilerTargetSuffixRe strips :line:col suffixes from compiler error file paths.
// Matches "path/to/file.go:123:456", "path/to/file.go:123", or bare "path/to/file.go".
// Captures the clean path and the optional line number.
var compilerTargetSuffixRe = regexp.MustCompile(`^([^:]+\.\w+)(?::(\d+))?(?::(\d+))?$`)

// SplitTargetPath separates a target extracted from compiler output (e.g.
// "cmd/api/main.go:24:2" or "cmd/api/main.go:24") into the clean file path
// and the optional line number. Returns line=0 if no line number is present.
func SplitTargetPath(raw string) (path string, line int) {
	if raw == "" {
		return "", 0
	}
	m := compilerTargetSuffixRe.FindStringSubmatch(raw)
	if m == nil {
		return raw, 0
	}
	path = m[1]
	if len(m) > 2 && m[2] != "" {
		line, _ = strconv.Atoi(m[2])
	}
	return path, line
}

// SanitizeTargetPath strips trailing :line:col suffixes from a compiler error
// file path and verifies the file exists on disk. Uses SplitTargetPath for suffix
// stripping, then validates existence. Returns the clean relative path or an
// error if the file does not exist or the path is empty.
func SanitizeTargetPath(raw string) (string, error) {
	path, _ := SplitTargetPath(raw)
	if path == "" {
		return "", fmt.Errorf("empty target path")
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("target path %q does not exist: %w", path, err)
	}
	return path, nil
}

// addGoImport inserts an import statement for importPath into Go source content.
// It handles three cases:
//  1. Existing import block: import (\n...\n) — inserts inside the block
//  2. Existing single import: import "..." — converts to block and adds
//  3. No existing imports — adds import block after package declaration
func addGoImport(content, importPath string) string {
	quoted := `"` + importPath + `"`

	// Already imported — no-op.
	if strings.Contains(content, quoted) {
		return content
	}

	importLine := "\t" + quoted

	// Case 1: existing import block: import (\n ... \n)
	blockRe := regexp.MustCompile(`(?m)^import\s+\((\s*\n)`)
	if m := blockRe.FindStringSubmatchIndex(content); m != nil {
		insertAt := m[3] // end of "import (\n" prefix — right after the newline
		return content[:insertAt] + importLine + "\n" + content[insertAt:]
	}

	// Case 2: existing single import: import "..." — convert to block.
	singleRe := regexp.MustCompile(`(?m)^import\s+"([^"]+)"\s*$`)
	if m := singleRe.FindStringSubmatchIndex(content); m != nil {
		existingPath := content[m[2]:m[3]]
		indent := "\t"
		block := "import (\n" + indent + `"` + existingPath + `"` + "\n" + importLine + "\n)"
		return content[:m[0]] + block + content[m[1]:]
	}

	// Case 3: no existing imports — add after package declaration.
	pkgRe := regexp.MustCompile(`(?m)^package\s+\w+`)
	if m := pkgRe.FindStringIndex(content); m != nil {
		insertAt := m[1]
		return content[:insertAt] + "\n\nimport (\n" + importLine + "\n)" + content[insertAt:]
	}

	return content
}

// ApplyStdlibCaseFix reads the file at filePath, replaces all package-qualified
// uses of symbol with pkgName (e.g., "Log." -> "log."), and adds an import for
// importPath if not already present. Returns the original and modified content,
// or an error if the file cannot be read.
func ApplyStdlibCaseFix(filePath, symbol, pkgName, importPath string) (original, modified string, err error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", filePath, err)
	}
	original = string(data)

	// Replace "Symbol." with "pkgName." (package-qualified usage).
	modified = strings.ReplaceAll(original, symbol+".", pkgName+".")

	// Also replace "Symbol(" (function call without qualifier is unlikely but defensive).
	modified = strings.ReplaceAll(modified, symbol+"(", pkgName+"(")

	// Add import if not present.
	modified = addGoImport(modified, importPath)

	return original, modified, nil
}

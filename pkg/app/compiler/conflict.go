package compiler

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WorkspaceState summarises what the compiler can observe about the
// workspace before planning. It distinguishes application-level target types
// (todo_app, portfolio, ...) from technical archetype markers (vanilla web,
// Go, React) so the conflict logic reasons about intent-relevant types only.
type WorkspaceState struct {
	// Root is the workspace directory the state was derived from.
	Root string
	// Empty reports whether the workspace holds no recognisable content.
	Empty bool
	// FileCount is the number of files scanned.
	FileCount int
	// AppTypes maps every application-level target type detected on disk.
	AppTypes map[string]bool
	// Archetypes maps every technical marker detected on disk.
	Archetypes map[string]bool
	// Markers are the human-readable markers that fired, in discovery order.
	Markers []string
}

// Conflict records a detected mismatch between the resolved intent and the
// existing workspace state.
type Conflict struct {
	// Present reports whether a high-impact mismatch exists.
	Present bool
	// Reason is the machine-readable trigger.
	Reason string
	// Requested is the target type the prompt asks for.
	Requested string
	// Detected are the application-level target types found on disk.
	Detected []string
}

// ConflictDetector scans a workspace for target-type markers and compares
// them against the resolved intent.
type ConflictDetector struct {
	contentCap int
}

// NewConflictDetector builds a ConflictDetector that reads file contents up
// to a 256 KiB cap per file when scanning for markers.
func NewConflictDetector() *ConflictDetector {
	return &ConflictDetector{contentCap: 256 * 1024}
}

// Detect scans root and returns the observable workspace state. A missing or
// empty directory yields an empty state, never an error.
func (c *ConflictDetector) Detect(root string) WorkspaceState {
	ws := WorkspaceState{
		Root:       root,
		AppTypes:   make(map[string]bool),
		Archetypes: make(map[string]bool),
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		ws.Empty = true
		return ws
	}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (name == ".git" || name == "node_modules" || name == "vendor" || name == ".izen" || name == ".codebase-memory") {
				return filepath.SkipDir
			}
			return nil
		}
		ws.FileCount++
		c.scanFile(path, name, &ws)
		return nil
	}); err != nil {
		ws.Empty = true
		return ws
	}
	ws.Empty = ws.FileCount == 0
	return ws
}

// scanFile examines one workspace file and folds its markers into ws.
func (c *ConflictDetector) scanFile(path, name string, ws *WorkspaceState) {
	lower := strings.ToLower(name)

	// Application-level target type markers are file-name driven first.
	switch {
	case strings.Contains(lower, "todo"):
		c.mark(ws, "todo_app", "file %s matches todo marker", name)
	case strings.Contains(lower, "portfolio"):
		c.mark(ws, "portfolio", "file %s matches portfolio marker", name)
	}

	// Technical archetype markers.
	switch {
	case lower == "go.mod":
		c.markArchetype(ws, "go", "go.mod present")
	case lower == "package.json" && hasPackageDep(path, "react", "next"):
		c.markArchetype(ws, "react", "package.json declares react/next")
	case strings.HasSuffix(lower, ".html"):
		c.markArchetype(ws, "vanilla_web", "html file present")
	}

	// Content markers are only consulted for small text files.
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if len(data) > c.contentCap {
		return
	}
	content := strings.ToLower(string(data))
	if c.hasAny(content, []string{"todo app", "task list", "tasklist", "addtask", "newtodo", "todolist", "let todos", "todos =", "to-do list"}) {
		c.mark(ws, "todo_app", "content of %s matches todo marker", name)
	}
	if strings.Contains(content, "portfolio") {
		c.mark(ws, "portfolio", "content of %s matches portfolio marker", name)
	}
}

// mark records an application-level target type marker.
func (c *ConflictDetector) mark(ws *WorkspaceState, appType, format string, args ...any) {
	if ws.AppTypes[appType] {
		return
	}
	ws.AppTypes[appType] = true
	ws.Markers = append(ws.Markers, fmt.Sprintf(format, args...))
}

// markArchetype records a technical archetype marker.
func (c *ConflictDetector) markArchetype(ws *WorkspaceState, arch, detail string) {
	if ws.Archetypes[arch] {
		return
	}
	ws.Archetypes[arch] = true
	ws.Markers = append(ws.Markers, detail)
}

// hasAny reports whether content contains any of the needles.
func (c *ConflictDetector) hasAny(content string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(content, n) {
			return true
		}
	}
	return false
}

// hasPackageDep reports whether the JSON file at path declares one of the
// given dependency names. It is best-effort and never fails on malformed
// input.
func hasPackageDep(path string, names ...string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	for _, n := range names {
		if strings.Contains(lower, `"`+n+`"`) {
			return true
		}
	}
	return false
}

// SortedKeys returns the true-valued keys of set in stable sorted order.
func SortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k, on := range set {
		if on {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Process compares the resolved intent against the observed workspace state
// and reports a Conflict when a high-impact mismatch exists. Conflicts arise
// when (1) the prompt explicitly excludes a target that the workspace is
// currently built as, or (2) the prompt targets a type that differs from an
// established application type on disk.
func (c *ConflictDetector) Process(res *Resolution, ws WorkspaceState) Conflict {
	if ws.Empty || len(ws.AppTypes) == 0 {
		return Conflict{}
	}

	detected := SortedKeys(ws.AppTypes)
	conf := Conflict{Requested: res.TargetType, Detected: detected}

	// Explicit negation wins: the user said "not X" while the workspace is X.
	for negated := range res.Negated {
		if ws.AppTypes[negated] {
			conf.Present = true
			conf.Reason = fmt.Sprintf("intent explicitly excludes %s but the workspace is a %s workspace", negated, negated)
			return conf
		}
	}

	// Target mismatch: the requested type differs from every established one.
	if res.TargetType != "" && !ws.AppTypes[res.TargetType] {
		conf.Present = true
		conf.Reason = fmt.Sprintf("requested %s over an existing %s workspace", res.TargetType, strings.Join(detected, ", "))
		return conf
	}
	return conf
}

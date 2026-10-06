package kernel_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// bannedImports is the set of modules the kernel and its capability packages must
// never depend on.
//
// The UI entries are the architectural law: the kernel executes, the terminal
// displays, and the dependency only ever points one way. The provider entries are
// a provider-neutrality law: the kernel must not know that OpenRouter, Ollama, or
// any particular model exists, because a kernel that names a provider cannot
// have its decisions replaced by swapping one.
var bannedImports = map[string]string{
	"github.com/charmbracelet/bubbletea":    "terminal UI framework",
	"github.com/charmbracelet/lipgloss":     "terminal styling library",
	"github.com/charmbracelet/bubbles":      "terminal UI components",
	"github.com/charmbracelet/x/ansi":       "terminal escape handling",
	"github.com/charmbracelet/x/term":       "terminal capability detection",
	"github.com/charmbracelet/x/cellbuf":    "terminal cell buffers",
	"github.com/charmbracelet/colorprofile": "terminal color detection",
	"github.com/atotto/clipboard":           "clipboard access",
	"github.com/muesli/termenv":             "terminal environment detection",
	"github.com/muesli/reflow":              "text reflow for terminals",
	"github.com/muesli/cancelreader":        "terminal input cancellation",
	"github.com/charmbracelet/x/input":      "terminal input encoding",
	"github.com/charmbracelet/x/exp/tea":    "terminal UI framework",
}

// kernelPackages are the directories whose import graph is locked.
var kernelPackages = []string{
	"kernel",
	"capabilities/filesystem",
}

// TestKernelHasNoUIDependencies is the architectural lock that makes the kernel
// reusable outside IZEN.
//
// It fails if any production file in the kernel subtree imports a terminal UI
// library. The test parses imports directly rather than inspecting the module
// graph, because a transitive dependency introduced by any future change would
// otherwise be invisible until someone ran the binary in a headless environment
// and discovered it needed a TTY.
func TestKernelHasNoUIDependencies(t *testing.T) {
	root := repoRoot(t)
	for _, pkg := range kernelPackages {
		dir := filepath.Join(root, "runtime", pkg)
		forEachGoFile(t, dir, false, func(path, rel string, file *ast.File) {
			for _, imp := range file.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					continue
				}
				if why, banned := bannedImports[p]; banned {
					t.Errorf("%s imports %s (%s): the kernel must never depend on a terminal", rel, p, why)
				}
			}
		})
	}
}

// TestKernelHasNoInternalDependencies locks the open-source boundary.
//
// The kernel is meant to be importable by a future external user. Reaching into
// github.com/PizenLabs/izen/internal/... would make that impossible, because Go's
// internal/ rule excludes importers outside the module subtree. A capability
// package under runtime/ therefore has the same constraint.
func TestKernelHasNoInternalDependencies(t *testing.T) {
	root := repoRoot(t)
	const internalPrefix = "github.com/PizenLabs/izen/internal/"
	for _, pkg := range kernelPackages {
		dir := filepath.Join(root, "runtime", pkg)
		forEachGoFile(t, dir, false, func(path, rel string, file *ast.File) {
			for _, imp := range file.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					continue
				}
				if strings.HasPrefix(p, internalPrefix) {
					t.Errorf("%s imports %s: the runtime kernel must stand alone so it can be distributed independently", rel, p)
				}
			}
		})
	}
}

// TestKernelHasNoProviderDependencies locks provider neutrality.
//
// The kernel must not know a provider exists. A kernel that references one cannot
// have its decisions replaced by substituting another, and it cannot be shipped
// as a general-purpose runtime because its authority model is welded to a
// transport.
func TestKernelHasNoProviderDependencies(t *testing.T) {
	root := repoRoot(t)
	forbidden := []string{
		"github.com/PizenLabs/izen/internal/providers",
		"github.com/PizenLabs/izen/internal/provider",
		"github.com/PizenLabs/izen/internal/ai",
		"github.com/PizenLabs/izen/internal/llm",
		"github.com/PizenLabs/izen/internal/config",
	}
	for _, pkg := range kernelPackages {
		dir := filepath.Join(root, "runtime", pkg)
		forEachGoFile(t, dir, false, func(path, rel string, file *ast.File) {
			for _, imp := range file.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					continue
				}
				for _, bad := range forbidden {
					if p == bad || strings.HasPrefix(p, bad+"/") {
						t.Errorf("%s imports %s: the kernel must treat every provider as replaceable data, not as a dependency", rel, p)
					}
				}
			}
		})
	}
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate module root")
	return ""
}

// forEachGoFile parses every .go file under dir, invoking fn for each.
func forEachGoFile(t *testing.T, dir string, includeTests bool, fn func(path, rel string, file *ast.File)) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if !includeTests && strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Errorf("parse %s: %v", path, parseErr)
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot(t), path)
		if relErr != nil {
			rel = path
		}
		fn(path, filepath.ToSlash(rel), parsed)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

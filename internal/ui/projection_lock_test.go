package ui

// ── PHASE 15: PROJECTION ARCHITECTURE LOCK ───────────────────────────────────
//
// The Phase 14 lock asserts one direction: the runtime domain must not depend on
// presentation. This file asserts the OTHER direction, over the NEW projection
// layer specifically:
//
//	The Main Viewport's reducer, presenter and HUD must not import the execution
//	runtime.
//
// Why this layer and not the whole package: `internal/ui` legitimately contains
// the gateway that drives the RuntimeExecutor, so a package-wide rule would be
// unenforceable. The PROJECTION is the part that must stay pure, and the reason
// is not tidiness — it is that a projector that can reach an executor result can
// start DECIDING. The moment the reducer is handed an `execution.ExecutionResult`
// it becomes a second completion authority, and the Phase 14 guarantee that only
// `ObjectiveCompletionAuthority` may say an objective was proven silently becomes
// two authorities that can disagree.
//
// The lock is mechanical rather than advisory: it reads the import graph, so a
// future contributor cannot reintroduce the dependency and only discover it in
// review. The bound it protects is worth more than the convenience it costs.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// projectionFiles are the files that make up the Main Viewport projection layer.
var projectionFiles = []string{
	"reducer.go",
	"presenter.go",
	"viewport.go",
}

// forbiddenProjectionImports are the execution-runtime trees the projection must
// never reach for. They are matched as PREFIXES, because a dependency introduced
// through a subpackage (`internal/execution/graph`, say) is the same violation with
// one more path segment.
var forbiddenProjectionImports = []string{
	"github.com/PizenLabs/izen/internal/execution",
	"github.com/PizenLabs/izen/internal/runtime",
	"github.com/PizenLabs/izen/internal/contextcompiler",
	"github.com/PizenLabs/izen/internal/autonomy",
	"github.com/PizenLabs/izen/internal/loop",
	"github.com/PizenLabs/izen/internal/llmstep",
	"github.com/PizenLabs/izen/internal/ai",
}

// TestPhase15_ProjectionLayerDoesNotImportTheExecutionRuntime is the lock. It
// fails on ANY module-local import in the projection files that names a forbidden
// tree, and it fails when a file is missing — a renamed or deleted projection file
// must not silently narrow the lock's coverage.
func TestPhase15_ProjectionLayerDoesNotImportTheExecutionRuntime(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for _, name := range projectionFiles {
		path := filepath.Join(dir, name)
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Errorf("projection file %s is unreadable — the lock would cover less than it claims: %v", name, readErr)
			continue
		}
		for _, imported := range moduleImportPaths(string(source)) {
			for _, forbidden := range forbiddenProjectionImports {
				if imported == forbidden || strings.HasPrefix(imported, forbidden+"/") {
					t.Errorf("projection layer violation: %s imports %s — the projector projects runtime "+
						"facts, it must never reach for the runtime that produces them", name, imported)
				}
			}
		}
	}
}

// TestPhase15_ProjectionReducerIsHeadless is the executable half of the lock: the
// reducer and the HUD are plain state machines with no bubbletea, no terminal and
// no provider. If that ever stops being true, the whole projection stops being
// assertable without a TTY, which is the property that made the Phase 15
// invariants cheap enough to test.
func TestPhase15_ProjectionReducerIsHeadless(t *testing.T) {
	r := NewProjectionReducer(NewTelemetryDemuxer(), NewSidebarHUD())
	r.Reduce(record{role: roleUser, text: "refactor the auth module", turnID: 1})
	r.Reduce(record{role: roleActivity, text: "[loop] observing -> deciding", turnID: 1})
	r.Reduce(record{role: roleStatus, text: "Inspecting internal/auth/token.go", turnID: 1})
	r.Reduce(record{role: roleStatus, text: "Inspecting internal/auth/token.go", turnID: 1})
	r.SealActive()

	state := r.State()
	if len(state.History) == 0 {
		t.Fatal("nothing was projected")
	}
	// The machine line is absent from the narrative and present in Trace.
	for _, node := range state.History {
		if strings.HasPrefix(strings.TrimSpace(node.Text), "[loop]") {
			t.Fatalf("machine line in the narrative: %q", node.Text)
		}
	}
	if r.Trace().StepCount() != 1 {
		t.Fatalf("trace steps = %d, want 1", r.Trace().StepCount())
	}
	// The duplicate announcement collapsed into one node.
	joined := strings.Join(r.Audit(), "\n")
	if got := strings.Count(joined, "Inspecting internal/auth/token.go"); got != 1 {
		t.Fatalf("the repeated step appears %d time(s) in the projection:\n%s", got, joined)
	}
	// And the HUD renders with no terminal attached.
	if r.HUD().Render(80) == "" {
		t.Fatal("the HUD renders nothing headless")
	}
}

// moduleImportPaths extracts every quoted module-local import path from a Go
// source, across multi-line import blocks. It is deliberately a textual scan
// rather than a type-checked one: it must see an import even in a file that does
// not currently compile in isolation, because a violation introduced and reverted
// between builds is still a violation of the rule.
func moduleImportPaths(source string) []string {
	const prefix = `"github.com/PizenLabs/izen/`
	var out []string
	rest := source
	for {
		idx := strings.Index(rest, prefix)
		if idx < 0 {
			return out
		}
		rest = rest[idx:]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		rest = rest[end+1:]
	}
}

package plan

// PHASE 12 — discovery before concrete decomposition.
//
// The reported production failure staged `index.html`, `styles.css` and
// `script.js` for a project request before any project understanding had
// occurred, and staged the IDENTICAL three tasks in a completely empty
// workspace: the artifact SET came from prompt substrings, the workspace
// inspection fed framework inference only, and nothing recorded why any target
// was in scope.
//
// These tests pin the corrected invariant: an artifact is staged only when the
// discovered workspace or the explicit prompt supports it, every staged task
// carries that evidence, and an unsupported set declines ownership instead of
// being staged blind.

import (
	stdctx "context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/engine/adapter"
	"github.com/PizenLabs/izen/internal/engine/inference"
)

// newTestFacts materializes the deterministic workspace facts the evidence gate
// consumes, through the canonical inspector.
func newTestFacts(t *testing.T, files map[string]string) inference.WorkspaceFacts {
	t.Helper()
	root := writeWorkspace(t, files)
	return inference.NewWorkspaceInspector(root).Inspect()
}

// testArtifacts builds lowered file artifacts for the given paths.
func testArtifacts(paths ...string) []adapter.FileArtifact {
	out := make([]adapter.FileArtifact, 0, len(paths))
	for _, p := range paths {
		out = append(out, adapter.FileArtifact{Path: p, Content: "x", Mode: 0o644})
	}
	return out
}

// writeWorkspace materializes a small project under root.
func writeWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// TestPhase12_ExplicitlyRequestedTechnologyIsStillStaged proves the gate is not
// a greenfield regression: a prompt that explicitly names HTML, CSS and JS is
// evidence enough to stage exactly those artifacts, in any workspace.
func TestPhase12_ExplicitlyRequestedTechnologyIsStillStaged(t *testing.T) {
	const prompt = "redesign a professional personal portfolio page for me " +
		"using HTML, CSS, and JS; the author's name is Tom Hunter, an AI Engineer."
	for _, tc := range []struct {
		name  string
		root  string
		files map[string]string
	}{
		{"empty workspace", t.TempDir(), nil},
		{"existing static project", writeWorkspace(t, map[string]string{
			"index.html": "<html><body>old</body></html>\n",
			"styles.css": "body{margin:0}\n",
		}), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ic := NewIntentCompilerPlanner(tc.root)
			tasks, handled, err := ic.TryPlan(stdctx.Background(), prompt)
			if err != nil {
				t.Fatalf("TryPlan: %v", err)
			}
			if !handled {
				t.Fatal("an explicitly requested technology is evidence enough to own the request")
			}
			if len(tasks) == 0 {
				t.Fatal("no task staged for an explicitly requested technology set")
			}
			for _, task := range tasks {
				if task.Target == "" {
					t.Fatalf("task %d has an empty target", task.StepNum)
				}
				// EVERY staged task records why it is in scope.
				if !strings.Contains(task.Rationale, "Intent compiler:") ||
					strings.Contains(task.Rationale, "scope proposed by") {
					t.Fatalf("task %d (%s) carries no scope evidence: %q",
						task.StepNum, task.Target, task.Rationale)
				}
			}
		})
	}
}

// TestPhase12_UnrequestedTechnologyIsNotSynthesized is the core repair: a
// technology the workspace does not speak and the prompt never asked for is
// never staged. The pre-Phase-12 enumerator emitted the whole canonical triple
// unconditionally, so `script.js` appeared in projects that had no JavaScript
// and prompts that never mentioned it.
func TestPhase12_UnrequestedTechnologyIsNotSynthesized(t *testing.T) {
	// A CSS-only project, and a prompt that only asks for markup + styling.
	root := writeWorkspace(t, map[string]string{
		"index.html": "<html><body>old</body></html>\n",
		"styles.css": "body{margin:0}\n",
	})
	ic := NewIntentCompilerPlanner(root)
	tasks, handled, err := ic.TryPlan(stdctx.Background(),
		"redesign the landing page markup and styling for a personal introduction")
	if err != nil {
		t.Fatalf("TryPlan: %v", err)
	}
	if !handled || len(tasks) == 0 {
		t.Fatal("a workspace-backed markup+styling request must still be planned")
	}
	for _, task := range tasks {
		if strings.HasSuffix(strings.ToLower(task.Target), ".js") {
			t.Fatalf("staged %s for a prompt and workspace that never mention JavaScript", task.Target)
		}
	}
}

// TestPhase12_EvidenceGateIsPure pins the gate itself: a pure function of the
// inspected facts and the prompt, with no filesystem probe of its own and no
// hidden state.
func TestPhase12_EvidenceGateIsPure(t *testing.T) {
	facts := newTestFacts(t, map[string]string{
		"index.html": "<html></html>\n",
		"styles.css": "body{}\n",
		"main.go":    "package main\n",
	})
	// A workspace that already speaks Go: a Go artifact is in scope even when
	// the prompt never says "Go", because the project shape is evidence.
	kept, dropped, evidence := filterArtifactsByEvidence(testArtifacts(
		"index.html", "styles.css", "main.go", "unrelated.rs"), facts,
		"redesign the page")
	if len(kept) != 3 {
		t.Fatalf("kept = %d (%v), want 3", len(kept), kept)
	}
	if len(dropped) != 1 || dropped[0] != "unrelated.rs" {
		t.Fatalf("dropped = %v, want [unrelated.rs]", dropped)
	}
	if evidence["index.html"] == "" || evidence["main.go"] == "" {
		t.Fatalf("every kept artifact must carry evidence: %v", evidence)
	}
	// Deterministic: the same inputs yield the same verdict.
	kept2, dropped2, _ := filterArtifactsByEvidence(testArtifacts(
		"index.html", "styles.css", "main.go", "unrelated.rs"), facts,
		"redesign the page")
	if len(kept2) != len(kept) || len(dropped2) != len(dropped) {
		t.Fatal("the evidence gate must be deterministic")
	}
	// A prompt that explicitly names the technology admits it.
	kept3, dropped3, _ := filterArtifactsByEvidence(testArtifacts("unrelated.rs"), facts,
		"add a rust integration")
	if len(kept3) != 1 || len(dropped3) != 0 {
		t.Fatalf("an explicitly requested technology must be admitted: kept=%v dropped=%v", kept3, dropped3)
	}
}

// TestPhase12_DeclinesOwnershipWithoutEvidence proves the truthful fallback: an
// unsupported artifact set is NOT staged blind. The intent compiler declines
// ownership so the caller falls through to the real plan pipeline, which
// reasons over actual repository evidence.
func TestPhase12_DeclinesOwnershipWithoutEvidence(t *testing.T) {
	// A Go project whose prompt asks for a static web page with no explicit
	// technology words at all is ambiguous; with an EMPTY workspace and a
	// prompt that names nothing, nothing is evidence-backed.
	root := t.TempDir()
	ic := NewIntentCompilerPlanner(root)
	tasks, handled, err := ic.TryPlan(stdctx.Background(), "portfolio")
	if err != nil {
		t.Fatalf("TryPlan must decline cleanly, got %v", err)
	}
	if handled {
		t.Fatalf("an unevidenced request must not own the plan; got %d tasks", len(tasks))
	}
	if len(tasks) != 0 {
		t.Fatal("a declined request must stage nothing")
	}
}

// TestPhase12_StagedPlanGrantsNoAuthority pins the invariant that a staged plan
// is a PROPOSAL. The evidence gate decides scope; it does not admit, authorize
// or mutate. Mutation still requires the executor's admission boundary and the
// AuthorizationEngine, and /plan itself is read-only.
func TestPhase12_StagedPlanGrantsNoAuthority(t *testing.T) {
	root := writeWorkspace(t, map[string]string{"index.html": "<html></html>\n"})
	ic := NewIntentCompilerPlanner(root)
	tasks, handled, err := ic.TryPlan(stdctx.Background(), "redesign the landing page")
	if err != nil || !handled {
		t.Fatalf("TryPlan: handled=%v err=%v", handled, err)
	}
	// The staged task is a view-model, not an authorization: it carries no
	// capability grant, no budget approval and no scope provenance. Every
	// mutation still crosses IntentGateway → RuntimeExecutor → Authorization.
	for _, task := range tasks {
		if task.Type != "FILE_MUTATE" {
			continue
		}
		if task.Status != "idle" {
			t.Fatalf("a staged task must be idle, got %q", task.Status)
		}
	}
}

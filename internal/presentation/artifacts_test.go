package presentation

import (
	"strings"
	"testing"
)

// ── PHASE 6 — ARTIFACT RENDERER ─────────────────────────────────────────────
//
// Artifacts are rendered by semantic type (response, plan, diff, inspection,
// verification, error) — never printed as raw JSON. The presentation layer
// classifies the runtime kind (interpretation); the renderer formats the typed
// view (visual output only, no business logic).

// TestClassifyArtifact pins the runtime kind → semantic type mapping.
func TestClassifyArtifact(t *testing.T) {
	cases := []struct {
		kind string
		want ArtifactType
	}{
		{"plan", ArtifactPlan},
		{"patch", ArtifactDiff},
		{"diff", ArtifactDiff},
		{"investigation", ArtifactInspection},
		{"verification", ArtifactVerification},
		{"error", ArtifactError},
		{"explanation", ArtifactResponse},
		{"", ArtifactResponse},
	}
	for _, tc := range cases {
		if got := ClassifyArtifact(tc.kind); got != tc.want {
			t.Errorf("ClassifyArtifact(%q) = %s, want %s", tc.kind, got, tc.want)
		}
	}
}

// TestJSONPlanArtifactUsesSemanticRenderer pins that a JSON plan artifact is
// rendered through the semantic renderer — the output is a semantic task list,
// never the raw JSON.
func TestJSONPlanArtifactUsesSemanticRenderer(t *testing.T) {
	rawJSON := `{
		"strategic_overview": {
			"impact_domain": "Execution Layer — Dependency Resolution",
			"risk_evaluation": "Low",
			"verification_vector": "Build + Test pipeline"
		},
		"atomic_tasks": [
			{"task_id": 1, "file": "internal/execution/resolver.go", "strategy": "FILE_MUTATE", "description": "add dependency resolution"},
			{"task_id": 2, "file": "internal/execution/verify.go", "strategy": "FILE_MUTATE", "description": "wire verification"}
		]
	}`

	renderer := DefaultArtifactRenderer{}
	lines := renderer.Render(ArtifactView{Type: ArtifactPlan, Kind: "plan", Target: "execution", Content: rawJSON})

	if len(lines) == 0 {
		t.Fatal("plan renderer produced no lines")
	}
	joined := strings.Join(lines, "\n")
	// Semantic content must be present.
	if !strings.Contains(joined, "resolver.go") || !strings.Contains(joined, "verify.go") {
		t.Errorf("plan renderer lost the task list: %q", joined)
	}
	if !strings.Contains(joined, "2 steps") {
		t.Errorf("plan renderer missing the step count: %q", joined)
	}
	// Raw JSON must NEVER leak.
	if strings.Contains(joined, "{") || strings.Contains(joined, "atomic_tasks") ||
		strings.Contains(joined, "\"task_id\"") || strings.Contains(joined, "}") {
		t.Errorf("plan renderer leaked raw JSON: %q", joined)
	}
}

// TestUnparseablePlanNeverDumpsRawJSON pins that even an unparseable plan
// payload is rendered as a truthful notice — never the raw text.
func TestUnparseablePlanNeverDumpsRawJSON(t *testing.T) {
	renderer := DefaultArtifactRenderer{}
	lines := renderer.Render(ArtifactView{Type: ArtifactPlan, Kind: "plan", Content: "{\"garbage\": true}"})
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "garbage") {
		t.Errorf("unparseable plan leaked raw payload: %q", joined)
	}
	if len(lines) == 0 {
		t.Fatal("unparseable plan produced no truthful notice")
	}
}

// TestDiffArtifactSemanticRender pins the diff renderer: the diff text is
// presented with a truthful header.
func TestDiffArtifactSemanticRender(t *testing.T) {
	renderer := DefaultArtifactRenderer{}
	lines := renderer.Render(ArtifactView{Type: ArtifactDiff, Kind: "patch", Target: "index.html", Content: "--- a/index.html\n+++ b/index.html\n-<p>old</p>\n+<p>new</p>"})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "diff index.html") {
		t.Errorf("diff renderer missing target header: %q", joined)
	}
	if !strings.Contains(joined, "+<p>new</p>") {
		t.Errorf("diff renderer lost the diff body: %q", joined)
	}
}

// TestResponseArtifactSemanticRender pins the response renderer: the content
// is rendered as human text.
func TestResponseArtifactSemanticRender(t *testing.T) {
	renderer := DefaultArtifactRenderer{}
	lines := renderer.Render(ArtifactView{Type: ArtifactResponse, Kind: "explanation", Content: "The build failed because of a type mismatch."})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "build failed") {
		t.Errorf("response renderer lost the content: %q", joined)
	}
}

// TestRenderArtifactConvenience pins the classify-and-render convenience: a
// plan kind is classified and rendered semantically in one call.
func TestRenderArtifactConvenience(t *testing.T) {
	lines := RenderArtifact("plan", "execution", `{"atomic_tasks":[{"file":"x.go","description":"fix"}]}`)
	if len(lines) == 0 {
		t.Fatal("RenderArtifact produced no lines")
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "atomic_tasks") || strings.Contains(joined, "{") {
		t.Errorf("RenderArtifact leaked raw JSON for a plan kind: %q", joined)
	}
	if !strings.Contains(joined, "x.go") {
		t.Errorf("RenderArtifact lost the task file: %q", joined)
	}
}

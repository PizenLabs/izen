package inference

import (
	"path/filepath"
	"testing"
)

// Test2_EvidenceTraceability is the primary verification contract of the
// inference engine: a workspace containing next.config.ts must produce an
// InferenceSet whose Next.js hypothesis carries exactly the evidence traces
// config:next.config.ts (+0.30) and dependency:next (+0.60).
func Test2_EvidenceTraceability(t *testing.T) {
	facts := NewWorkspaceFacts("/tmp/w")
	facts.Files = []string{"next.config.ts", "package.json"}
	facts.Configs = []string{"next.config.ts"}
	facts.Dependencies = map[string]string{"next": "14.2.0"}

	set := NewInferenceEngine().Infer(facts, PromptSlots{})

	framework := set.Framework()
	if framework.Label != "Next.js" {
		t.Fatalf("resolved framework = %q, want %q", framework.Label, "Next.js")
	}

	var configWeight, depWeight float64
	var sawConfig, sawDep bool
	for _, tr := range framework.Evidence {
		switch tr.Key() {
		case "config:next.config.ts":
			sawConfig = true
			configWeight = tr.Weight
			if tr.Source != SourceConfig {
				t.Errorf("config trace source = %q, want %q", tr.Source, SourceConfig)
			}
		case "dependency:next":
			sawDep = true
			depWeight = tr.Weight
			if tr.Source != SourceDependency {
				t.Errorf("dependency trace source = %q, want %q", tr.Source, SourceDependency)
			}
		}
	}
	if !sawConfig {
		t.Fatal("expected evidence trace config:next.config.ts")
	}
	if configWeight != 0.30 {
		t.Errorf("config:next.config.ts weight = %.2f, want +0.30", configWeight)
	}
	if !sawDep {
		t.Fatal("expected evidence trace dependency:next")
	}
	if depWeight != 0.60 {
		t.Errorf("dependency:next weight = %.2f, want +0.60", depWeight)
	}

	// The Next.js hypothesis score is the sum of its trace weights.
	top, ok := set.Top(TypeFramework)
	if !ok {
		t.Fatal("no framework hypothesis emitted")
	}
	if got := top.Score(); !almostEqual(got, 0.90) {
		t.Errorf("Next.js hypothesis score = %.4f, want 0.90", got)
	}
}

// almostEqual compares floats within a small epsilon, since evidence weights
// are decimal constants subject to binary representation noise.
func almostEqual(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-6
}

func TestInferRanksHypothesesByScore(t *testing.T) {
	// Astro signal is stronger (config 0.30 + dep 0.60) than a mere prompt
	// mention, so Astro must rank first even when both fire.
	facts := NewWorkspaceFacts("/tmp/w")
	facts.Configs = []string{"astro.config.mjs"}
	facts.Dependencies = map[string]string{"astro": "4.0.0"}
	slots := PromptSlots{Raw: "make a next.js blog with react"}

	set := NewInferenceEngine().Infer(facts, slots)
	hyps := set.Hypotheses(TypeFramework)
	if len(hyps) == 0 {
		t.Fatal("expected at least one framework hypothesis")
	}
	if hyps[0].Label != "Astro" {
		t.Fatalf("top framework = %q, want Astro (stronger evidence wins)", hyps[0].Label)
	}
	// Scores must be descending.
	for i := 1; i < len(hyps); i++ {
		if hyps[i-1].Score() < hyps[i].Score() {
			t.Fatalf("hypotheses not ranked by score: %v", hyps)
		}
	}
}

func TestInferAllDimensions(t *testing.T) {
	facts := NewWorkspaceFacts("/tmp/w")
	facts.Configs = []string{"next.config.ts", "tailwind.config.ts", "tsconfig.json"}
	facts.Files = []string{"next.config.ts", "package.json", "app/layout.tsx"}
	facts.Dependencies = map[string]string{"next": "14.2.0", "tailwindcss": "3.4.0", "typescript": "5.4.0"}

	set := NewInferenceEngine().Infer(facts, PromptSlots{})

	if set.ResolvedFramework() != "Next.js" {
		t.Errorf("framework = %q, want Next.js", set.ResolvedFramework())
	}
	if set.ResolvedLanguage() != "TypeScript" {
		t.Errorf("language = %q, want TypeScript", set.ResolvedLanguage())
	}
	if set.ResolvedStyling() != "Tailwind CSS" {
		t.Errorf("styling = %q, want Tailwind CSS", set.ResolvedStyling())
	}
	router := set.ResolvedRouter()
	if router == "" {
		t.Error("expected a router hypothesis")
	}
}

func TestInferEmptyWorkspaceProducesNoFramework(t *testing.T) {
	facts := NewWorkspaceFacts("/tmp/empty")
	set := NewInferenceEngine().Infer(facts, PromptSlots{})
	if set.ResolvedFramework() != "" {
		t.Errorf("framework = %q, want empty for an empty workspace", set.ResolvedFramework())
	}
	if _, ok := set.Top(TypeFramework); ok {
		t.Error("expected no framework hypothesis for an empty workspace")
	}
}

func TestInferenceSetAllEvidenceIsFlatAndSorted(t *testing.T) {
	facts := NewWorkspaceFacts("/tmp/w")
	facts.Configs = []string{"next.config.ts"}
	facts.Dependencies = map[string]string{"next": "14.2.0", "tailwindcss": "3.4.0"}
	facts.Files = []string{"next.config.ts", "package.json", "app/page.tsx"}
	facts.Directories = []string{"app"}

	set := NewInferenceEngine().Infer(facts, PromptSlots{})
	all := set.AllEvidence()
	if len(all) == 0 {
		t.Fatal("expected evidence across dimensions")
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Key() > all[i].Key() {
			t.Fatalf("AllEvidence not sorted: %v -> %v", all[i-1].Key(), all[i].Key())
		}
	}
}

func TestPromptSlotsKeywordMatching(t *testing.T) {
	slots := PromptSlots{Raw: "Build a Next.js blog with Tailwind"}
	if !slots.HasKeyword("next.js") {
		t.Error("expected prompt to match next.js keyword")
	}
	if !slots.HasKeyword("TAILWIND") {
		t.Error("expected case-insensitive keyword match")
	}
	if slots.HasKeyword("astro") {
		t.Error("unexpected astro keyword match")
	}
}

func TestPromptMentionsDriveHypotheses(t *testing.T) {
	facts := NewWorkspaceFacts("/tmp/empty")
	slots := PromptSlots{Raw: "I want a website using vite and react"}
	set := NewInferenceEngine().Infer(facts, slots)
	if set.ResolvedFramework() != "React + Vite" {
		t.Errorf("framework = %q, want React + Vite from prompt alone", set.ResolvedFramework())
	}
}

// TestStaticWebFrameworkFromPrompt is the verification contract for the intent
// compiler: a greenfield "using HTML, CSS, and JavaScript" prompt resolves to
// the Static HTML/CSS/JS framework with enough evidence to Proceed.
func TestStaticWebFrameworkFromPrompt(t *testing.T) {
	facts := NewWorkspaceFacts("/tmp/empty")
	slots := PromptSlots{Raw: "Design a website introducing JAY, describing your job as a software engineer, using HTML, CSS, and JavaScript."}
	set := NewInferenceEngine().Infer(facts, slots)

	if set.ResolvedFramework() != "Static HTML/CSS/JS" {
		t.Fatalf("framework = %q, want Static HTML/CSS/JS", set.ResolvedFramework())
	}
	top, ok := set.Top(TypeFramework)
	if !ok {
		t.Fatal("expected a framework hypothesis")
	}
	if top.Score() < 0.45 {
		t.Fatalf("framework confidence %.2f below the proceed threshold", top.Confidence())
	}
	if v := NewPolicyEngine().Evaluate(set, TypeFramework); v.Decision != DecisionProceed {
		t.Fatalf("policy = %q, want proceed (%s)", v.Decision, v.Reason)
	}
}

// TestWorkspaceInspector verifies the deterministic first stage of the intent
// compiler collects facts without assuming the workspace exists.
func TestWorkspaceInspector(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "next.config.ts", "export default {};\n")
	writeFile(t, root, "package.json", `{ "dependencies": { "next": "14.2.0" } }`)

	inspector := NewWorkspaceInspector(root)
	if inspector.Root() != root {
		t.Fatalf("Root = %q, want %q", inspector.Root(), root)
	}
	facts := inspector.Inspect()
	if len(facts.Files) == 0 {
		t.Fatal("inspector collected no files")
	}
	if facts.Dependencies["next"] != "14.2.0" {
		t.Errorf("next dep = %q, want 14.2.0", facts.Dependencies["next"])
	}
}

func TestWorkspaceInspectorMissingRootDegrades(t *testing.T) {
	inspector := NewWorkspaceInspector(filepath.Join(t.TempDir(), "missing"))
	facts := inspector.Inspect()
	if len(facts.Files) != 0 {
		t.Fatalf("missing root must degrade to an empty surface, got %v", facts.Files)
	}
}

package inference

import (
	"os"
	"path/filepath"
	"testing"

	stdctx "context"

	"github.com/PizenLabs/izen/pkg/engine/context"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestAnalyzeWorkspaceCollectsFacts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "next.config.ts", "export default {};\n")
	writeFile(t, root, "package.json", `{
  "name": "demo",
  "dependencies": { "next": "14.2.0", "react": "18.3.0" },
  "devDependencies": { "typescript": "5.4.0" }
}`)
	writeFile(t, root, "app/layout.tsx", "export default function Layout() {}\n")
	writeFile(t, root, "tailwind.config.ts", "export default {};\n")

	facts := AnalyzeWorkspace(root)

	if len(facts.Files) == 0 {
		t.Fatal("expected files to be collected")
	}
	if !hasFile(facts.Files, "next.config.ts") {
		t.Error("expected next.config.ts in files")
	}
	if len(facts.Configs) == 0 {
		t.Fatal("expected configs to be recognized")
	}
	if facts.Dependencies["next"] != "14.2.0" {
		t.Errorf("next dep = %q, want 14.2.0", facts.Dependencies["next"])
	}
	if facts.Dependencies["typescript"] != "5.4.0" {
		t.Errorf("typescript dep = %q, want 5.4.0 (devDependencies parsed)", facts.Dependencies["typescript"])
	}
	foundDir := false
	for _, d := range facts.Directories {
		if d == "app" {
			foundDir = true
		}
	}
	if !foundDir {
		t.Errorf("expected app directory, got %v", facts.Directories)
	}
}

func TestAnalyzeWorkspaceParsesGoMod(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", `module example.com/demo

go 1.22

require (
	github.com/gin-gonic/gin v1.9.1
	github.com/stretchr/testify v1.8.4
)
`)
	facts := AnalyzeWorkspace(root)
	if len(facts.GoDeps) != 2 {
		t.Fatalf("GoDeps = %v, want 2 modules", facts.GoDeps)
	}
	if !hasDep(facts.Dependencies, "go:github.com/gin-gonic/gin") {
		t.Errorf("expected gin in dependency map, got %v", facts.Dependencies)
	}
}

func TestAnalyzeWorkspaceMissingRootDegrades(t *testing.T) {
	facts := AnalyzeWorkspace(filepath.Join(t.TempDir(), "does-not-exist"))
	if len(facts.Files) != 0 {
		t.Fatalf("expected empty facts for missing root, got %v", facts.Files)
	}
}

func TestFromPlanningContext(t *testing.T) {
	pc := fakePlanningContext(t)
	facts := FromPlanningContext(pc)
	if len(facts.Files) != 2 {
		t.Fatalf("files = %v, want 2", facts.Files)
	}
	if prompt := PromptFromPlanningContext(pc); prompt != "build a website" {
		t.Errorf("prompt = %q, want %q", prompt, "build a website")
	}
}

// fakePlanningContext builds a real PlanningContext via the microkernel
// collector over a temp workspace so the filesystem + prompt chunks are
// populated.
func fakePlanningContext(t *testing.T) context.PlanningContext {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "index.html", "<html></html>\n")
	writeFile(t, root, "styles.css", "body {}\n")
	collector := context.NewCollector()
	collector.Register(context.ProviderFilesystem, context.NewFilesystemProvider(root, 0, true))
	collector.Register(context.ProviderPrompt, context.NewPromptProvider("build a website"))
	pc, err := collector.Collect(stdctx.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return pc
}

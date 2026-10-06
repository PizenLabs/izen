package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
)

// TestPipelineBrownfieldMutationCrossesKernelAuthority is the real brownfield
// `izen run` smoke test. It begins with an EXISTING workspace containing files
// (so the auto-detector classifies it brownfield), runs the full pipeline, and
// proves the mutation crossed the complete path:
//
//	brownfield detection → mutation request → Core authorization
//	→ kernelbridge → runtime/kernel → actual file change
//	→ primitive + Core evidence.
//
// A greenfield-only test is not proof of this path, which is why the workspace
// is seeded before the run.
func TestPipelineBrownfieldMutationCrossesKernelAuthority(t *testing.T) {
	root := t.TempDir()
	// An existing Go module with an existing page: brownfield, unmistakably.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!DOCTYPE html><html><body>old</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen),
		WithVerifyCommand(func(string) string { return "true" }))

	evCh := collectBusEvents(t, p.Bus())

	res, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Mode != ModeBrownfield {
		t.Fatalf("mode = %s, want brownfield (existing workspace)", res.Mode)
	}

	got := readFile(t, filepath.Join(root, "index.html"))
	if !strings.Contains(got, "My Portfolio") {
		t.Fatalf("brownfield mutation did not change the target: %q", got)
	}

	// The proof artifact is Core evidence produced by the substrate, and the
	// only way it exists is that the mutation crossed the kernel.
	proofs, err := filepath.Glob(filepath.Join(root, ".izen", "substrate", "*.proof"))
	if err != nil {
		t.Fatalf("glob proofs: %v", err)
	}
	if len(proofs) == 0 {
		t.Fatal("brownfield run produced no Core proof artifact: the mutation did not reach the kernel")
	}
	var sawProven bool
	for _, path := range proofs {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read proof %s: %v", path, readErr)
		}
		if strings.Contains(string(data), "outcome=PROVEN") {
			sawProven = true
		}
	}
	if !sawProven {
		t.Fatalf("no brownfield proof recorded a PROVEN kernel outcome: %v", proofs)
	}

	waitForEvent(t, evCh, events.EventTaskCompleted, "bf-verify")
}

// TestPipelineBrownfieldNoAuthorityFailsClosed proves the pipeline refuses a
// brownfield mutation when no Core authority is bound, rather than mutating
// through a raw resource.
func TestPipelineBrownfieldNoAuthorityFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen),
		WithVerifyCommand(func(string) string { return "true" }))
	// Simulate a configuration with no Core authority; the pipeline must fail
	// closed rather than let the graph write through a raw resource.
	p.substrate = nil

	if _, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"}); err == nil {
		t.Fatal("a brownfield run with no Core authority did not fail")
	}
	if _, statErr := os.Stat(filepath.Join(root, "index.html")); statErr == nil {
		t.Fatal("a brownfield run with no Core authority mutated the workspace")
	}
}

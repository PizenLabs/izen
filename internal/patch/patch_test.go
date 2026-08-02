package patch

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func collect(t *testing.T, bus *events.Bus, types ...string) *sync.Map {
	t.Helper()
	got := &sync.Map{}
	for _, typ := range types {
		bus.Subscribe(typ, func(ev events.DomainEvent) {
			got.Store(ev.Type(), ev)
		})
	}
	return got
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestTier1StructuredDiff(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "greet.go", "package main\n\nfunc greet() string {\n\treturn \"hello\"\n}\n")

	raw := "--- a/greet.go\n+++ b/greet.go\n@@ -3,2 +3,2 @@\n func greet() string {\n-\treturn \"hello\"\n+\treturn \"hi\"\n }\n"

	eng := NewEngine()
	res, err := eng.Apply(root, Request{File: "greet.go", Raw: raw})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Applied {
		t.Fatal("expected applied")
	}
	if res.Tier != Tier1StructuredDiff {
		t.Errorf("tier = %d, want %d", res.Tier, Tier1StructuredDiff)
	}
	got := readFile(t, root, "greet.go")
	if got != "package main\n\nfunc greet() string {\n\treturn \"hi\"\n}\n" {
		t.Errorf("file content = %q", got)
	}
}

func TestTier2SearchReplace(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "greet.go", "package main\n\nfunc greet() string {\n\treturn \"hello\"\n}\n")

	raw := "<<<<<<< SEARCH\n\treturn \"hello\"\n=======\n\treturn \"hi\"\n>>>>>>> REPLACE"

	eng := NewEngine()
	res, err := eng.Apply(root, Request{File: "greet.go", Raw: raw})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Applied || res.Tier != Tier2SearchReplace {
		t.Fatalf("res = %+v, want applied tier 2", res)
	}
	got := readFile(t, root, "greet.go")
	if got != "package main\n\nfunc greet() string {\n\treturn \"hi\"\n}\n" {
		t.Errorf("file content = %q", got)
	}
}

// TestTier3WholeFileRewriteMarkupRedesign is the RFC acceptance case: the
// ContextualSafetyEvaluator permits a full HTML rewrite when the task objective
// expresses a structural redesign intent.
func TestTier3WholeFileRewriteMarkupRedesign(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "index.html", "<html><body><h1>old</h1></body></html>\n")

	raw := "<html><body><h1>new</h1><p>redesigned</p></body></html>\n"

	eng := NewEngine()
	res, err := eng.Apply(root, Request{
		File:          "index.html",
		FileType:      ".html",
		TaskObjective: "Redesign the landing page layout with a modern look",
		Raw:           raw,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Applied {
		t.Fatal("expected applied")
	}
	if res.Tier != Tier3WholeFile {
		t.Errorf("tier = %d, want %d", res.Tier, Tier3WholeFile)
	}
	if got := readFile(t, root, "index.html"); got != raw {
		t.Errorf("file content = %q, want %q", got, raw)
	}
}

// TestTier3WholeFileRewriteSourceRequiresApproval confirms a whole-file
// rewrite of a source file engages Tier 4 human approval rather than writing.
func TestTier3WholeFileRewriteSourceRequiresApproval(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")

	eng := NewEngine()
	res, err := eng.Apply(root, Request{
		File:          "main.go",
		FileType:      ".go",
		TaskObjective: "Rewrite main.go",
		Raw:           "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
	})
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("err = %v, want ErrApprovalRequired", err)
	}
	if res.Applied {
		t.Fatal("expected no write before approval")
	}
	// The file is untouched.
	if got := readFile(t, root, "main.go"); got != "package main\n\nfunc main() {}\n" {
		t.Errorf("file content = %q, want unchanged", got)
	}

	// Tier 4: human approves, then the same payload applies.
	res2, err := eng.Apply(root, Request{
		File:          "main.go",
		FileType:      ".go",
		TaskObjective: "Rewrite main.go",
		Raw:           "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
		Approved:      true,
	})
	if err != nil {
		t.Fatalf("Apply with approval: %v", err)
	}
	if !res2.Applied || res2.Tier != Tier3WholeFile {
		t.Fatalf("res2 = %+v, want applied tier 3", res2)
	}
}

func TestAmbiguousSnippetRejected(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main\n\nfunc main() { println(\"hi\") }\n")

	eng := NewEngine()
	// A tiny raw snippet with no markers against an existing larger file.
	_, err := eng.Apply(root, Request{
		File: "main.go",
		Raw:  "func main() {",
	})
	if !errors.Is(err, ErrAmbiguousPatch) {
		t.Fatalf("err = %v, want ErrAmbiguousPatch", err)
	}
}

func TestAlreadyAppliedIdempotent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "greet.go", "package main\n\nfunc greet() string {\n\treturn \"hi\"\n}\n")

	raw := "<<<<<<< SEARCH\n\treturn \"hi\"\n=======\n\treturn \"hi\"\n>>>>>>> REPLACE"

	eng := NewEngine()
	_, err := eng.Apply(root, Request{File: "greet.go", Raw: raw})
	if !errors.Is(err, ErrAlreadyApplied) {
		t.Fatalf("err = %v, want ErrAlreadyApplied", err)
	}
}

func TestDestructiveEmptyRejected(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")

	eng := NewEngine()
	_, err := eng.Apply(root, Request{
		File: "main.go",
		Raw:  "", // empty payload cannot empty a file
	})
	if !errors.Is(err, ErrAmbiguousPatch) {
		t.Fatalf("err = %v, want ErrAmbiguousPatch", err)
	}
}

func TestEventsPublished(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "greet.go", "package main\n\nfunc greet() string {\n\treturn \"hello\"\n}\n")

	bus := events.NewBus(events.DefaultBufferSize)
	defer bus.Close()
	got := collect(t, bus, events.EventPatchParsed, events.EventPatchValidated, events.EventPatchRejected, events.EventApprovalRequested)

	eng := NewEngine().WithEventBus(bus)
	raw := "<<<<<<< SEARCH\n\treturn \"hello\"\n=======\n\treturn \"hi\"\n>>>>>>> REPLACE"
	if _, err := eng.Apply(root, Request{File: "greet.go", Raw: raw}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	waitFor(t, func() bool {
		_, ok := got.Load(events.EventPatchValidated)
		return ok
	})

	if _, ok := got.Load(events.EventPatchParsed); !ok {
		t.Error("expected EventPatchParsed")
	}
	if _, ok := got.Load(events.EventPatchValidated); !ok {
		t.Error("expected EventPatchValidated")
	}

	// Tier 4 request path.
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	got2 := collect(t, bus, events.EventApprovalRequested)
	if _, err := eng.Apply(root, Request{
		File:          "main.go",
		FileType:      ".go",
		TaskObjective: "Rewrite main.go",
		Raw:           "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
	}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("err = %v, want ErrApprovalRequired", err)
	}
	waitFor(t, func() bool {
		_, ok := got2.Load(events.EventApprovalRequested)
		return ok
	})
}

func TestNoBusDoesNotPanic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "greet.go", "package main\n\nfunc greet() string {\n\treturn \"hello\"\n}\n")

	eng := NewEngine()
	raw := "<<<<<<< SEARCH\n\treturn \"hello\"\n=======\n\treturn \"hi\"\n>>>>>>> REPLACE"
	if _, err := eng.Apply(root, Request{File: "greet.go", Raw: raw}); err != nil {
		t.Fatalf("Apply without bus: %v", err)
	}
}

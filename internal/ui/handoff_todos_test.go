package ui

import (
	"strings"
	"testing"
)

// TestParseProposedFixIntoTodos_NoFloodFromForensics is the regression guard for
// the /investigate → /plan deadlock: a structured forensics handoff blob (raw
// diagnostics, code fences, section headers, [PKT-N] framing, compiler output)
// must NOT be exploded into a flood of junk pending TODOs. Previously this
// produced ~16-18 bogus items and stalled plan synthesis.
func TestParseProposedFixIntoTodos_NoFloodFromForensics(t *testing.T) {
	fix := strings.Join([]string{
		"### INJECTED INVESTIGATION FORENSICS",
		"- Target File: cmd/api/main.go",
		"- Diagnostics Error Log:",
		"```",
		"cmd/api/main.go:7:5: undefined: Router",
		"go: downloading github.com/foo/bar",
		"no required module provides package github.com/foo/bar",
		"```",
		"### ANALYTICAL PACKETS (sequential, ID-addressed)",
		"Total packets: 3",
		`[PKT-1] kind=problem title="Investigation problem statement"`,
		"build failed",
		"[PKT-2] kind=target file=cmd/api/main.go:7",
		"node=main kind=func",
		`[PKT-3] kind=conclusion title="Investigation conclusion"`,
		"missing dependency",
	}, "\n")

	todos := parseProposedFixIntoTodos(fix)

	if len(todos) > maxPendingTodos {
		t.Fatalf("forensics blob produced %d todos, exceeding cap %d: %#v",
			len(todos), maxPendingTodos, todos)
	}
	for _, td := range todos {
		if isHandoffNoiseLine(td) {
			t.Errorf("noise line leaked into todos: %q", td)
		}
	}
}

// TestParseProposedFixIntoTodos_ExtractsMarkedTasks verifies genuine, explicitly
// marked task items are still extracted, deduplicated, and clamped.
func TestParseProposedFixIntoTodos_ExtractsMarkedTasks(t *testing.T) {
	fix := strings.Join([]string{
		"### PLAN",
		"- [ ] Fix Router import in cmd/api/main.go",
		"- [ ] Add github.com/foo/bar to go.mod",
		"- [ ] Fix Router import in cmd/api/main.go", // duplicate
		"✓ Re-run build verification",
		"```",
		"raw log noise",
		"```",
	}, "\n")

	todos := parseProposedFixIntoTodos(fix)

	want := []string{
		"Fix Router import in cmd/api/main.go",
		"Add github.com/foo/bar to go.mod",
		"Re-run build verification",
	}
	if len(todos) != len(want) {
		t.Fatalf("got %d todos, want %d: %#v", len(todos), len(want), todos)
	}
	for i, w := range want {
		if todos[i] != w {
			t.Errorf("todo[%d] = %q, want %q", i, todos[i], w)
		}
	}
}

// TestParseProposedFixIntoTodos_Cap enforces the hard maxPendingTodos ceiling.
func TestParseProposedFixIntoTodos_Cap(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "- [ ] task number "+string(rune('a'+i)))
	}
	todos := parseProposedFixIntoTodos(strings.Join(lines, "\n"))
	if len(todos) != maxPendingTodos {
		t.Fatalf("got %d todos, want cap %d", len(todos), maxPendingTodos)
	}
}

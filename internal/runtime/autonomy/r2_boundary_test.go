package autonomy

// R2 — the targetless-repair boundary, pinned deterministically.
//
// The R2 live benchmark (test/live_r2) runs a real model against a one-file
// workspace with an objective that NAMES NO FILE. It is BLOCKED at
// discovery→inspection: discovery observes index.html and the runtime then
// refuses to bind a target it was never given. The live run needs Ollama; this
// test pins the same boundary with no model at all, so the shape is guarded in
// the always-run suite.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// TestR2_TargetlessRepairObservesButDoesNotDispatch asserts that a mutating
// objective which names no file and declares no artifact kind:
//
//   - DISCOVERS its candidate (the park carries index.html as the option);
//   - binds NOTHING from the scan (I13: evidence is not authority);
//   - dispatches NO provider call;
//   - mutates NOTHING; and
//   - parks at a clarification boundary.
//
// The first three are the block; the last two are why it is safe.
func TestR2_TargetlessRepairObservesButDoesNotDispatch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<h1>Helo</h1>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{}
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)

	objective := `inspect this project, find the incorrect greeting, fix it to "Hello", and verify the result.`
	if _, err := d.Run(context.Background(), objective); err != nil {
		t.Fatalf("driver.Run: %v", err)
	}

	// The block: nothing was bound, so nothing may be dispatched.
	if mock.calls() != 0 {
		t.Fatalf("the runtime dispatched %d provider call(s) for a target it never bound", mock.calls())
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human — a targetless repair must park, never guess", d.State())
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryClarify {
		t.Fatalf("boundary = %+v, want a clarification carrying the discovered candidate", b)
	}
	// Discovery actually ran: the candidate is the disambiguation evidence.
	if !contains(b.Options, "index.html") {
		t.Fatalf("boundary options = %v, want the discovered candidate index.html", b.Options)
	}

	// The workspace is untouched.
	body, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "<h1>Helo</h1>\n" {
		t.Fatalf("the runtime mutated the workspace for an unresolved target: %q", body)
	}
}

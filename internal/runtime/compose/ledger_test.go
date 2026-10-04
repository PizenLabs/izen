package compose

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestWireBindsTheDurableExecutionLedgerAndSurvivesRestart is the composition
// root's half of the production wiring: Wire opens the journal at the resolved
// workspace root, and a task left unfinished by a previous process is
// reconstructed on the next boot instead of vanishing.
func TestWireBindsTheDurableExecutionLedgerAndSurvivesRestart(t *testing.T) {
	root := t.TempDir()

	app, err := Wire(WithRoot(root))
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	store := app.Ledger()
	if store == nil {
		t.Fatal("Wire left the durable execution ledger unwired")
	}
	wantPath := filepath.Join(root, ".izen", "runtime", "ledger.ndjson")
	if store.LedgerPath() != wantPath {
		t.Fatalf("ledger path = %q, want %q", store.LedgerPath(), wantPath)
	}
	if store.WorkDir() != filepath.Clean(root) {
		t.Fatalf("ledger workspace = %q, want %q", store.WorkDir(), filepath.Clean(root))
	}
	// The driver must be wired onto the SAME store, not a second one: there is
	if _, err := store.CreateTaskWithProvenance("obj-unfinished", "patch note.txt", []string{"note.txt"}, 1); err != nil {
		t.Fatalf("create task: %v", err)
	}
	app.Close()

	// A restart: the journal is reopened and the unfinished work is offered back.
	restarted, err := Wire(WithRoot(root))
	if err != nil {
		t.Fatalf("second Wire: %v", err)
	}
	defer restarted.Close()
	task, ok := restarted.InterruptedTask()
	if !ok {
		t.Fatal("a restart found no unfinished task — the interrupted objective was silently forgotten")
	}
	if task.ID != "obj-unfinished" || task.Intent != "patch note.txt" {
		t.Fatalf("recovered task = %+v, want the unfinished 'obj-unfinished'", task)
	}
	if task.ScopeProvenance != 1 {
		t.Fatalf("recovered provenance = %v, want the $prompt grant", task.ScopeProvenance)
	}
	if !strings.HasSuffix(restarted.Ledger().LedgerPath(), "ledger.ndjson") {
		t.Fatalf("restarted ledger path = %q", restarted.Ledger().LedgerPath())
	}
}

// TestWireWithoutRootLeavesNoLedger pins the harness-mode boundary: with no
// workspace root there is nothing to be a truth about, and the ledger stays
// nil rather than pointing at the process's current directory.
func TestWireWithoutRootLeavesNoLedger(t *testing.T) {
	app, err := Wire()
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	defer app.Close()
	if app.Ledger() != nil {
		t.Fatalf("harness mode wired a ledger at %q", app.Ledger().LedgerPath())
	}
	if _, ok := app.InterruptedTask(); ok {
		t.Fatal("harness mode reported an interrupted task with no ledger")
	}
}

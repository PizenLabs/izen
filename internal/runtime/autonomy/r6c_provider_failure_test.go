package autonomy

// ── R6-C: PROVIDER FAILURE AT THE DRIVER BOUNDARY (case C1) ─────────────────
//
// A provider that fails at the transport boundary must terminate the run as a
// non-success WITHOUT completing the objective and WITHOUT mutating the
// workspace. Provider success is not objective success; provider failure is not
// objective failure either — it is a bounded recovery input. What must never
// happen is a false PROVEN.

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
)

// r6cErrorProvider always fails at the provider boundary. It models a dropped
// connection / refused request, not a model answer.
type r6cErrorProvider struct {
	err   error
	calls int
}

func (p *r6cErrorProvider) Name() string { return "r6c-error" }

func (p *r6cErrorProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	p.calls++
	return nil, p.err
}

func (p *r6cErrorProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	p.calls++
	return nil, p.err
}

func TestR6C_ProviderErrorNeverCompletesAndNeverMutates(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	prov := &r6cErrorProvider{err: errors.New("connection reset by peer")}
	x := testExecutor(t, root, prov, nil)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, nil)

	term, err := d.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run returned an error instead of a typed termination: %v", err)
	}
	// A provider failure may terminate (aborted/unsubstantiated) OR park at a
	// human boundary; a parked loop has no LoopTermination. Either way it must
	// never be a completion.
	if term != nil && term.State == autonomy.RuntimeCompleted {
		t.Fatalf("a failing provider terminated the run as completed: %+v", term)
	}
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatal("a failing provider left the loop in completed")
	}
	if !d.State().IsTerminal() && d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("provider failure left an unexpected live state: %s", d.State())
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("provider failure mutated the workspace: %q", got)
	}
	if prov.calls == 0 {
		t.Fatal("the provider was never attempted")
	}
}

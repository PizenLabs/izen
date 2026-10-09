package autonomy

// ── REGRESSION: the command surface is carried, not dropped ──────────────────
//
// OBSERVED (black-box trace): a `$prompt` run reported
// `command_mode=(not-carried)` even though the directive was the authority that
// authorized the run.
//
// CAUSAL: intentAxes read the composition-time preflight policy field
// (`d.subcommand`), which the per-input `SetScope` binding never touched. The
// authoritative per-run directive lives on the loop request (`d.req.Scope`), and
// it IS carried into the loop.
//
// These tests bind the directive the way the UI does and pin the axis.

import (
	"testing"

	"github.com/PizenLabs/izen/internal/execution"
)

func TestIntentAxes_CommandSurfaceIsCarried(t *testing.T) {
	root := t.TempDir()
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), nil)
	d := NewDriver(adapter, nil)

	for _, surface := range []string{"$prompt", "$hot"} {
		d.SetScope(surface)
		_, mode, _, _ := d.intentAxes()
		if mode != surface {
			t.Fatalf("command_mode = %q after SetScope(%q), want %q", mode, surface, surface)
		}
	}

	// An unbound run still reports the absence explicitly rather than guessing.
	d2 := NewDriver(adapter, nil)
	if _, mode, _, _ := d2.intentAxes(); mode != NotCarried {
		t.Fatalf("command_mode = %q for an unbound run, want %q", mode, NotCarried)
	}
}

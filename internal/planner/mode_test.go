package planner

import "testing"

// TestExecutionModeForPolicy proves the strict greenfield guarantee: a rewrite
// context policy, or an intent that does not preserve the workspace, ALWAYS
// forces one-shot Greenfield Generation Mode regardless of any other signal.
func TestExecutionModeForPolicy(t *testing.T) {
	t.Run("rewrite policy forces greenfield even with preserved intent", func(t *testing.T) {
		pv := true
		if got := ExecutionModeForPolicy(true, &pv); got != ModeGreenfield {
			t.Errorf("ExecutionModeForPolicy(true, &true) = %s, want greenfield", got)
		}
		if got := ExecutionModeForPolicy(true, nil); got != ModeGreenfield {
			t.Errorf("ExecutionModeForPolicy(true, nil) = %s, want greenfield", got)
		}
	})

	t.Run("non-preserving intent forces greenfield", func(t *testing.T) {
		pv := false
		if got := ExecutionModeForPolicy(false, &pv); got != ModeGreenfield {
			t.Errorf("ExecutionModeForPolicy(false, &false) = %s, want greenfield", got)
		}
	})

	t.Run("preserving intent falls back to brownfield", func(t *testing.T) {
		pv := true
		if got := ExecutionModeForPolicy(false, &pv); got != ModeBrownfield {
			t.Errorf("ExecutionModeForPolicy(false, &true) = %s, want brownfield", got)
		}
		if got := ExecutionModeForPolicy(false, nil); got != ModeBrownfield {
			t.Errorf("ExecutionModeForPolicy(false, nil) = %s, want brownfield", got)
		}
	})
}

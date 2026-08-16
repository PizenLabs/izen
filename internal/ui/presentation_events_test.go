package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/modes"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
)

func TestHandlePresentationEventProjection(t *testing.T) {
	tests := []struct {
		name string
		ev   appruntime.PresentationEvent
		want string
	}{
		{
			"command received",
			appruntime.PresentationEvent{Type: appruntime.PresentationCommandReceived, Severity: appruntime.SeverityInfo, Summary: "command received: submit_prompt"},
			"command received: submit_prompt",
		},
		{
			"plan staged",
			appruntime.PresentationEvent{Type: appruntime.PresentationPlanStaged, Severity: appruntime.SeverityInfo, Summary: "plan staged: 3 task(s) in plan"},
			"plan staged: 3 task(s) in plan",
		},
		{
			"patch applied (success)",
			appruntime.PresentationEvent{Type: appruntime.PresentationPatchApplied, Severity: appruntime.SeveritySuccess, Summary: "patch applied: x.go (+12 -4)", Target: "x.go"},
			"patch applied: x.go (+12 -4)",
		},
		{
			"execution failed",
			appruntime.PresentationEvent{Type: appruntime.PresentationExecutionFailed, Severity: appruntime.SeverityError, Summary: "execution failed in build: boom"},
			"execution failed in build: boom",
		},
		// ApprovalRequested / PhaseChanged / IntentClassified are deliberately
		// NOT projected here: they are deduplicated (Rule 4) — the UI renders
		// the raw domain event, which carries the structured payload the
		// viewState/approval projections need.
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &model{}
			m.handlePresentationEvent(tc.ev)

			if len(m.records) < 1 {
				t.Fatalf("got %d records, want at least 1", len(m.records))
			}
			got := ansi.Strip(m.records[0].text)
			if got != tc.want {
				t.Errorf("record = %q, want %q", got, tc.want)
			}
			if m.records[0].role != roleActivity {
				t.Errorf("record role = %v, want roleActivity", m.records[0].role)
			}
		})
	}
}

func TestHandlePresentationEventTargetLine(t *testing.T) {
	m := &model{}
	m.handlePresentationEvent(appruntime.PresentationEvent{
		Type:     appruntime.PresentationPatchApplied,
		Severity: appruntime.SeveritySuccess,
		Summary:  "patch applied: x.go (+1 -1)",
		Target:   "x.go",
	})
	if len(m.records) != 2 {
		t.Fatalf("got %d records, want 2 (summary + target)", len(m.records))
	}
	if got := ansi.Strip(m.records[1].text); !strings.Contains(got, "x.go") {
		t.Errorf("target line = %q, want it to contain x.go", got)
	}
}

func TestRuntimeBridgeNilSafe(t *testing.T) {
	// A model with no wired runtime must return no-op commands (nil) so the
	// rich UI path is unaffected in harnesses.
	m := &model{}
	if cmd := m.runRuntimeCmd(nil); cmd != nil {
		t.Fatalf("runRuntimeCmd(nil) = %v, want nil", cmd)
	}
	if cmd := m.runtimeSubmitCmd("hello"); cmd != nil {
		t.Fatalf("runtimeSubmitCmd without runtime = %v, want nil", cmd)
	}
	if cmd := m.runtimeSwitchCmd(modes.ModePlan); cmd != nil {
		t.Fatalf("runtimeSwitchCmd without runtime = %v, want nil", cmd)
	}
	if cmd := m.runtimeCancelCmd("stop"); cmd != nil {
		t.Fatalf("runtimeCancelCmd without runtime = %v, want nil", cmd)
	}
	if cmd := m.runtimeApproveCmd("p1"); cmd != nil {
		t.Fatalf("runtimeApproveCmd without runtime = %v, want nil", cmd)
	}
	if cmd := m.runtimeRejectCmd("p1", "no"); cmd != nil {
		t.Fatalf("runtimeRejectCmd without runtime = %v, want nil", cmd)
	}
}

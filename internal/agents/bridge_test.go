package agents

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/domain"
)

func TestInjectObjectiveContext(t *testing.T) {
	obj := &domain.Objective{
		ID:             "obj-1",
		RawIntent:      "Implement objective header",
		CurrentStatus:  domain.ObjectivePlanned,
		HumanConfirmed: true,
		Scope: domain.ObjectiveScope{
			Files:   []string{"internal/ui/view.go"},
			Symbols: []string{"renderFocusHeader"},
		},
		TokenBudget: domain.ObjectiveTokenBudget{
			CurrentWeight: 300,
			Threshold:     1000,
		},
	}
	got := InjectObjectiveContext("run build mode action", obj)
	if !strings.Contains(got, "### ACTIVE OBJECTIVE") {
		t.Fatalf("expected active objective frame, got: %s", got)
	}
}

func TestInjectObjectiveContextSkipsUnconfirmedObjective(t *testing.T) {
	obj := &domain.Objective{
		RawIntent:      "Implement objective header",
		CurrentStatus:  domain.ObjectivePlanned,
		HumanConfirmed: false,
	}
	in := "run build mode action"
	got := InjectObjectiveContext(in, obj)
	if got != in {
		t.Fatalf("expected unchanged content for unconfirmed objective, got: %s", got)
	}
}

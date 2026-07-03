package agents

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/domain"
)

func InjectObjectiveContext(content string, objective *domain.Objective) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return trimmed
	}
	if objective == nil || strings.TrimSpace(objective.RawIntent) == "" {
		return trimmed
	}
	if !objective.HumanConfirmed || objective.CurrentStatus == domain.ObjectiveAnalyzing {
		return trimmed
	}

	budgetStatus := "OK"
	if objective.TokenBudget.RequiresApproval {
		budgetStatus = "WARNING"
	}

	frame := fmt.Sprintf(
		"### ACTIVE OBJECTIVE\nID: %s\nIntent: %s\nStatus: %s\nScope Files: %d\nScope Symbols: %d\nBudget: %s (%d/%d)\n",
		objective.ID,
		objective.RawIntent,
		objective.CurrentStatus,
		len(objective.Scope.Files),
		len(objective.Scope.Symbols),
		budgetStatus,
		objective.TokenBudget.CurrentWeight,
		objective.TokenBudget.Threshold,
	)

	if strings.HasPrefix(trimmed, "### ACTIVE OBJECTIVE\n") {
		return trimmed
	}
	return frame + "\n" + trimmed
}

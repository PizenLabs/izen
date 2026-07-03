package domain

import (
	"fmt"
	"strings"
	"time"
)

type ObjectiveStatus string

const (
	ObjectiveIdle      ObjectiveStatus = "idle"
	ObjectiveAnalyzing ObjectiveStatus = "analyzing"
	ObjectivePlanned   ObjectiveStatus = "planned"
	ObjectiveExecuting ObjectiveStatus = "executing"
)

type ObjectiveScope struct {
	Files    []string `json:"files,omitempty"`
	Symbols  []string `json:"symbols,omitempty"`
	ASTNodes []string `json:"ast_nodes,omitempty"`
}

type ObjectiveTokenBudget struct {
	CurrentWeight    int  `json:"current_weight"`
	Threshold        int  `json:"threshold"`
	RequiresApproval bool `json:"requires_approval"`
}

type Objective struct {
	ID             string               `json:"id"`
	RawIntent      string               `json:"raw_intent"`
	CurrentStatus  ObjectiveStatus      `json:"current_status"`
	Scope          ObjectiveScope       `json:"scope"`
	TokenBudget    ObjectiveTokenBudget `json:"token_budget"`
	HumanConfirmed bool                 `json:"human_confirmed"`
	Telemetry      []string             `json:"telemetry,omitempty"`
}

func NewObjective(rawIntent string) *Objective {
	normalized := strings.TrimSpace(rawIntent)
	return &Objective{
		ID:             fmt.Sprintf("obj-%d", time.Now().UnixNano()),
		RawIntent:      normalized,
		CurrentStatus:  ObjectiveIdle,
		HumanConfirmed: false,
	}
}

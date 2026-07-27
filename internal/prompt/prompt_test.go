package prompt

import (
	"strings"
	"testing"
)

func TestAskPromptHandoffContract_NoRoleplay(t *testing.T) {
	contract := AskPromptHandoffContract()

	forbidden := []string{
		"Senior DevOps",
		"Senior Architect",
		"Forensic Handoff",
		"Symptoms / Motivation",
		"FORENSIC HANDOFF VECTOR",
		"Smart Analysis",
		"Tradeoff",
	}
	for _, word := range forbidden {
		if strings.Contains(contract, word) {
			t.Errorf("AskPromptHandoffContract contains forbidden text %q", word)
		}
	}

	required := []string{
		"Goal:",
		"Targets:",
		"Steps:",
	}
	for _, word := range required {
		if !strings.Contains(contract, word) {
			t.Errorf("AskPromptHandoffContract missing required text %q", word)
		}
	}
}

func TestPlanContract_NoRoleplay(t *testing.T) {
	contract := PlanContract()

	forbidden := []string{
		"Senior Principal Structural Architect",
		"Forensic Ledger",
		"Forensic data compressor",
	}
	for _, word := range forbidden {
		if strings.Contains(contract, word) {
			t.Errorf("PlanContract contains forbidden text %q", word)
		}
	}
}

func TestCompactPlanContract_NoRoleplay(t *testing.T) {
	contract := CompactPlanContract()

	forbidden := []string{
		"Execution Mapper",
		"Senior Architect",
	}
	for _, word := range forbidden {
		if strings.Contains(contract, word) {
			t.Errorf("CompactPlanContract contains forbidden text %q", word)
		}
	}
}

func TestInvestigateContract_NoRoleplay(t *testing.T) {
	contract := InvestigateContract()

	forbidden := []string{
		"forensic data compressor",
		"Forensic Ledger",
	}
	for _, word := range forbidden {
		if strings.Contains(contract, word) {
			t.Errorf("InvestigateContract contains forbidden text %q", word)
		}
	}
}

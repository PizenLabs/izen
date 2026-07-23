package review

import (
	"strings"
)

type RiskCategory string

const (
	RiskDeterministic RiskCategory = "Deterministic"
	RiskBehavioral    RiskCategory = "Behavioral"
	RiskStructural    RiskCategory = "Structural"
	RiskEnvironmental RiskCategory = "Environmental"
	RiskSpeculative   RiskCategory = "Speculative"
)

type RiskClassification struct {
	Category   RiskCategory
	Confidence EvidenceConfidence
	Rationale  string
}

type InputRisk struct {
	File        string
	Line        int
	Category    string
	RuleID      string
	Severity    string
	Code        string
	Description string
}

func ClassifyRisk(risk InputRisk) RiskClassification {
	cat := strings.ToLower(risk.Category)
	rule := strings.ToLower(risk.RuleID)
	severity := strings.ToLower(risk.Severity)

	switch {
	case cat == "hardcoded_secret" || cat == "secret":
		return RiskClassification{
			Category:   RiskDeterministic,
			Confidence: ConfHigh,
			Rationale:  "Secret detection is deterministic — can verify via grep/pattern check",
		}

	case rule == "sec-sql-001" || rule == "sec-cmd-001" || rule == "go-panic-001" || rule == "go-panic-002":
		return RiskClassification{
			Category:   RiskDeterministic,
			Confidence: ConfHigh,
			Rationale:  "AST-detectable pattern with zero false-positive rate for generated tests",
		}

	case cat == "goroutine" || cat == "defer" || cat == "lock_without_defer" || rule == "go-lock-001" || rule == "go-def-001":
		return RiskClassification{
			Category:   RiskBehavioral,
			Confidence: ConfMedium,
			Rationale:  "Concurrency/synchronization patterns require runtime scenario verification",
		}

	case cat == "sql_injection" || cat == "os_command" || cat == "unsized_read":
		return RiskClassification{
			Category:   RiskBehavioral,
			Confidence: ConfMedium,
			Rationale:  "Input-dependent behavior — needs integration/edge-case test",
		}

	case cat == "serialization":
		return RiskClassification{
			Category:   RiskBehavioral,
			Confidence: ConfMedium,
			Rationale:  "Serialization behavior depends on input size and format",
		}

	case rule == "sec-http-001" || cat == "exposed_endpoint":
		return RiskClassification{
			Category:   RiskBehavioral,
			Confidence: ConfMedium,
			Rationale:  "HTTP endpoint behavior requires runtime integration check",
		}

	case cat == "no_error_return" || rule == "go-func-001":
		return RiskClassification{
			Category:   RiskStructural,
			Confidence: ConfHigh,
			Rationale:  "Function signature structural issue — AST check sufficient",
		}

	case cat == "fatal_log" || rule == "go-fatal-001" || cat == "os_exit" || rule == "go-exit-001":
		return RiskClassification{
			Category:   RiskBehavioral,
			Confidence: ConfMedium,
			Rationale:  "Exit/log.Fatal behavior requires runtime verification in isolation",
		}

	case cat == "side_effect":
		return RiskClassification{
			Category:   RiskEnvironmental,
			Confidence: ConfLow,
			Rationale:  "Side-effect behavior depends on sandbox environment",
		}

	case cat == "debug_output" || cat == "unused_result" || rule == "cq-print-001" || rule == "cq-blank-001":
		return RiskClassification{
			Category:   RiskSpeculative,
			Confidence: ConfLow,
			Rationale:  "Code quality concern — report only, no automated test",
		}

	case cat == "code_quality" || rule == "cq-todo-001":
		return RiskClassification{
			Category:   RiskSpeculative,
			Confidence: ConfLow,
			Rationale:  "Code quality marker — report only, no automated test",
		}

	case severity == "info":
		return RiskClassification{
			Category:   RiskSpeculative,
			Confidence: ConfLow,
			Rationale:  "Informational finding — report only, no automated test",
		}

	default:
		return RiskClassification{
			Category:   RiskSpeculative,
			Confidence: ConfSpeculative,
			Rationale:  "Uncategorized risk — report only, no automated test",
		}
	}
}

func ShouldGenerateTest(classification RiskClassification) bool {
	return classification.Category == RiskDeterministic
}

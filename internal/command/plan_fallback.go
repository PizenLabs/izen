package command

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/modes/plan"
)

// FallbackPlanTarget carries the minimal structural information needed to
// generate a deterministic 1-step execution plan without any LLM or lx
// dependency. It is populated by the context-reducer or directly by the
// investigate engine when the LLM synthesis path fails.
type FallbackPlanTarget struct {
	// File is the target file path relative to project root.
	File string
	// Line is the target line number (0 if unknown).
	Line int
	// Description is a one-line human-readable description of what to do.
	Description string
	// TaskType is the plan.Task type: "FILE_MUTATE", "SHELL_EXEC", or "GIT_ACTION".
	TaskType string
	// ShellCommand is the exact shell command when TaskType is SHELL_EXEC.
	ShellCommand string
}

// GenerateFallbackPlan produces a valid 1-step ExecutionPlan ([]plan.Task)
// from a FallbackPlanTarget. It bypasses LLM synthesis completely and is
// guaranteed to pass ValidateAllTasks. The generated task is always marked
// IsHardcoded so downstream validation filters (FilterUnsolicitedPkgFiles,
// FilterUndefinedSymbolShellExec) preserve it.
//
// When the target is empty or invalid, a generic shell fallback is returned
// so the user never sees an empty/error plan from the fallback path.
func GenerateFallbackPlan(target FallbackPlanTarget) []plan.Task {
	target.Description = strings.TrimSpace(target.Description)
	target.File = strings.TrimSpace(target.File)

	// If no target is specified, generate a generic go test verification step.
	if target.File == "" && target.ShellCommand == "" {
		if target.Description != "" {
			return []plan.Task{{
				StepNum:     1,
				IsDone:      false,
				Status:      "idle",
				Type:        "SHELL_EXEC",
				Target:      "go test ./...",
				Description: target.Description,
				Rationale:   "Fallback: no specific target — run full test suite to surface next diagnostic.",
				Solution:    "Test suite executed; failures will guide subsequent steps.",
				IsHardcoded: true,
			}}
		}
		return []plan.Task{{
			StepNum:     1,
			IsDone:      false,
			Status:      "idle",
			Type:        "SHELL_EXEC",
			Target:      "go test ./...",
			Description: "Run full test suite to verify workspace state.",
			Rationale:   "Fallback: no specific target — run full test suite.",
			Solution:    "Workspace state verified.",
			IsHardcoded: true,
		}}
	}

	// Shell command tasks.
	if target.TaskType == "SHELL_EXEC" || target.ShellCommand != "" {
		cmd := strings.TrimSpace(target.ShellCommand)
		if cmd == "" {
			cmd = "go mod tidy"
		}
		desc := target.Description
		if desc == "" {
			desc = fmt.Sprintf("Run shell command: %s", cmd)
		}
		return []plan.Task{{
			StepNum:     1,
			IsDone:      false,
			Status:      "idle",
			Type:        "SHELL_EXEC",
			Target:      cmd,
			Description: desc,
			Rationale:   fmt.Sprintf("Deterministic fallback: execute %q to resolve the issue.", cmd),
			Solution:    fmt.Sprintf("Command %q completed successfully.", cmd),
			IsHardcoded: true,
		}}
	}

	// FILE_MUTATE task.
	desc := target.Description
	if desc == "" {
		if target.Line > 0 {
			desc = fmt.Sprintf("Edit %s at line %d", target.File, target.Line)
		} else {
			desc = fmt.Sprintf("Edit %s", target.File)
		}
	}

	var rationale string
	if target.Line > 0 {
		rationale = fmt.Sprintf("Target file %s at line %d requires a mutation.", target.File, target.Line)
	} else {
		rationale = fmt.Sprintf("Target file %s requires a mutation.", target.File)
	}

	task := plan.Task{
		StepNum:     1,
		IsDone:      false,
		Status:      "idle",
		Type:        "FILE_MUTATE",
		Target:      target.File,
		Description: desc,
		Rationale:   rationale,
		Solution:    fmt.Sprintf("File %s mutated successfully.", target.File),
		IsHardcoded: true,
	}

	return []plan.Task{task}
}

// ParseTargetFromSanitizedLedger extracts a FallbackPlanTarget from the
// context-reduced ledger content produced by SanitizeForensicLedger. It
// scans for TGT:, FILE:, and DONE: lines to determine the structural target.
// Returns an empty target (zero-value) when nothing can be extracted.
func ParseTargetFromSanitizedLedger(sanitized string) FallbackPlanTarget {
	if sanitized == "" {
		return FallbackPlanTarget{}
	}

	var target FallbackPlanTarget

	for _, line := range strings.Split(sanitized, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "TGT:"):
			rest := strings.TrimSpace(trimmed[4:])
			if idx := strings.Index(rest, " "); idx > 0 {
				target.File = rest[:idx]
				target.Description = rest[idx+1:]
			} else {
				target.File = rest
			}
			target.TaskType = "FILE_MUTATE"

		case strings.HasPrefix(trimmed, "FILE:"):
			target.File = strings.TrimSpace(trimmed[5:])
			target.TaskType = "FILE_MUTATE"

		case strings.HasPrefix(trimmed, "DONE:"):
			rest := strings.TrimSpace(trimmed[5:])
			if target.Description == "" {
				target.Description = rest
			}
			if strings.Contains(strings.ToLower(rest), "shell") ||
				strings.Contains(strings.ToLower(rest), "command") ||
				strings.Contains(strings.ToLower(rest), "go get") ||
				strings.Contains(strings.ToLower(rest), "go mod") {
				target.TaskType = "SHELL_EXEC"
				target.ShellCommand = extractShellCommand(rest)
			}

		case strings.HasPrefix(trimmed, "ROOT:"):
			if target.Description == "" {
				target.Description = strings.TrimSpace(trimmed[5:])
			}

		case strings.HasPrefix(strings.ToLower(trimmed), "conclusion:"):
			rest := strings.TrimSpace(trimmed[11:])
			if target.Description == "" {
				target.Description = rest
			}
			if strings.Contains(strings.ToLower(rest), "go get") {
				target.TaskType = "SHELL_EXEC"
				target.ShellCommand = extractShellCommand(rest)
			}

		case strings.HasPrefix(trimmed, "[PKT-"):
			target.Description = trimmed
		}
	}

	// Infer FILE_MUTATE if we have a file but no type.
	if target.File != "" && target.TaskType == "" {
		target.TaskType = "FILE_MUTATE"
	}

	return target
}

// extractShellCommand attempts to extract a concrete shell command from a
// conclusion or description string. Looks for known command prefixes.
func extractShellCommand(s string) string {
	s = strings.TrimSpace(s)
	knownCommands := []string{
		"go get ",
		"go mod tidy",
		"go test ",
		"go build ",
		"go install ",
		"go work ",
		"npm install ",
		"npm run ",
		"pip install ",
		"cargo build",
		"cargo test",
		"make ",
		"git clone ",
	}

	lower := strings.ToLower(s)
	for _, cmd := range knownCommands {
		if idx := strings.Index(lower, cmd); idx >= 0 {
			return s[idx : idx+len(cmd)]
		}
	}

	return ""
}

package control

import (
	"fmt"
	"strings"
)

// ScopeViolationError indicates that a task target is not in the allowed file tree.
type ScopeViolationError struct {
	Target       string
	AllowedFiles []string
}

func (e *ScopeViolationError) Error() string {
	return fmt.Sprintf("SCOPE_VIOLATION: target %q is not in ALLOWED_FILE_TREE (allowed: %v)", e.Target, e.AllowedFiles)
}

// TargetString returns a compact representation of the violation for log messages.
func (e *ScopeViolationError) TargetString() string { return e.Target }

// AllowedString returns the allowed files list as a formatted string.
func (e *ScopeViolationError) AllowedString() string {
	return fmt.Sprintf("%v", e.AllowedFiles)
}

// TaskTarget is a minimal representation of a plan task for scope validation.
type TaskTarget struct {
	Target string
	Type   string
}

// ValidateStagedPlan checks that no FILE_MUTATE/ATOMIC_REPLACE/DIFF_PATCH task
// targets a file outside the allowed file tree. go.mod and go.sum are implicitly
// allowed. SHELL_EXEC and GIT_ACTION tasks are not validated against the tree.
func ValidateStagedPlan(tasks []TaskTarget, allowedFiles []string) error {
	allowedSet := make(map[string]bool, len(allowedFiles))
	for _, f := range allowedFiles {
		allowedSet[f] = true
	}

	allowedSet["go.mod"] = true
	allowedSet["go.sum"] = true

	for _, t := range tasks {
		ttype := strings.ToUpper(strings.TrimSpace(t.Type))
		if ttype != "FILE_MUTATE" && ttype != "ATOMIC_REPLACE" && ttype != "DIFF_PATCH" {
			continue
		}
		target := strings.TrimSpace(t.Target)
		if target == "" {
			continue
		}
		if allowedSet[target] {
			continue
		}
		return &ScopeViolationError{
			Target:       target,
			AllowedFiles: allowedFiles,
		}
	}
	return nil
}

// FormatScopeGuardLogLine returns a dimmed system log line for scope guard
// rejections, following the format specified in the technical requirements:
//
//	[ScopeGuard] Rejected target <target> - Not in workspace tree
func FormatScopeGuardLogLine(target string) string {
	return fmt.Sprintf("[ScopeGuard] Rejected target %s - Not in workspace tree", target)
}

// FormatRepromptInstruction returns the re-prompt instruction block that tells
// the model the EXACT workspace file list when a scope violation was detected.
// This is injected into the LLM prompt on retry so the model can correct itself.
func FormatRepromptInstruction(allowedFiles []string) string {
	if len(allowedFiles) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[SYSTEM: SCOPE VIOLATION — FILE RESTRICTION ACTIVATED]\n")
	b.WriteString("The previous plan was rejected because it targeted a file outside the workspace.\n")
	b.WriteString("You MUST ONLY target files in this exact list:\n")
	for _, f := range allowedFiles {
		fmt.Fprintf(&b, "  - %s\n", f)
	}
	b.WriteString("Do NOT create, modify, or reference any file not in this list.\n")
	b.WriteString("If no file in the list matches the task, generate SHELL_EXEC tasks only.\n")
	return b.String()
}

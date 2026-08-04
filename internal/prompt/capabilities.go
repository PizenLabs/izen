package prompt

import "strings"

// activeCapabilities is the package-level workspace-capability header injected
// into every composed system prompt. Bootstrap code calls
// SetWorkspaceCapabilities with the Layer 1 capability header so models are
// told exactly which toolchain commands exist for the workspace — and which do
// not — eliminating tech-stack hallucinations (e.g. assuming Go for a static
// HTML/JS project).
var activeCapabilities = ""

// SetWorkspaceCapabilities sets the active workspace-capability header and
// returns the previously active one. An empty header clears the injection.
func SetWorkspaceCapabilities(header string) string {
	prev := activeCapabilities
	activeCapabilities = header
	return prev
}

// ActiveWorkspaceCapabilities returns the currently active header.
func ActiveWorkspaceCapabilities() string { return activeCapabilities }

// ApplyWorkspaceCapabilities injects the active capabilities header into a
// system prompt. Prompts that already carry it, or that have no header set,
// are returned unchanged so the injection is idempotent.
func ApplyWorkspaceCapabilities(systemPrompt string) string {
	if strings.TrimSpace(activeCapabilities) == "" {
		return systemPrompt
	}
	if strings.Contains(systemPrompt, "WORKSPACE CAPABILITIES") {
		return systemPrompt
	}
	return strings.TrimRight(systemPrompt, "\n") + "\n\n" + activeCapabilities
}

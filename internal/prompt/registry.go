package prompt

import (
	"fmt"
	"runtime"
	"strings"
)

// RuntimeFacts are externally supplied facts about the runtime environment.
// The registry's only responsibility is composition:
//
//	Identity Header + Common Contract + Mode Contract + Environment Context
type RuntimeFacts struct {
	// Username is the collaborating engineer's identity. Empty falls back to "Developer".
	Username string
	// HostOS is the host operating system (runtime.GOOS). When set, anchors
	// command generation to the real environment so the model never hallucinate
	// OS-specific commands (e.g. `apt-get` on macOS). Empty → constraint omitted.
	HostOS string
}

// Compose assembles the full system prompt:
//
//	Identity Header → Public Handle Context → Common Contract → Mode Contract → Environment Context (optional) → Style Directive (active policy)
//
// Each section lives in exactly one place; nothing is duplicated.
func Compose(modeContract string, facts RuntimeFacts) string {
	var b strings.Builder

	b.WriteString("You are IZEN, a fast CLI coding companion.\n\n")

	// Public Handle context — safe identity injection for natural LLM rapport.
	if facts.Username != "" {
		b.WriteString("Active CLI Workspace Context:\n")
		fmt.Fprintf(&b, "- Developer Handle: %s (public session handle)\n\n", facts.Username)
		b.WriteString("Instructions:\n")
		fmt.Fprintf(&b, "In your responses and explanations, feel free to naturally address the developer by their handle (%s) when appropriate to keep the dialogue friendly and personal.\n\n", facts.Username)
	}

	b.WriteString(CommonContract())
	b.WriteString("\n\n")
	b.WriteString(modeContract)

	if facts.HostOS != "" {
		b.WriteString("\n\n")
		b.WriteString(EnvironmentContextForOS(facts.HostOS))
	}

	// Response style directive — injected dynamically from the active policy
	// so every mode honors the engineer's configured verbosity.
	return ApplyStyle(b.String(), activeStyle)
}

// AskSystemPrompt returns the composed system prompt for ask mode.
func AskSystemPrompt(username string) string {
	if username == "" {
		username = "Developer"
	}
	return Compose(AskContract(), RuntimeFacts{Username: username, HostOS: runtime.GOOS})
}

// BuildSystemPrompt returns the composed system prompt for build mode.
func BuildSystemPrompt(username string) string {
	if username == "" {
		username = "Developer"
	}
	return Compose(BuildContract(), RuntimeFacts{Username: username, HostOS: runtime.GOOS})
}

// PlanSystemPrompt returns the composed system prompt for plan mode.
func PlanSystemPrompt(username string) string {
	if username == "" {
		username = "Developer"
	}
	return Compose(PlanContract(), RuntimeFacts{Username: username, HostOS: runtime.GOOS})
}

// CompactPlanSystemPrompt returns the compact 3-bullet checklist prompt for
// LOW and MEDIUM complexity tasks. Omits verbose plan prose.
func CompactPlanSystemPrompt(username string) string {
	if username == "" {
		username = "Developer"
	}
	return Compose(CompactPlanContract(), RuntimeFacts{Username: username, HostOS: runtime.GOOS})
}

// SelectPlanSystemPrompt returns the appropriate plan system prompt based on
// the task objective's heuristic complexity and the presence of an explicit
// high-intent flag. High-complexity tasks (>8/10) or explicit --high flags
// get the full plan contract; everything else gets the compact
// 3-bullet checklist format.
func SelectPlanSystemPrompt(objective string, hasHighFlag bool, username string) string {
	complexity := AssessComplexity(objective)
	if IsHighComplexity(complexity, hasHighFlag) {
		return PlanSystemPrompt(username)
	}
	return CompactPlanSystemPrompt(username)
}

// InvestigateSystemPrompt returns the composed system prompt for investigate mode.
func InvestigateSystemPrompt(username string) string {
	if username == "" {
		username = "Developer"
	}
	return Compose(InvestigateContract(), RuntimeFacts{Username: username, HostOS: runtime.GOOS})
}

// AskPromptHandoffSystemPrompt returns the composed system prompt for the
// $prompt handoff synthesis in ask mode. Composes via the standard pipeline
// (no manual identity duplication).
func AskPromptHandoffSystemPrompt(username string) string {
	if username == "" {
		username = "Developer"
	}
	return Compose(AskPromptHandoffContract(), RuntimeFacts{Username: username, HostOS: runtime.GOOS})
}

// ForMode returns the composed system prompt for the named mode with default identity.
func ForMode(mode string) string {
	return ForModeWithUser(mode, "Developer")
}

// ForModeWithUser returns the composed system prompt for the named mode,
// supplying the collaborating engineer's identity as a runtime fact.
func ForModeWithUser(mode, username string) string {
	switch mode {
	case "ask":
		return AskSystemPrompt(username)
	case "build":
		return BuildSystemPrompt(username)
	case "plan":
		return PlanSystemPrompt(username)
	case "investigate":
		return InvestigateSystemPrompt(username)
	case "review":
		return Compose(ReviewContract(), RuntimeFacts{Username: username, HostOS: runtime.GOOS})
	default:
		return ""
	}
}

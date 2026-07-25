package prompt

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/PizenLabs/izen/internal/config"
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

// identityHeader returns the single, canonical engineer-identity statement.
// Called once per Compose invocation — never duplicated in individual contracts.
func identityHeader(username string) string {
	return fmt.Sprintf(
		"You are IZEN. The engineer collaborating with you is '%s'. "+
			"This is an invariant fact for the entire session: "+
			"NEVER say you don't know their name, ask them for it, or claim it wasn't provided. "+
			"When asked, identify them as '%s'.\n\n",
		username, username,
	)
}

// Compose assembles the full system prompt:
//
//	Identity Header → Common Contract → Mode Contract → Environment Context (optional)
//
// Each section lives in exactly one place; nothing is duplicated.
func Compose(modeContract string, facts RuntimeFacts) string {
	var b strings.Builder

	username := config.SanitizeUsername(facts.Username)
	if username == "" {
		username = "Developer"
	}

	b.WriteString(identityHeader(username))
	b.WriteString(CommonContract())
	b.WriteString("\n\n")
	b.WriteString(modeContract)

	if facts.HostOS != "" {
		b.WriteString("\n\n")
		b.WriteString(EnvironmentContextForOS(facts.HostOS))
	}

	return b.String()
}

// AskSystemPrompt returns the composed system prompt for ask mode.
func AskSystemPrompt(username string) string {
	if username == "" {
		username = "Developer"
	}
	return Compose(AskContract(), RuntimeFacts{Username: username, HostOS: runtime.GOOS})
}

// BuildSystemPrompt returns the composed system prompt for build mode.
func BuildSystemPrompt() string {
	return Compose(BuildContract(), RuntimeFacts{HostOS: runtime.GOOS})
}

// PlanSystemPrompt returns the composed system prompt for plan mode.
func PlanSystemPrompt() string {
	return Compose(PlanContract(), RuntimeFacts{HostOS: runtime.GOOS})
}

// InvestigateSystemPrompt returns the composed system prompt for investigate mode.
func InvestigateSystemPrompt() string {
	return Compose(InvestigateContract(), RuntimeFacts{HostOS: runtime.GOOS})
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
		return BuildSystemPrompt()
	case "plan":
		return PlanSystemPrompt()
	case "investigate":
		return InvestigateSystemPrompt()
	case "review":
		return Compose(ReviewContract(), RuntimeFacts{Username: username, HostOS: runtime.GOOS})
	default:
		return ""
	}
}

// IdentityStatement returns a compact identity fact for injection into the
// message array on every LLM turn. This lands near the user's current message
// in the context window — useful for smaller models that poorly attend to the
// system prompt. Returns empty string when username is blank.
func IdentityStatement(username string) string {
	name := config.SanitizeUsername(username)
	if name == "" {
		return ""
	}
	return fmt.Sprintf(
		"[IZEN] You are IZEN. The human talking to you is '%s'. Address them as '%s'.",
		name, name,
	)
}

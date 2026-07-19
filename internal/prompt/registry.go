package prompt

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/PizenLabs/izen/internal/config"
)

// RuntimeFacts are facts supplied externally by the runtime (e.g. the engineer
// identity). The registry never contains engineering philosophy or mode logic;
// its only responsibility is composition:
//
//	Common Contract + Mode Contract + Runtime Facts
type RuntimeFacts struct {
	// Username is the collaborating engineer's identity. Empty falls back to "developer".
	Username string
	// HostOS is the host operating system (runtime.GOOS) the agent runs on.
	// When populated it anchors command generation to the real environment so
	// the model does not hallucinate OS-specific commands (e.g. `apt-get` on
	// macOS). Empty means "unknown" — the constraint is omitted.
	HostOS string
}

// Compose assembles the full system prompt for a mode from the constitutional
// common contract plus the mode's operational contract plus externally supplied
// runtime facts. The common and mode contracts are concatenated; no philosophy
// is duplicated because each lives in exactly one place.
func Compose(modeContract string, facts RuntimeFacts) string {
	var b strings.Builder
	username := config.SanitizeUsername(facts.Username)
	if username == "" {
		username = "Developer"
	}
	fmt.Fprintf(&b, "You are IZEN. The human engineer you are collaborating with is named '%s'. This is a hard, invariant fact for the entire session — you MUST remember it and NEVER say you do not know their name, never ask them to tell you their name, and never claim the name was not provided. When asked about the user's identity, answer that they are '%s'.", username, username)
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

// ForMode returns the composed system prompt for the named mode.
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

// BuildMessage composes a system prompt and a user message into a single string.
func BuildMessage(mode, userContent string) string {
	sys := ForMode(mode)
	if sys == "" {
		return userContent
	}
	return fmt.Sprintf("System: %s\n\nUser: %s", sys, userContent)
}

// IdentityStatement returns a short, standalone identity fact for injection
// directly into the message array on every LLM turn. This is separate from
// the system prompt so it lands near the user's current message in the
// model's context window — critical for smaller models that poorly attend
// to the system prompt.
func IdentityStatement(username string) string {
	name := config.SanitizeUsername(username)
	if name == "" {
		return ""
	}
	return fmt.Sprintf("Remember: you are IZEN. The human talking to you is named '%s'. Never refer to yourself as %s. Always address the human as %s.", name, name, name)
}

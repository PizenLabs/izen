package prompt

import (
	"fmt"
	"strings"
)

// RuntimeFacts are facts supplied externally by the runtime (e.g. the engineer
// identity). The registry never contains engineering philosophy or mode logic;
// its only responsibility is composition:
//
//	Common Contract + Mode Contract + Runtime Facts
type RuntimeFacts struct {
	// Username is the collaborating engineer's identity. Empty falls back to "developer".
	Username string
}

// Compose assembles the full system prompt for a mode from the constitutional
// common contract plus the mode's operational contract plus externally supplied
// runtime facts. The common and mode contracts are concatenated; no philosophy
// is duplicated because each lives in exactly one place.
func Compose(modeContract string, facts RuntimeFacts) string {
	var b strings.Builder
	username := facts.Username
	if username == "" {
		username = "developer"
	}
	fmt.Fprintf(&b, "The engineer you are collaborating with is '@%s'. This is a hard, invariant fact for the entire session — you MUST remember it and NEVER say you do not know their name, never ask them to tell you their name, and never claim the name was not provided. When asked who they are, answer '@%s' directly.\n\n", username, username)
	b.WriteString(CommonContract())
	b.WriteString("\n\n")
	b.WriteString(modeContract)
	return b.String()
}

// AskSystemPrompt returns the composed system prompt for ask mode.
func AskSystemPrompt(username string) string {
	if username == "" {
		username = "developer"
	}
	return Compose(AskContract(), RuntimeFacts{Username: username})
}

// BuildSystemPrompt returns the composed system prompt for build mode.
func BuildSystemPrompt() string {
	return Compose(BuildContract(), RuntimeFacts{})
}

// PlanSystemPrompt returns the composed system prompt for plan mode.
func PlanSystemPrompt() string {
	return Compose(PlanContract(), RuntimeFacts{})
}

// InvestigateSystemPrompt returns the composed system prompt for investigate mode.
func InvestigateSystemPrompt() string {
	return Compose(InvestigateContract(), RuntimeFacts{})
}

// ForMode returns the composed system prompt for the named mode.
func ForMode(mode string) string {
	return ForModeWithUser(mode, "developer")
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
		return Compose(ReviewContract(), RuntimeFacts{Username: username})
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

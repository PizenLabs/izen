package prompt

// CasualChatContract returns the minimal identity contract for casual /ask
// small talk (greetings, general questions). It is deliberately tiny: casual
// chat is answered with a lightweight prompt and no codebase context. The
// response verbosity is governed by the active StylePolicy rather than a
// hardcoded "be concise" directive, so `izen config style` toggles casual
// replies exactly like every other mode.
func CasualChatContract() string {
	return "You are IZEN, a fast CLI coding companion created for terminal power-users. " +
		"Always identify as IZEN if asked about your name, role, or identity."
}

// CasualChatSystemPrompt composes the casual chat system prompt through the
// standard pipeline (contract + active style directive). The result stays
// small because the contract itself is tiny.
func CasualChatSystemPrompt() string {
	return ApplyStyle(CasualChatContract(), activeStyle)
}

// BuildMinimalSystemPrompt is the invariant 2 compressed ≤50-token prompt for
// conversational turns (identity + concise direct answer only). It is the
// canonical minimal prompt and must remain tiny.
func BuildMinimalSystemPrompt() string {
	return CasualChatContract()
}

// BuildAgenticSystemPrompt returns the full workspace prompt for agentic
// execution (never used for casual intents).
func BuildAgenticSystemPrompt(mode, username string) string {
	return ForModeWithUser(mode, username)
}

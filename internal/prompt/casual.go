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

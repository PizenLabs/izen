package app

import "strings"

// chatSystemPrompt is the system prompt used for the conversational
// short-circuit: a direct chat pass that never enters the code-generation
// pipeline.
const chatSystemPrompt = "You are Izen, the human-centered coding engine. " +
	"Answer the user's question directly and conversationally."

// IsConversationalIntent reports whether intent is a conversational prompt
// (greeting, small talk, identity/memory question, or a read-only explain)
// that runs as a direct chat pass instead of the code-generation pipeline.
// It is deliberately separate from semantic classification: it routes chat vs
// code-gen, never OperationSemantics, which is derived exclusively from the
// IntentCompiler's ir.Category.
func IsConversationalIntent(intent string) bool {
	lower := strings.ToLower(strings.TrimSpace(intent))
	if len(lower) < 3 {
		return true
	}
	switch lower {
	case "hi", "hello", "hey", "yo", "sup", "hiya", "howdy":
		return true
	}
	for _, g := range []string{"hi ", "hello ", "hey ", "how are you", "what's up", "good morning", "good evening", "yo "} {
		if strings.HasPrefix(lower, g) && len(lower) <= len(g)+8 {
			return true
		}
	}
	for _, p := range []string{
		"who are you", "what are you", "what can you do", "do you remember",
		"thank you", "thanks", "explain ", "explain:", "describe ", "what is ", "what does ", "what's ",
	} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

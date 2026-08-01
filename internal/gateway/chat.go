package gateway

import "strings"

// casualGreetingPatterns are phrases that indicate a casual,
// non-coding interaction (greeting, small talk, general question).
var casualGreetingPatterns = []string{
	"hi", "hello", "hey", "greetings", "good morning",
	"good afternoon", "good evening", "good night",
	"how are you", "how's it going", "what's up",
	"who are you", "what are you", "tell me about yourself",
	"what can you do", "help me", "can you help",
	"i need help", "i don't know", "is that you",
	"are you there", "got it", "ok", "okay", "thanks",
	"thank you", "cheers", "bye", "goodbye", "see you",
	"nice", "great", "awesome", "cool",
	"lol", "ahaha", "haha", "hmm", "oh",
	"wikipedia", "what is", "how does",
	"define", "meaning of",
}

// isCasualMatch reports whether lower contains phrase as a
// whole word or phrase (not a substring of a larger word).
func isCasualMatch(lower, phrase string) bool {
	idx := strings.Index(lower, phrase)
	if idx < 0 {
		return false
	}
	// Phrase found at idx. Check that the character before the
	// match is a word boundary and the character after the
	// match end is also a word boundary.
	beforeOK := idx == 0 || !isWordChar(rune(lower[idx-1]))
	afterIdx := idx + len(phrase)
	afterOK := afterIdx >= len(lower) || !isWordChar(rune(lower[afterIdx]))
	return beforeOK && afterOK
}

func isWordChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

// fileRefIndicatorPatterns signal the user is referencing a file
// or code artifact, which means this is a coding task.
var fileRefIndicatorPatterns = []string{
	"@", ".go ", ".ts ", ".js ", ".py ", ".rs ",
	".java ", ".rb ", ".cfg ", ".toml ", ".yaml ", ".yml ",
	".json ", ".md ", ".sh ", ".bat ", ".ps1 ",
	".html ", ".css ", ".proto ", ".graphql ", ".sql ",
	"error:", "undefined", "build fail", "build error",
	"import ", "package ", "func ", "type ", "struct ",
	"npm ", "go mod ", "cargo ",
	"pip install", "docker ", "k8s ", "terraform ",
}

// IsCasualChat classifies whether the user message is a casual
// greeting / general question (non-coding chatter) that should
// receive a lightweight system prompt and minimal token budget,
// or a coding task action that requires full context injection.
//
// Classification rules (checked in order):
//  1. Messages with explicit coding command prefixes ($prompt, /build,
//     /plan, /hotfix) are ALWAYS coding tasks, never casual.
//  2. Messages containing file references (@file, .go, error:, import, etc.)
//     are ALWAYS coding tasks.
//  3. Messages matching known casual greeting / small-talk patterns
//     are classified as casual chat.
//  4. Everything else defaults to coding task (safe conservative
//     choice — over-injecting context is cheaper than under-injecting
//     for a real task).
func IsCasualChat(input string) bool {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return false
	}

	// Rule 1: explicit coding command prefix → never casual
	for _, pattern := range []string{"$prompt", "$ask", "/build", "/plan", "/hotfix", "/investigate", "/review"} {
		if strings.HasPrefix(strings.ToLower(trimmed), pattern) {
			return false
		}
	}

	// Rule 2: file reference indicators → coding task, not casual
	lower := strings.ToLower(trimmed)
	for _, p := range fileRefIndicatorPatterns {
		if strings.Contains(lower, p) {
			return false
		}
	}

	// Rule 3: casual greeting / small-talk patterns → casual chat
	for _, p := range casualGreetingPatterns {
		if isCasualMatch(lower, p) {
			return true
		}
	}

	// Rule 4: default to coding task (conservative)
	return false
}

// CasualChatSystemPrompt returns the minimal system prompt for
// casual chat interactions (under 50 tokens). Always identifies
// the model as IZEN so that typos or unknown intents never
// produce identity leaks (e.g. "Claude").
func CasualChatSystemPrompt() string {
	return "You are IZEN, a fast CLI coding companion created for terminal power-users. Always identify as IZEN if asked about your name, role, or identity. Respond concisely in 1-2 short sentences."
}

// CasualChatMaxTokens returns the max_tokens budget for casual chat
// responses. It is a healthy default (2048) so casual replies can form
// complete sentences instead of being cut off mid-generation by a tiny
// completion ceiling (which surfaced on the OpenRouter dashboard as
// finish_reason: "length" at ~78 tokens).
func CasualChatMaxTokens() int {
	return 2048
}

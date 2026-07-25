package ai

// SimpleMutationPrompt returns a minimal system prompt that enforces
// SEARCH/REPLACE-only output with zero conversational filler. Designed
// for SIMPLE_MUTATION tier requests where max_tokens ≤ 150.
func SimpleMutationPrompt() string {
	return `STRICT RULE: Output ONLY a valid SEARCH/REPLACE block for the change.
NO conversational filler. NO markdown prose outside code blocks. NO full file rewrites.

FORMAT EXAMPLE:
<<<<<<< SEARCH
old_line
=======
new_line
>>>>>>>`
}

// IntentClassifyPrompt returns a minimal system prompt for cloud-based
// intent classification with extremely tight token budget (max_tokens: 30).
func IntentClassifyPrompt() string {
	return `Classify intent: MUTATE, READ, or DIAGNOSE. Output one word.`
}

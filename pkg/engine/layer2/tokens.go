package layer2

// EstimateTokens approximates the token count of text using the ~4 chars/token
// heuristic (0.25 tokens per char), conservative for source code. It mirrors
// the estimator used by the grounding layer so token accounting is consistent
// across the engine.
func EstimateTokens(s string) int {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

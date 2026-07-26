package retrieval

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/core/artifact"
)

type TokenWeightEstimator struct {
	// TokensPerChar is the estimated token-to-character ratio.
	// Default 1 token per 4 characters (conservative for code).
	TokensPerChar float64
}

func NewTokenWeightEstimator() *TokenWeightEstimator {
	return &TokenWeightEstimator{
		TokensPerChar: 0.25,
	}
}

func (e *TokenWeightEstimator) Estimate(text string) int {
	if text == "" {
		return 0
	}
	return int(float64(len(text)) * e.TokensPerChar)
}

func (e *TokenWeightEstimator) EstimateAndFit(text string, maxTokens int) (string, int) {
	estimated := e.Estimate(text)
	if estimated <= maxTokens {
		return text, estimated
	}

	excessRatio := float64(maxTokens) / float64(estimated)
	maxChars := int(float64(len(text)) * excessRatio)

	if maxChars <= 0 {
		return "", estimated
	}

	truncated := truncateByLines(text, maxChars)
	return truncated, e.Estimate(truncated)
}

func truncateByLines(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}

	lines := strings.Split(text, "\n")
	var result strings.Builder
	chars := 0

	for _, line := range lines {
		lineLen := len(line) + 1
		if chars+lineLen > maxChars {
			if result.Len() == 0 && len(line) > 0 {
				trim := line
				if len(trim) > maxChars {
					trim = trim[:maxChars]
				}
				result.WriteString(trim)
				result.WriteString("\n[truncated]")
			} else {
				result.WriteString("[truncated]")
			}
			break
		}
		result.WriteString(line)
		result.WriteString("\n")
		chars += lineLen
	}

	return strings.TrimRight(result.String(), "\n")
}

func TokenBudgetExceeded(estimated, maxTokens int) error {
	if estimated > maxTokens {
		return fmt.Errorf(
			"token budget exceeded: estimated %d tokens, max %d tokens (reduce context scope or increase budget)",
			estimated, maxTokens,
		)
	}
	return nil
}

const EvidenceKeyTokenWeight = "token_estimate"
const EvidenceKeyResultCount = "result_count"
const EvidenceKeyStrategy = "strategy"

func RecordFallbackEvidence(store *artifact.Store, evidenceType, query string, resultSet *ResultSet) (artifact.Artifact, error) {
	if store == nil {
		return nil, nil
	}

	combined := fmt.Sprintf("query=%s strategy=%s results=%d confidence=%.2f",
		query, resultSet.Strategy, resultSet.Count(), resultSet.Confidence)

	ea := artifact.NewEvidenceArtifact(evidenceType, combined, fmt.Sprintf("%.0f", resultSet.Confidence*100))
	if err := store.Save(ea); err != nil {
		return nil, fmt.Errorf("record evidence: %w", err)
	}
	return ea, nil
}

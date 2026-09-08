package grounding

import (
	"fmt"
	"strings"
)

const DefaultConfidenceThreshold = 0.5

type CanonicalIntent struct {
	RawPrompt    string   `json:"raw_prompt"`
	CleanIntent  string   `json:"clean_intent"`
	TargetScopes []string `json:"target_scopes"`
	Confidence   float64  `json:"confidence"`
}

type Sanitizer struct {
	matcher   *FuzzyMatcher
	threshold float64
}

func NewSanitizer() *Sanitizer {
	return &Sanitizer{
		matcher:   NewFuzzyMatcher(0.6),
		threshold: DefaultConfidenceThreshold,
	}
}

func (s *Sanitizer) Sanitize(raw string) (*CanonicalIntent, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("grounding: empty raw prompt")
	}

	words := Words(raw)

	var cleaned []string
	var scopes []string
	scopeSet := make(map[string]bool)
	matchCount := 0

	for _, w := range words {
		if isNoiseWord(w) {
			continue
		}

		if m := s.matcher.Match(w); m != nil {
			cleaned = append(cleaned, m.Keyword)
			if !scopeSet[m.Keyword] {
				scopeSet[m.Keyword] = true
				scopes = append(scopes, m.Keyword)
			}
			matchCount++
		} else {
			cleaned = append(cleaned, w)
		}
	}

	cleanIntent := strings.Join(cleaned, " ")
	if cleanIntent == "" {
		cleanIntent = strings.ToLower(strings.TrimSpace(raw))
	}

	confidence := float64(matchCount) / float64(max(len(words), 1))

	return &CanonicalIntent{
		RawPrompt:    raw,
		CleanIntent:  cleanIntent,
		TargetScopes: scopes,
		Confidence:   confidence,
	}, nil
}

func (s *Sanitizer) NeedsClarification(intent *CanonicalIntent) bool {
	return intent.Confidence < s.threshold || len(intent.TargetScopes) == 0
}

func (s *Sanitizer) WithThreshold(t float64) *Sanitizer {
	s.threshold = t
	return s
}

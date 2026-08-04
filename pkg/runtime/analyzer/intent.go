package analyzer

import (
	"sort"
	"strconv"
	"strings"
)

// intentKeywords maps each intent to the deterministic keyword markers that
// trigger it. The map keys order the priority tie-break: bug fixes win over
// refactors, refactors over features, features over questions.
var intentKeywords = map[Intent][]string{
	IntentBugFix:   {"bug", "fix", "broken", "error", "panic", "crash", "failing", "incorrect", "buggy"},
	IntentRefactor: {"refactor", "rename", "simplify", "restructure", "extract", "cleanup", "deduplicate", "modularize"},
	IntentFeature:  {"add", "implement", "new", "feature", "create", "support", "introduce", "build"},
	IntentQuestion: {"how", "what", "why", "explain", "describe", "?"},
}

// intentPriority defines the deterministic tie-break order.
var intentPriority = []Intent{IntentBugFix, IntentRefactor, IntentFeature, IntentQuestion}

// ParseIntent classifies a request input with deterministic keyword scoring.
// It returns the winning intent and a human-readable reason that lists the
// matched keywords, so the classification is always explainable.
func ParseIntent(input string) (Intent, string) {
	lower := strings.ToLower(input)
	best := IntentUnknown
	bestScore := 0
	var bestHits []string
	for _, intent := range intentPriority {
		score := 0
		var hits []string
		for _, kw := range intentKeywords[intent] {
			if strings.Contains(lower, kw) {
				score++
				hits = append(hits, kw)
			}
		}
		if score > bestScore {
			best, bestScore, bestHits = intent, score, hits
		}
	}
	if bestScore == 0 {
		return best, "no intent keywords matched"
	}
	sort.Strings(bestHits)
	return best, "matched " + strconv.Itoa(bestScore) + " keyword(s): " + strings.Join(bestHits, ", ")
}

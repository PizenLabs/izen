package analyzer

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// intentKeywords maps each code intent to the deterministic keyword markers
// that trigger it. The map keys order the priority tie-break: bug fixes win
// over refactors, refactors over features, features over questions.
var intentKeywords = map[Intent][]string{
	IntentBugFix:   {"bug", "fix", "broken", "error", "panic", "crash", "failing", "incorrect", "buggy"},
	IntentRefactor: {"refactor", "rename", "simplify", "restructure", "extract", "cleanup", "deduplicate", "modularize"},
	IntentFeature:  {"add", "implement", "new", "feature", "create", "support", "introduce", "build"},
	IntentQuestion: {"how", "what", "why", "explain", "describe", "?"},
}

// intentPriority defines the deterministic tie-break order.
var intentPriority = []Intent{IntentBugFix, IntentRefactor, IntentFeature, IntentQuestion}

// strongIntents are code intents whose keywords indicate an actual code
// operation. They always win over the conversational fast path.
var strongIntents = map[Intent]struct{}{
	IntentBugFix:   {},
	IntentRefactor: {},
	IntentFeature:  {},
}

// chatPhrases are high-precision conversational markers matched as contiguous
// whole-word sequences. Identity, memory, greeting and small-talk questions
// land here so they are never routed into the coding engines.
var chatPhrases = []string{
	"do you remember me",
	"do you remember",
	"remember me",
	"remember us",
	"who are you",
	"what are you",
	"what are u",
	"what is your name",
	"whats your name",
	"what is your purpose",
	"are you alive",
	"are you real",
	"are you human",
	"are you a robot",
	"are you ai",
	"are you there",
	"are you ok",
	"are you okay",
	"how are you",
	"how are u",
	"how s it going",
	"hows it going",
	"how is it going",
	"how do you feel",
	"how have you been",
	"what s up",
	"what is up",
	"whats up",
	"what are you up to",
	"nice to meet you",
	"nice meeting you",
	"good to see you",
	"good morning",
	"good afternoon",
	"good evening",
	"good night",
	"thank you",
	"thanks a lot",
	"thank you very much",
	"see you later",
	"see you soon",
	"see you",
	"talk to you later",
	"what can you do",
	"what do you do",
	"can you help me",
	"can u help me",
}

// chatWords are single-word conversational markers matched only as whole
// words, so "hi" never fires inside "which" or "chair".
var chatWords = map[string]struct{}{
	"hi": {}, "hello": {}, "hey": {}, "yo": {}, "hiya": {}, "howdy": {},
	"greetings": {}, "sup": {}, "thanks": {}, "cheers": {}, "welcome": {},
	"bye": {}, "goodbye": {}, "aloha": {},
}

// codeHints are explicit code-operation signals that keep a prompt on the
// coding path even when no intent keyword fires. A prompt with any of these
// is never classified as conversational.
var codeHints = []string{
	"function", "method", "class", "struct", "interface", "variable",
	"import", "package", "module", "syntax", "compile", "compiler", "lint",
	"gofmt", "go test", "go mod", "test", "tests", "testing", "code",
	"api", "endpoint", "router", "handler", "worker", "server", "client",
	"error", "panic", "exception", "stack trace", "trace", "bug", "crash",
	"file", "files", "path", "line", "symbol", "blueprint", "plan",
}

// fileRefRe detects inline file references such as out.txt, main.go or
// config.json. A prompt naming a file touches the workspace and is therefore
// never classified as conversational.
var fileRefRe = regexp.MustCompile(`[a-z0-9]+\.[a-z0-9]{1,6}`)

// ParseIntent classifies a request input with deterministic keyword scoring.
// It returns the winning intent and a human-readable reason that lists the
// matched markers, so the classification is always explainable.
func ParseIntent(input string) (Intent, string) {
	intent, _, reason := ParseIntentConfidence(input)
	return intent, reason
}

// ParseIntentConfidence classifies a request input and returns the winning
// intent together with a deterministic confidence in (0, 1]. Conversational
// prompts (IntentChat) are always reported with confidence above 0.95; code
// intents score 0.9 and unknown inputs score 0.5.
func ParseIntentConfidence(input string) (Intent, float64, string) {
	lower := strings.ToLower(strings.TrimSpace(input))
	if lower == "" {
		return IntentUnknown, 0.5, "empty input"
	}

	// Strong code intents (bug_fix, refactor, feature) always win, even when
	// a prompt also contains conversational markers.
	best, bestScore, bestHits := scoreCodeIntents(lower)
	if bestScore > 0 && isStrongIntent(best) {
		sort.Strings(bestHits)
		return best, 0.9, "matched " + strconv.Itoa(bestScore) + " keyword(s): " + strings.Join(bestHits, ", ")
	}

	// Conversational markers beat the weak question keywords ("what", "how",
	// "?") so prompts like "what's up" stay on the chat fast path.
	if conf, hits := chatScore(lower); conf > 0 {
		return IntentChat, conf, "conversational prompt matched: " + strings.Join(hits, ", ")
	}
	// Weak question keywords ("what", "why", "how", "?") classify as
	// questions unless they matched a conversational marker above.
	if bestScore > 0 {
		sort.Strings(bestHits)
		return best, 0.85, "matched " + strconv.Itoa(bestScore) + " keyword(s): " + strings.Join(bestHits, ", ")
	}

	// No code markers and no conversational markers: the prompt touches no
	// files, AST symbols or explicit code operations, so it is routed through
	// the conversational fast path.
	if !hasCodeHints(lower) {
		return IntentChat, 0.96, "no code operations, file or symbol references detected"
	}
	return IntentUnknown, 0.5, "no intent keywords matched"
}

// isStrongIntent reports whether intent indicates an actual code operation
// rather than a weak question marker.
func isStrongIntent(intent Intent) bool {
	_, ok := strongIntents[intent]
	return ok
}

// scoreCodeIntents returns the highest-scoring code intent, its score and the
// matched keywords. Question keywords are scored as before but kept separate
// so the caller can apply the conversational precedence.
func scoreCodeIntents(lower string) (Intent, int, []string) {
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
	return best, bestScore, bestHits
}

// chatScore reports whether the input is conversational and returns a
// deterministic confidence above 0.95 together with the matched markers.
// Apostrophes are stripped so "what's up" normalizes to "whats up" before
// phrase matching.
func chatScore(input string) (float64, []string) {
	words := tokenizeWords(strings.ReplaceAll(input, "'", ""))
	var hits []string
	markers := 0
	for _, phrase := range chatPhrases {
		if containsPhrase(words, phrase) {
			markers++
			hits = append(hits, phrase)
		}
	}
	seen := make(map[string]struct{}, len(words))
	for _, w := range words {
		if _, ok := chatWords[w]; ok {
			if _, dup := seen[w]; dup {
				continue
			}
			seen[w] = struct{}{}
			markers++
			hits = append(hits, w)
		}
	}
	if markers == 0 {
		return 0, nil
	}
	conf := 0.97 + 0.005*float64(markers)
	if conf > 0.99 {
		conf = 0.99
	}
	sort.Strings(hits)
	return conf, hits
}

// hasCodeHints reports whether any explicit code-operation marker or file
// reference appears in the input.
func hasCodeHints(lower string) bool {
	for _, hint := range codeHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return fileRefRe.MatchString(lower)
}

// tokenizeWords splits the input into lowercase word tokens, dropping
// punctuation.
func tokenizeWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// containsPhrase reports whether phrase appears as a contiguous whole-word
// sequence inside words.
func containsPhrase(words []string, phrase string) bool {
	phraseWords := strings.Fields(phrase)
	if len(phraseWords) > len(words) {
		return false
	}
	for i := 0; i+len(phraseWords) <= len(words); i++ {
		match := true
		for j, pw := range phraseWords {
			if words[i+j] != pw {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

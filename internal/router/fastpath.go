package router

import "strings"

// fastPathResult is the fixed, preallocated outcome for a deterministic match.
type fastPathResult struct {
	intent      Intent
	confidence  float64
	explanation string
}

// fastPath deterministically classifies input using syntax/prefix checks ONLY.
// It never invokes semantic classification, so its execution cost is bounded
// and language-independent. It returns the matched intent and a static
// explanation string, or false when no deterministic signal applies.
//
// Deterministic signals recognized here:
//   - explicit slash commands: /plan, /build, /ask, /investigate, /review
//   - the $hot fast-track prefix (explicit bypass to /build)
//   - high-intent flags: --force, --high, /intent high
//   - explicit file:line references (e.g. @main.go:42, handler.go:123)
func fastPath(input string) (fastPathResult, bool) {
	if input == "" {
		return fastPathResult{}, false
	}

	// Slash commands: compare the leading run of letters against the known
	// mode names without allocating (strings.HasPrefix never allocates).
	switch {
	case hasPrefixFold(input, "/plan"):
		return fastPathResult{intent: IntentPlan, confidence: 1.0, explanation: "explicit /plan command"}, true
	case hasPrefixFold(input, "/build"):
		return fastPathResult{intent: IntentBuild, confidence: 1.0, explanation: "explicit /build command"}, true
	case hasPrefixFold(input, "/ask"):
		return fastPathResult{intent: IntentAsk, confidence: 1.0, explanation: "explicit /ask command"}, true
	case hasPrefixFold(input, "/investigate"):
		return fastPathResult{intent: IntentInvestigate, confidence: 1.0, explanation: "explicit /investigate command"}, true
	case hasPrefixFold(input, "/review"):
		return fastPathResult{intent: IntentReview, confidence: 1.0, explanation: "explicit /review command"}, true
	}

	// $hot fast-track prefix: explicit bypass that routes directly to /build.
	if strings.HasPrefix(input, "$hot") {
		return fastPathResult{intent: IntentBuild, confidence: 1.0, explanation: "$hot fast-track prefix"}, true
	}

	// High-intent flags: --force and --high force execution without analysis.
	lower := toLowerASCII(input)
	if strings.Contains(lower, "--force") || strings.Contains(lower, "--high") ||
		strings.Contains(lower, "/intent high") {
		return fastPathResult{intent: IntentBuild, confidence: 1.0, explanation: "explicit high-intent flag"}, true
	}

	// Explicit file:line references (e.g. @main.go:42, src/handler.go:123)
	// signal a targeted edit at a deterministic location.
	if hasFileLineRef(input) {
		return fastPathResult{intent: IntentPlan, confidence: 1.0, explanation: "explicit file:line reference"}, true
	}

	return fastPathResult{}, false
}

// hasPrefixFold reports whether s starts with prefix, ignoring ASCII case.
// It never allocates.
func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		c := s[i]
		p := prefix[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if p >= 'A' && p <= 'Z' {
			p += 'a' - 'A'
		}
		if c != p {
			return false
		}
	}
	return true
}

// toLowerASCII lowercases s in place of the heap: it returns the original
// string when it contains no uppercase ASCII bytes, otherwise a new string.
// The no-uppercase fast path avoids any allocation on the hot path.
func toLowerASCII(s string) string {
	needs := false
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			needs = true
			break
		}
	}
	if !needs {
		return s
	}
	return strings.ToLower(s)
}

// hasFileLineRef reports whether input carries an explicit file:line reference
// of the form <path>.<ext>:<digits>. It scans bytes directly and never
// allocates, so the deterministic fast path stays allocation-free.
func hasFileLineRef(input string) bool {
	for i := 0; i < len(input); i++ {
		if input[i] != ':' {
			continue
		}
		// A line reference must be followed by at least one digit.
		j := i + 1
		if j >= len(input) || input[j] < '0' || input[j] > '9' {
			continue
		}
		// Walk backwards from ':' to find a dot (extension separator) before
		// a word boundary. @main.go:42 and src/handler.go:123 both match.
		k := i - 1
		for k >= 0 {
			c := input[k]
			if c == '.' {
				return true
			}
			if c < '0' || (c > '9' && c < 'A') || (c > 'Z' && c < 'a') || c > 'z' {
				break
			}
			k--
		}
	}
	return false
}

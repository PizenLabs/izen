package prompt

import (
	"fmt"
	"strings"
)

// StylePolicy names a response verbosity policy. The active policy is injected
// into every composed system prompt as an OUTPUT STYLE directive so generated
// responses consistently follow the engineer's preferred verbosity.
type StylePolicy string

const (
	// StyleVerbose: detailed explanations, step-by-step guides, full prose.
	StyleVerbose StylePolicy = "verbose"
	// StyleBalanced (default): concise explanations with complete code blocks.
	StyleBalanced StylePolicy = "balanced"
	// StyleTerse: direct technical fragments, no preambles, exact code/paths.
	StyleTerse StylePolicy = "terse"
	// StyleUltra: telemetry/caveman shorthand for extreme token savings.
	StyleUltra StylePolicy = "ultra"
)

// ValidStylePolicies enumerates every supported policy, ordered from most to
// least verbose.
var ValidStylePolicies = []StylePolicy{StyleVerbose, StyleBalanced, StyleTerse, StyleUltra}

// DefaultStylePolicy is used whenever no style is configured.
func DefaultStylePolicy() StylePolicy { return StyleBalanced }

// String returns the canonical policy name.
func (p StylePolicy) String() string { return string(p) }

// ParseStylePolicy normalizes s (case-insensitive) to a StylePolicy. The empty
// string resolves to the default (Balanced).
func ParseStylePolicy(s string) (StylePolicy, error) {
	switch StylePolicy(strings.ToLower(strings.TrimSpace(s))) {
	case "":
		return DefaultStylePolicy(), nil
	case StyleVerbose:
		return StyleVerbose, nil
	case StyleBalanced:
		return StyleBalanced, nil
	case StyleTerse:
		return StyleTerse, nil
	case StyleUltra:
		return StyleUltra, nil
	default:
		return "", fmt.Errorf("unknown style policy %q (valid: %s)", s, strings.Join(styleNames(), ", "))
	}
}

// IsValidStylePolicy reports whether s names a supported policy.
func IsValidStylePolicy(s string) bool {
	_, err := ParseStylePolicy(s)
	return err == nil
}

func styleNames() []string {
	names := make([]string, 0, len(ValidStylePolicies))
	for _, p := range ValidStylePolicies {
		names = append(names, string(p))
	}
	return names
}

// activeStyle is the package-level policy injected by Compose into every
// composed system prompt. Bootstrap code calls SetActiveStyle with the value
// read from configuration.
var activeStyle = DefaultStylePolicy()

// SetActiveStyle sets the active policy and returns the previously active one.
func SetActiveStyle(p StylePolicy) StylePolicy {
	if p == "" {
		p = DefaultStylePolicy()
	}
	prev := activeStyle
	activeStyle = p
	return prev
}

// ActiveStyle returns the currently active style policy.
func ActiveStyle() StylePolicy { return activeStyle }

// StyleDirective returns the OUTPUT STYLE directive text for p. Unknown
// policies yield "".
func StyleDirective(p StylePolicy) string {
	switch p {
	case StyleVerbose:
		return `OUTPUT STYLE: Verbose
- Provide detailed explanations with step-by-step reasoning.
- Write in full conversational prose; explain the "why" behind each decision.
- Prefer complete, self-contained answers with concrete examples.
- Do not abbreviate explanations to save tokens.`
	case StyleBalanced:
		return `OUTPUT STYLE: Balanced
- Give concise explanations paired with complete, runnable code blocks.
- State the key idea in 2-3 sentences, then show the exact code, paths, and flags.
- Prefer the minimum prose needed to make the code self-evident.`
	case StyleTerse:
		return `OUTPUT STYLE: Terse
- No preambles, greetings, or closing remarks. No "Sure!", "Here's how:", or summary trailers.
- Answer in direct technical fragments: exact code, exact paths, exact flags.
- Use code blocks and bullet points over prose; omit anything the reader can infer.`
	case StyleUltra:
		return `OUTPUT STYLE: Ultra
- Telemetry/caveman shorthand for extreme token savings.
- Respond in 1-2 word fragments when possible; drop articles, fillers, and full sentences.
- Emit only code, paths, and flags. No explanations unless explicitly requested.`
	default:
		return ""
	}
}

// ApplyStyle appends the directive for p to systemPrompt. Unknown policies
// leave systemPrompt untouched.
func ApplyStyle(systemPrompt string, p StylePolicy) string {
	d := StyleDirective(p)
	if d == "" {
		return systemPrompt
	}
	return strings.TrimRight(systemPrompt, "\n") + "\n\n" + d
}

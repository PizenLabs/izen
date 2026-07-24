package agents

import (
	"regexp"
	"strings"
)

// testLogPatterns match lines commonly found in go test / compiler output
// that carry no structural value for a direct mutation plan.
var testLogPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^ok\s+`),
	regexp.MustCompile(`(?i)^FAIL\s+`),
	regexp.MustCompile(`(?i)^---\s+(PASS|FAIL|SKIP)`),
	regexp.MustCompile(`(?i)^\?`),
	regexp.MustCompile(`(?i)^=== RUN`),
	regexp.MustCompile(`(?i)^\s+.*_test\.go:`),
	regexp.MustCompile(`(?i)panic:`),
	regexp.MustCompile(`(?i)goroutine\s+\d+`),
	regexp.MustCompile(`(?i)created by`),
	regexp.MustCompile(`(?i)go: downloading`),
	regexp.MustCompile(`(?i)go: extracting`),
	regexp.MustCompile(`(?i)go: finding`),
}

// runtimeDetailPatterns match environment/runtime diagnostic lines that are
// irrelevant for a direct mutation (no compile/test step required).
var runtimeDetailPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)go\s+version`),
	regexp.MustCompile(`(?i)git\s+branch`),
	regexp.MustCompile(`(?i)git\s+commit`),
	regexp.MustCompile(`(?i)GOPATH=`),
	regexp.MustCompile(`(?i)GO111MODULE=`),
	regexp.MustCompile(`(?i)GOFLAGS=`),
	regexp.MustCompile(`(?i)GOROOT=`),
	regexp.MustCompile(`(?i)PATH=`),
	regexp.MustCompile(`(?i)SHELL=`),
	regexp.MustCompile(`(?i)TERM=`),
	regexp.MustCompile(`(?i)HOME=`),
	regexp.MustCompile(`(?i)^\[SYSTEM`),
	regexp.MustCompile(`(?i)^You\s+MUST`),
	regexp.MustCompile(`(?i)^CRITICAL:`),
	regexp.MustCompile(`(?i)^RULES:`),
	regexp.MustCompile(`(?i)^DIRECTIVES:`),
	regexp.MustCompile(`(?i)^FORBIDDEN`),
	regexp.MustCompile(`(?i)ROOT:`),
	regexp.MustCompile(`(?i)BLOCKER:`),
}

// SanitizeForensicLedger strips all test logs, environment runtime details,
// and unreferenced semantic rules from the ledger content. For direct mutation
// mode the output is guaranteed to be under 200 tokens (~800 characters).
// Only structural targets and minimal instructions survive.
func SanitizeForensicLedger(ledger string) string {
	if ledger == "" {
		return ""
	}

	lines := strings.Split(ledger, "\n")
	var kept []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Drop test output and compiler diagnostics.
		if matchesAny(trimmed, testLogPatterns) {
			continue
		}

		// Drop runtime/environment details and metarules.
		if matchesAny(trimmed, runtimeDetailPatterns) {
			continue
		}

		kept = append(kept, trimmed)
	}

	// Extract only the structural targets (TGT: / FILE: / DONE: lines).
	var structural []string
	for _, line := range kept {
		lower := strings.ToLower(line)
		if strings.HasPrefix(line, "TGT:") ||
			strings.HasPrefix(line, "FILE:") ||
			strings.HasPrefix(line, "DONE:") ||
			strings.HasPrefix(line, "ROOT:") ||
			strings.HasPrefix(lower, "conclusion:") ||
			strings.HasPrefix(lower, "root cause:") ||
			strings.HasPrefix(line, "[PKT-") {
			structural = append(structural, line)
		}
	}

	if len(structural) == 0 && len(kept) > 0 {
		structural = kept[:min(len(kept), 5)]
	}

	result := strings.Join(structural, "\n")
	result = strings.TrimSpace(result)

	// Hard cap at 200 tokens (~800 chars) for direct mutation.
	runes := []rune(result)
	if len(runes) > 800 {
		result = string(runes[:800])
	}

	if result == "" {
		return ""
	}
	return result
}

// matchesAny reports whether the line matches any of the given patterns.
func matchesAny(line string, patterns []*regexp.Regexp) bool {
	for _, p := range patterns {
		if p.MatchString(line) {
			return true
		}
	}
	return false
}

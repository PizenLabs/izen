package classifier

import (
	"fmt"
	"regexp"
)

type FailureClass int

const (
	FailureCodeClass FailureClass = iota
	FailureEnvironmentClass
	FailureTestClass
	FailureScopeClass
	FailureUnknownClass
)

func (fc FailureClass) String() string {
	switch fc {
	case FailureCodeClass:
		return "code"
	case FailureEnvironmentClass:
		return "environment"
	case FailureTestClass:
		return "test"
	case FailureScopeClass:
		return "scope"
	case FailureUnknownClass:
		return "unknown"
	default:
		return fmt.Sprintf("FailureClass(%d)", int(fc))
	}
}

type ClassificationResult struct {
	Class   FailureClass
	Reason  string
	Details map[string]string
}

type PatternMatcher struct {
	Pattern *regexp.Regexp
	Class   FailureClass
	Reason  string
}

type FailureClassifier struct {
	matchers []PatternMatcher
}

func NewFailureClassifier() *FailureClassifier {
	return &FailureClassifier{
		matchers: []PatternMatcher{
			{
				Pattern: regexp.MustCompile(`(?i)(syntax error|undefined:|cannot find|cannot use|type mismatch|not used|declared but not|compilation error|expected ';'|unexpected\b|redeclared|impossible type|invalid operation|invalid memory|nil pointer|index out of range|divide by zero|runtime error)`),
				Class:   FailureCodeClass,
				Reason:  "code error pattern matched",
			},
			{
				Pattern: regexp.MustCompile(`(?i)(exit status 2|build failed|compilation failed|compile error)`),
				Class:   FailureCodeClass,
				Reason:  "build failure pattern matched",
			},
			{
				Pattern: regexp.MustCompile(`(?i)(connection refused|no such host|timeout|i/o timeout|no route to host|network is unreachable|broken pipe|connection reset)`),
				Class:   FailureEnvironmentClass,
				Reason:  "network error pattern matched",
			},
			{
				Pattern: regexp.MustCompile(`(?i)(command not found|executable file not found|cannot exec|cannot run|no such file or directory|permission denied|required binary|missing binary|not installed)`),
				Class:   FailureEnvironmentClass,
				Reason:  "missing binary pattern matched",
			},
			{
				Pattern: regexp.MustCompile(`(?i)(environment variable|not set|missing environment|env var|not configured|missing config\b|not found in path)`),
				Class:   FailureEnvironmentClass,
				Reason:  "environment config pattern matched",
			},
			{
				Pattern: regexp.MustCompile(`(?i)(test failed|FAIL\s|assertion|expected.*but got|got.*want|not equal|test case.*failed|test suite.*failed)`),
				Class:   FailureTestClass,
				Reason:  "test failure pattern matched",
			},
			{
				Pattern: regexp.MustCompile(`(?i)(unauthorized|outside.*scope|not authorized|file.*not.*permit|path.*not.*allow|scope.*violation|not within.*scope)`),
				Class:   FailureScopeClass,
				Reason:  "scope violation pattern matched",
			},
		},
	}
}

func (fc *FailureClassifier) Classify(output string, exitCode int) ClassificationResult {
	if exitCode != 0 && output == "" {
		return ClassificationResult{
			Class:  FailureCodeClass,
			Reason: fmt.Sprintf("non-zero exit code %d with no output", exitCode),
		}
	}
	for _, m := range fc.matchers {
		if m.Pattern.MatchString(output) {
			details := map[string]string{}
			match := m.Pattern.FindString(output)
			if match != "" {
				details["matched_pattern"] = match
			}
			return ClassificationResult{
				Class:   m.Class,
				Reason:  m.Reason,
				Details: details,
			}
		}
	}
	if exitCode != 0 {
		return ClassificationResult{
			Class:  FailureCodeClass,
			Reason: fmt.Sprintf("non-zero exit code %d (unclassified)", exitCode),
		}
	}
	return ClassificationResult{
		Class:  FailureUnknownClass,
		Reason: "output did not match any known failure pattern",
	}
}

func (fc *FailureClassifier) AddMatcher(pattern *regexp.Regexp, class FailureClass, reason string) {
	fc.matchers = append(fc.matchers, PatternMatcher{
		Pattern: pattern,
		Class:   class,
		Reason:  reason,
	})
}

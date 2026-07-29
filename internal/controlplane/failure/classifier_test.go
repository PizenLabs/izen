package failure

import (
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/PizenLabs/izen/internal/controlplane/guard"
)

func TestClassify_CodeFailure_SyntaxError(t *testing.T) {
	c := NewClassifier()
	tests := []struct {
		output string
		desc   string
	}{
		{"syntax error: unexpected newline, expecting comma or }", "unexpected newline"},
		{"# command-line-arguments\n./main.go:10: undefined: Println", "undefined symbol"},
		{"cannot find package \"fmt\" in any of:", "cannot find package"},
		{"cannot use str (variable of type string) as type int", "type mismatch"},
		{"x declared but not used", "declared not used"},
		{"compilation error: expected ';'", "compilation error"},
		{"build failed: exit status 2", "build failed"},
		{"vet: ./foo.go:42: unreachable code", "vet finding"},
		{"not enough arguments in call to foo", "not enough arguments"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := c.Classify(nil, tc.output); got != CODE_FAILURE {
				t.Errorf("Classify(nil, %q) = %s, want CODE_FAILURE", tc.output[:min(len(tc.output), 40)], got)
			}
		})
	}
}

func TestClassify_CodeFailure_ErrNoOutput(t *testing.T) {
	c := NewClassifier()
	// Non-nil error with no output but not a scope guard sentinel
	err := errors.New("some error")
	if got := c.Classify(err, ""); got != UNKNOWN {
		t.Errorf("Classify(err, \"\") = %s, want UNKNOWN", got)
	}
}

func TestClassify_EnvironmentFailure_MissingBinary(t *testing.T) {
	c := NewClassifier()
	tests := []struct {
		output string
		desc   string
	}{
		{"exec: \"go\": executable file not found in $PATH", "missing go binary"},
		{"command not found: python3", "command not found"},
		{"cannot exec: permission denied", "cannot exec"},
		{"open /etc/config.yaml: no such file or directory", "no such file"},
		{"required binary 'node' not installed", "required binary"},
		{"connection refused: dial tcp 127.0.0.1:8080", "connection refused"},
		{"no such host: example.invalid", "no such host"},
		{"i/o timeout after 30s", "i/o timeout"},
		{"network is unreachable", "network unreachable"},
		{"environment variable HOME not set", "env var not set"},
		{"missing environment variable DB_HOST", "missing env"},
		{"pattern ./... does not contain main module", "no main module"},
		{"go: go.mod file not found in current directory", "go.mod not found"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := c.Classify(nil, tc.output); got != ENVIRONMENT_FAILURE {
				t.Errorf("Classify(nil, %q) = %s, want ENVIRONMENT_FAILURE", tc.output[:min(len(tc.output), 50)], got)
			}
		})
	}
}

func TestClassify_TestFailure(t *testing.T) {
	c := NewClassifier()
	tests := []struct {
		output string
		desc   string
	}{
		{"--- FAIL: TestValidatePatch (0.00s)", "test function fail"},
		{"    assertion failed: expected 5, got 3", "assertion failed"},
		{"expected \"hello\", but got \"world\"", "expected but got"},
		{"got 42, want 7", "got want mismatch"},
		{"not equal: values differ", "not equal"},
		{"test case TestFoo failed", "test case failed"},
		{"panic in test: runtime error", "panic test"},
		{"FAIL\tgithub.com/foo/bar\t0.5s", "FAIL prefix"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := c.Classify(nil, tc.output); got != TEST_FAILURE {
				t.Errorf("Classify(nil, %q) = %s, want TEST_FAILURE", tc.output[:min(len(tc.output), 40)], got)
			}
		})
	}
}

func TestClassify_ScopeFailure_BySentinel(t *testing.T) {
	c := NewClassifier()

	t.Run("ErrScopeViolation", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", guard.ErrScopeViolation)
		if got := c.Classify(err, ""); got != SCOPE_FAILURE {
			t.Errorf("Classify(ErrScopeViolation) = %s, want SCOPE_FAILURE", got)
		}
	})

	t.Run("ErrBudgetExceeded", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", guard.ErrBudgetExceeded)
		if got := c.Classify(err, ""); got != SCOPE_FAILURE {
			t.Errorf("Classify(ErrBudgetExceeded) = %s, want SCOPE_FAILURE", got)
		}
	})

	t.Run("bare sentinel", func(t *testing.T) {
		if got := c.Classify(guard.ErrScopeViolation, ""); got != SCOPE_FAILURE {
			t.Errorf("Classify(bare ErrScopeViolation) = %s, want SCOPE_FAILURE", got)
		}
	})
}

func TestClassify_ScopeFailure_ByPattern(t *testing.T) {
	c := NewClassifier()
	tests := []struct {
		output string
		desc   string
	}{
		{"scope violation: patch touches file outside declared plan scope", "scope violation"},
		{"path /etc/passwd is not within scope", "not within scope"},
		{"mutation budget exceeded: diff_lines limit=5", "budget exceeded"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := c.Classify(nil, tc.output); got != SCOPE_FAILURE {
				t.Errorf("Classify(nil, %q) = %s, want SCOPE_FAILURE", tc.output, got)
			}
		})
	}
}

func TestClassify_SecurityIssue(t *testing.T) {
	c := NewClassifier()
	tests := []struct {
		output string
		desc   string
	}{
		{"G101: Potential hardcoded credential \"password\" detected", "gosec G101"},
		{"hardcoded credential found in config.go", "hardcoded credential"},
		{"hardcoded secret detected in variable 'api_key'", "hardcoded secret"},
		{"semgrep error: rule 'no-hardcoded-secrets' triggered", "semgrep error"},
		{"snyk: finding 1 vulnerability in main.go", "snyk finding"},
		{"forbidden pattern: api_key hardcoded in source", "forbidden pattern"},
		{"static analysis error: secrets detected in diff", "static analysis"},
		{"vulnerability found by govulncheck", "govulncheck"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := c.Classify(nil, tc.output); got != SECURITY_ISSUE {
				t.Errorf("Classify(nil, %q) = %s, want SECURITY_ISSUE", tc.output[:min(len(tc.output), 50)], got)
			}
		})
	}
}

func TestClassify_Unknown(t *testing.T) {
	c := NewClassifier()
	tests := []struct {
		output string
		desc   string
	}{
		{"everything looks fine", "normal output"},
		{"", "empty output"},
		{"processing complete: 10 files updated", "success message"},
		{"some random message with no pattern match", "ambiguous text"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := c.Classify(nil, tc.output); got != UNKNOWN {
				t.Errorf("Classify(nil, %q) = %s, want UNKNOWN", tc.output, got)
			}
		})
	}
}

func TestClassify_Priority(t *testing.T) {
	c := NewClassifier()

	// Code patterns checked before test patterns, so a line that matches both
	// must be classified as CODE_FAILURE. "test failed" also appears in test
	// patterns, but let's test with a clear code pattern mixed with test.
	t.Run("code beats test", func(t *testing.T) {
		output := "syntax error in test: --- FAIL: TestFoo"
		if got := c.Classify(nil, output); got != CODE_FAILURE {
			t.Errorf("Classify(nil, %q) = %s, want CODE_FAILURE", output, got)
		}
	})

	// Scope pattern should beat security (tested by having both scope and security keywords)
	t.Run("scope beats security", func(t *testing.T) {
		output := "scope violation: hardcoded credential detected"
		if got := c.Classify(nil, output); got != SCOPE_FAILURE {
			t.Errorf("Classify(nil, %q) = %s, want SCOPE_FAILURE", output, got)
		}
	})

	// Scope sentinel beats everything
	t.Run("scope sentinel beats all", func(t *testing.T) {
		err := fmt.Errorf("%w: something", guard.ErrScopeViolation)
		output := "syntax error: undefined: x"
		if got := c.Classify(err, output); got != SCOPE_FAILURE {
			t.Errorf("Classify(ErrScopeViolation, code output) = %s, want SCOPE_FAILURE", got)
		}
	})
}

func TestClassify_NonScopeError_NoOutput(t *testing.T) {
	c := NewClassifier()
	err := fmt.Errorf("random error without sentinel")
	if got := c.Classify(err, ""); got != UNKNOWN {
		t.Errorf("Classify(random err, \"\") = %s, want UNKNOWN", got)
	}
}

func TestClassify_NilError_CodeOutput(t *testing.T) {
	c := NewClassifier()
	if got := c.Classify(nil, "build failed: exit status 2"); got != CODE_FAILURE {
		t.Errorf("Classify(nil, build output) = %s, want CODE_FAILURE", got)
	}
}

func TestClassify_AddCustomMatcher(t *testing.T) {
	c := NewClassifier()
	// Verify the exported matcher slices are modifiable for extensibility
	c.CodeMatchers = append(c.CodeMatchers, regexp.MustCompile(`(?i)custom code error`))
	if got := c.Classify(nil, "custom code error occurred"); got != CODE_FAILURE {
		t.Errorf("Classify with custom matcher = %s, want CODE_FAILURE", got)
	}
}

func TestNewClassifier_HasDefaultMatchers(t *testing.T) {
	c := NewClassifier()
	if len(c.CodeMatchers) == 0 {
		t.Error("NewClassifier: CodeMatchers is empty")
	}
	if len(c.EnvMatchers) == 0 {
		t.Error("NewClassifier: EnvMatchers is empty")
	}
	if len(c.TestMatchers) == 0 {
		t.Error("NewClassifier: TestMatchers is empty")
	}
	if len(c.ScopeMatchers) == 0 {
		t.Error("NewClassifier: ScopeMatchers is empty")
	}
	if len(c.SecurityMatchers) == 0 {
		t.Error("NewClassifier: SecurityMatchers is empty")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

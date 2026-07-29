package failure

import (
	"errors"
	"regexp"

	"github.com/PizenLabs/izen/internal/controlplane/guard"
)

// ─── Pattern Matchers ─────────────────────────────────────────────────────────

var (
	// codePatterns match compiler, linter, and syntax errors.
	codePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(syntax error|undefined:|cannot find|cannot use|type mismatch)`),
		regexp.MustCompile(`(?i)(not used|declared but not|compilation error|expected ';')`),
		regexp.MustCompile(`(?i)(unexpected\b|redeclared|impossible type|invalid operation)`),
		regexp.MustCompile(`(?i)(invalid memory address|nil pointer dereference|index out of range|divide by zero|slice bounds out of range)`),
		regexp.MustCompile(`(?i)(exit status 2|build failed|compilation failed|compile error)`),
		regexp.MustCompile(`(?i)(golangci-lint|\bvet:|not enough arguments|too many arguments)`),
		regexp.MustCompile(`(?i)(cannot convert|cannot assign|non-name|used as value|is not a type)`),
		regexp.MustCompile(`(?i)(expected operand|expected end of statement|expected \x28|expected \x29)`),
		regexp.MustCompile(`(?i)(missing method|does not implement|interface conversion|assertion error)`),
	}

	// envPatterns match missing binaries, network, and configuration errors.
	envPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(command not found|executable file not found|cannot exec|cannot run)`),
		regexp.MustCompile(`(?i)(no such file or directory|permission denied|required binary|not installed)`),
		regexp.MustCompile(`(?i)(connection refused|no such host|timeout|i/o timeout|no route to host)`),
		regexp.MustCompile(`(?i)(network is unreachable|broken pipe|connection reset)`),
		regexp.MustCompile(`(?i)(environment variable|not set|missing environment|env var)`),
		regexp.MustCompile(`(?i)(not configured|missing config\b|not found in path|contain main module|no main module)`),
		regexp.MustCompile(`(?i)(missing go\.sum|go\.mod file not found|cannot find main module)`),
		regexp.MustCompile(`(?i)(no go files|build constraints exclude|not a directory)`),
	}

	// testPatterns match test framework failures.
	testPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(--- FAIL:|FAIL\s+\S+\s+\S+\s+assertion)`),
		regexp.MustCompile(`(?i)(expected.*but got|got.*want|not equal|assertion failed)`),
		regexp.MustCompile(`(?i)(test case.*failed|test suite.*failed|test failed)`),
		regexp.MustCompile(`(?i)(panic.*test|TestFailed|FAILED|tests.*fail)`),
		regexp.MustCompile(`(?i)FAIL\s+(\S+)\s+[\d.]+s`),
	}

	// scopePatterns are used as string-based fallback when the error does not
	// match the sentinel errors via errors.Is.
	scopePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(scope violation|outside.*scope|not within.*scope)`),
		regexp.MustCompile(`(?i)(budget.*exceeded|exceeds.*budget|budget exhausted)`),
		regexp.MustCompile(`(?i)(file.*outside.*scope|path.*not.*allow|not.*authorized.*file)`),
	}

	// securityPatterns match static analyzer and security tool outputs.
	securityPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(G10[0-9]{1,2}|hardcoded.*(credential|secret|password))`),
		regexp.MustCompile(`(?i)(semgrep|snyk).*(error|fail|violation|finding)`),
		regexp.MustCompile(`(?i)(forbidden pattern|security.*scan.*(error|fail))`),
		regexp.MustCompile(`(?i)(api.?key|secret.*token|password.*hardcoded)`),
		regexp.MustCompile(`(?i)(static.*analysis.*(error|fail)|secrets.*detected)`),
		regexp.MustCompile(`(?i)(gosec|govulncheck|nancy|vulnerability.*(found|detected))`),
	}
)

// ─── Classifier ───────────────────────────────────────────────────────────────

// Classifier categorises a failure by matching error output against known
// patterns. Matching is strict: the first matching pattern wins. If nothing
// matches, the result is UNKNOWN.
type Classifier struct {
	// codeMatchers, envMatchers, testMatchers, scopeMatchers,
	// securityMatchers are the ordered lists of regex patterns. They are
	// exported to allow callers to append custom matchers if needed.
	CodeMatchers     []*regexp.Regexp
	EnvMatchers      []*regexp.Regexp
	TestMatchers     []*regexp.Regexp
	ScopeMatchers    []*regexp.Regexp
	SecurityMatchers []*regexp.Regexp
}

// NewClassifier creates a Classifier with the default pattern matchers.
func NewClassifier() *Classifier {
	cp := make([]*regexp.Regexp, len(codePatterns))
	copy(cp, codePatterns)
	ep := make([]*regexp.Regexp, len(envPatterns))
	copy(ep, envPatterns)
	tp := make([]*regexp.Regexp, len(testPatterns))
	copy(tp, testPatterns)
	sp := make([]*regexp.Regexp, len(scopePatterns))
	copy(sp, scopePatterns)
	secp := make([]*regexp.Regexp, len(securityPatterns))
	copy(secp, securityPatterns)

	return &Classifier{
		CodeMatchers:     cp,
		EnvMatchers:      ep,
		TestMatchers:     tp,
		ScopeMatchers:    sp,
		SecurityMatchers: secp,
	}
}

// Classify determines the FailureClass from an error and its output text.
// The err parameter is checked first via errors.Is for sentinel errors from the
// scope guard. If err is nil or doesn't match a sentinel, the output string is
// matched against pattern groups in priority order: SCOPE_FAILURE (string
// fallback), SECURITY_ISSUE, CODE_FAILURE, ENVIRONMENT_FAILURE, TEST_FAILURE,
// then UNKNOWN.
func (c *Classifier) Classify(err error, output string) FailureClass {
	// 1. Direct error sentinel matching (highest authority)
	if err != nil {
		if errors.Is(err, guard.ErrScopeViolation) || errors.Is(err, guard.ErrBudgetExceeded) {
			return SCOPE_FAILURE
		}
	}

	if output == "" {
		return UNKNOWN
	}

	// 2. String-based scope patterns (fallback if sentinel not used)
	if c.matchAny(output, c.ScopeMatchers) {
		return SCOPE_FAILURE
	}

	// 3. Security patterns
	if c.matchAny(output, c.SecurityMatchers) {
		return SECURITY_ISSUE
	}

	// 4. Code patterns
	if c.matchAny(output, c.CodeMatchers) {
		return CODE_FAILURE
	}

	// 5. Environment patterns
	if c.matchAny(output, c.EnvMatchers) {
		return ENVIRONMENT_FAILURE
	}

	// 6. Test patterns
	if c.matchAny(output, c.TestMatchers) {
		return TEST_FAILURE
	}

	// 7. Fallback: UNKNOWN
	return UNKNOWN
}

// matchAny returns true if the text matches any pattern in the list.
func (c *Classifier) matchAny(text string, patterns []*regexp.Regexp) bool {
	for _, p := range patterns {
		if p.MatchString(text) {
			return true
		}
	}
	return false
}

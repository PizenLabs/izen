package classifier

import (
	"regexp"
	"testing"
)

func TestNewClassifier_HasDefaultMatchers(t *testing.T) {
	fc := NewFailureClassifier()
	if len(fc.matchers) == 0 {
		t.Fatal("NewFailureClassifier() should have default matchers")
	}
}

func TestClassify_CodeFailure_SyntaxError(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("syntax error: unexpected newline in declaration", 1)
	if result.Class != FailureCodeClass {
		t.Errorf("class = %s, want %s", result.Class, FailureCodeClass)
	}
	if result.Reason == "" {
		t.Error("Reason should not be empty")
	}
}

func TestClassify_CodeFailure_Compilation(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("build failed: cannot find package", 2)
	if result.Class != FailureCodeClass {
		t.Errorf("class = %s, want %s", result.Class, FailureCodeClass)
	}
}

func TestClassify_CodeFailure_TypeMismatch(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("cannot use str (variable of type string) as type int in assignment", 1)
	if result.Class != FailureCodeClass {
		t.Errorf("class = %s, want %s", result.Class, FailureCodeClass)
	}
}

func TestClassify_CodeFailure_DeclaredNotUsed(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("x declared but not used", 1)
	if result.Class != FailureCodeClass {
		t.Errorf("class = %s, want %s", result.Class, FailureCodeClass)
	}
}

func TestClassify_CodeFailure_CompileError(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("compile error: expected ';'", 2)
	if result.Class != FailureCodeClass {
		t.Errorf("class = %s, want %s", result.Class, FailureCodeClass)
	}
}

func TestClassify_CodeFailure_ExitStatus(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("exit status 2", 2)
	if result.Class != FailureCodeClass {
		t.Errorf("class = %s, want %s", result.Class, FailureCodeClass)
	}
}

func TestClassify_EnvironmentFailure_Network(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("connection refused: dial tcp 127.0.0.1:8080", 1)
	if result.Class != FailureEnvironmentClass {
		t.Errorf("class = %s, want %s", result.Class, FailureEnvironmentClass)
	}
}

func TestClassify_EnvironmentFailure_NoSuchHost(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("no such host: example.invalid", 1)
	if result.Class != FailureEnvironmentClass {
		t.Errorf("class = %s, want %s", result.Class, FailureEnvironmentClass)
	}
}

func TestClassify_EnvironmentFailure_Timeout(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("i/o timeout after 30s", 1)
	if result.Class != FailureEnvironmentClass {
		t.Errorf("class = %s, want %s", result.Class, FailureEnvironmentClass)
	}
}

func TestClassify_EnvironmentFailure_MissingBinary(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("command not found: python3", 127)
	if result.Class != FailureEnvironmentClass {
		t.Errorf("class = %s, want %s", result.Class, FailureEnvironmentClass)
	}
}

func TestClassify_EnvironmentFailure_FileNotFound(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("open /etc/config.yaml: no such file or directory", 1)
	if result.Class != FailureEnvironmentClass {
		t.Errorf("class = %s, want %s", result.Class, FailureEnvironmentClass)
	}
}

func TestClassify_EnvironmentFailure_PermissionDenied(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("permission denied: /usr/bin/deploy", 126)
	if result.Class != FailureEnvironmentClass {
		t.Errorf("class = %s, want %s", result.Class, FailureEnvironmentClass)
	}
}

func TestClassify_EnvironmentFailure_MissingEnvVar(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("environment variable HOME not set", 1)
	if result.Class != FailureEnvironmentClass {
		t.Errorf("class = %s, want %s", result.Class, FailureEnvironmentClass)
	}
}

func TestClassify_EnvironmentFailure_NotConfigured(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("missing config file: database not configured", 1)
	if result.Class != FailureEnvironmentClass {
		t.Errorf("class = %s, want %s", result.Class, FailureEnvironmentClass)
	}
}

func TestClassify_TestFailure_Assertion(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("assertion failed: expected 5, got 3", 1)
	if result.Class != FailureTestClass {
		t.Errorf("class = %s, want %s", result.Class, FailureTestClass)
	}
}

func TestClassify_TestFailure_FailOutput(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("FAIL    github.com/foo/bar 0.5s", 1)
	if result.Class != FailureTestClass {
		t.Errorf("class = %s, want %s", result.Class, FailureTestClass)
	}
}

func TestClassify_TestFailure_GotWant(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("got 42, want 7", 1)
	if result.Class != FailureTestClass {
		t.Errorf("class = %s, want %s", result.Class, FailureTestClass)
	}
}

func TestClassify_ScopeFailure_Unauthorized(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("unauthorized file mutation: /etc/passwd", 1)
	if result.Class != FailureScopeClass {
		t.Errorf("class = %s, want %s", result.Class, FailureScopeClass)
	}
}

func TestClassify_ScopeFailure_OutsideScope(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("path /root/.ssh/id_rsa is not within scope", 1)
	if result.Class != FailureScopeClass {
		t.Errorf("class = %s, want %s", result.Class, FailureScopeClass)
	}
}

func TestClassify_UnknownFailure(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("everything looks fine", 0)
	if result.Class != FailureUnknownClass {
		t.Errorf("class = %s, want %s", result.Class, FailureUnknownClass)
	}
	if result.Reason == "" {
		t.Error("Reason should not be empty")
	}
}

func TestClassify_NonZeroExit_NoOutput(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("", 1)
	if result.Class != FailureCodeClass {
		t.Errorf("class = %s, want %s", result.Class, FailureCodeClass)
	}
	if result.Reason == "" {
		t.Error("Reason should not be empty")
	}
}

func TestClassify_NonZeroExit_WithOutput(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("something went wrong but not classified", 1)
	if result.Class != FailureCodeClass {
		t.Errorf("class = %s, want %s", result.Class, FailureCodeClass)
	}
}

func TestClassify_EmptyOutput_ZeroExit(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("", 0)
	if result.Class != FailureUnknownClass {
		t.Errorf("class = %s, want %s", result.Class, FailureUnknownClass)
	}
}

func TestClassify_Details(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("syntax error: unexpected token", 1)
	if result.Details == nil {
		t.Fatal("Details should not be nil")
	}
	if result.Details["matched_pattern"] == "" {
		t.Error("Details should contain matched_pattern")
	}
}

func TestClassify_AddMatcher(t *testing.T) {
	fc := NewFailureClassifier()
	pat := regexp.MustCompile(`(?i)custom error`)
	fc.AddMatcher(pat, FailureScopeClass, "custom matcher")
	if len(fc.matchers) < 8 {
		t.Errorf("expected at least 8 matchers after AddMatcher, got %d", len(fc.matchers))
	}
	result := fc.Classify("custom error occurred", 1)
	if result.Class != FailureScopeClass {
		t.Errorf("class = %s, want %s", result.Class, FailureScopeClass)
	}
}

func TestFailureClass_String(t *testing.T) {
	tests := []struct {
		fc   FailureClass
		want string
	}{
		{FailureCodeClass, "code"},
		{FailureEnvironmentClass, "environment"},
		{FailureTestClass, "test"},
		{FailureScopeClass, "scope"},
		{FailureUnknownClass, "unknown"},
		{FailureClass(99), "FailureClass(99)"},
	}
	for _, tc := range tests {
		if got := tc.fc.String(); got != tc.want {
			t.Errorf("FailureClass(%d).String() = %q, want %q", int(tc.fc), got, tc.want)
		}
	}
}

func TestClassificationResult_Fields(t *testing.T) {
	details := map[string]string{"key": "val"}
	r := ClassificationResult{
		Class:   FailureCodeClass,
		Reason:  "test reason",
		Details: details,
	}
	if r.Class != FailureCodeClass {
		t.Errorf("Class = %s, want %s", r.Class, FailureCodeClass)
	}
	if r.Reason != "test reason" {
		t.Errorf("Reason = %q, want %q", r.Reason, "test reason")
	}
	if r.Details["key"] != "val" {
		t.Errorf("Details[key] = %q, want %q", r.Details["key"], "val")
	}
}

func TestClassify_PatternPriority(t *testing.T) {
	fc := NewFailureClassifier()
	result := fc.Classify("test failed: got 42, want 7", 1)
	if result.Class != FailureTestClass {
		t.Errorf("expected test class for test failure output, got %s", result.Class)
	}
}

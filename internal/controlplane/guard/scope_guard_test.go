package guard

import (
	"errors"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
)

func newTestPatch(changes []string, content string) *artifact.PatchArtifact {
	return artifact.NewPatchArtifact(content, changes)
}

func newTestBudget(maxDiffLines int) *budget.MutationBudget {
	return budget.NewBudget(10, maxDiffLines, 8000, 3, 5*time.Minute, 20)
}

func TestValidatePatch_WithinScope_ByChanges(t *testing.T) {
	scope := &ScopeDeclaration{
		AllowedFiles: []string{"internal/handler/user.go"},
	}
	sg := NewScopeGuard(scope, nil)
	patch := newTestPatch(
		[]string{"internal/handler/user.go"},
		"",
	)
	if err := sg.ValidatePatch(patch); err != nil {
		t.Errorf("ValidatePatch = %v, want nil", err)
	}
}

func TestValidatePatch_WithinScope_ByPatchContent(t *testing.T) {
	scope := &ScopeDeclaration{
		AllowedFiles: []string{"pkg/orders/calculator.go"},
	}
	sg := NewScopeGuard(scope, nil)
	patch := newTestPatch(
		nil,
		`--- a/pkg/orders/calculator.go
+++ b/pkg/orders/calculator.go
@@ -1,5 +1,7 @@
 package orders

+// NewOrder creates a new order.
+func NewOrder() Order {
+	return Order{}
+}
`,
	)
	if err := sg.ValidatePatch(patch); err != nil {
		t.Errorf("ValidatePatch = %v, want nil", err)
	}
}

func TestValidatePatch_WithinScope_GitStyleDiff(t *testing.T) {
	scope := &ScopeDeclaration{
		AllowedFiles: []string{"src/main.go"},
	}
	sg := NewScopeGuard(scope, nil)
	patch := newTestPatch(
		nil,
		`diff --git a/src/main.go b/src/main.go
--- a/src/main.go
+++ b/src/main.go
@@ -2,6 +2,7 @@ package main

 func main() {
+	println("hello")
 }
`,
	)
	if err := sg.ValidatePatch(patch); err != nil {
		t.Errorf("ValidatePatch = %v, want nil", err)
	}
}

func TestValidatePatch_ScopeViolation(t *testing.T) {
	scope := &ScopeDeclaration{
		AllowedFiles: []string{"internal/handler/user.go"},
	}
	sg := NewScopeGuard(scope, nil)
	patch := newTestPatch(
		[]string{"internal/auth/jwt.go"},
		"",
	)
	err := sg.ValidatePatch(patch)
	if err == nil {
		t.Fatal("ValidatePatch: expected error, got nil")
	}
	if !errors.Is(err, ErrScopeViolation) {
		t.Errorf("ValidatePatch: want ErrScopeViolation, got %T: %v", err, err)
	}
}

func TestValidatePatch_ScopeViolation_Mixed(t *testing.T) {
	scope := &ScopeDeclaration{
		AllowedFiles: []string{"internal/handler/user.go"},
	}
	sg := NewScopeGuard(scope, nil)
	patch := newTestPatch(
		[]string{"internal/handler/user.go"},
		`--- a/internal/auth/jwt.go
+++ b/internal/auth/jwt.go
@@ -1 +1 @@
-foo
+bar`,
	)
	err := sg.ValidatePatch(patch)
	if err == nil {
		t.Fatal("ValidatePatch: expected error, got nil")
	}
	if !errors.Is(err, ErrScopeViolation) {
		t.Errorf("ValidatePatch: want ErrScopeViolation, got %T: %v", err, err)
	}
}

func TestValidatePatch_BudgetExceeded(t *testing.T) {
	scope := &ScopeDeclaration{
		AllowedFiles: []string{"internal/handler/user.go"},
	}
	bgt := newTestBudget(5)
	sg := NewScopeGuard(scope, bgt)
	patch := newTestPatch(
		[]string{"internal/handler/user.go"},
		`--- a/internal/handler/user.go
+++ b/internal/handler/user.go
@@ -1,5 +1,15 @@
-func old() {}
+func new() {
+  line1
+  line2
+  line3
+  line4
+  line5
+  line6
+  line7
+  line8
+}
`,
	)
	err := sg.ValidatePatch(patch)
	if err == nil {
		t.Fatal("ValidatePatch: expected budget error, got nil")
	}
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("ValidatePatch: want ErrBudgetExceeded, got %T: %v", err, err)
	}
}

func TestValidatePatch_BudgetNotExceeded(t *testing.T) {
	scope := &ScopeDeclaration{
		AllowedFiles: []string{"internal/handler/user.go"},
	}
	bgt := newTestBudget(10)
	sg := NewScopeGuard(scope, bgt)
	patch := newTestPatch(
		[]string{"internal/handler/user.go"},
		`--- a/internal/handler/user.go
+++ b/internal/handler/user.go
@@ -1,5 +1,7 @@
-func old() {}
+func new() {
+  line1
+  line2
+}
`,
	)
	if err := sg.ValidatePatch(patch); err != nil {
		t.Errorf("ValidatePatch = %v, want nil", err)
	}
}

func TestValidatePatch_NoScope_Error(t *testing.T) {
	sg := &ScopeGuard{budget: nil, scope: nil}
	patch := newTestPatch([]string{"foo.go"}, "")
	err := sg.ValidatePatch(patch)
	if err == nil {
		t.Fatal("ValidatePatch: expected error for nil scope, got nil")
	}
}

func TestValidatePatch_NilPatch_Error(t *testing.T) {
	scope := &ScopeDeclaration{AllowedFiles: []string{"foo.go"}}
	sg := NewScopeGuard(scope, nil)
	err := sg.ValidatePatch(nil)
	if err == nil {
		t.Fatal("ValidatePatch: expected error for nil patch, got nil")
	}
}

func TestValidatePatch_EmptyPatch_Error(t *testing.T) {
	scope := &ScopeDeclaration{AllowedFiles: []string{"foo.go"}}
	sg := NewScopeGuard(scope, nil)
	patch := newTestPatch(nil, "")
	err := sg.ValidatePatch(patch)
	if err == nil {
		t.Fatal("ValidatePatch: expected error for empty patch, got nil")
	}
}

func TestValidatePatch_GlobPattern(t *testing.T) {
	scope := &ScopeDeclaration{
		AllowedFiles: []string{"pkg/orders/*.go"},
	}
	sg := NewScopeGuard(scope, nil)

	tests := []struct {
		path string
		want bool
		desc string
	}{
		{"pkg/orders/calculator.go", true, "matching glob"},
		{"pkg/orders/types.go", true, "matching glob types"},
		{"pkg/auth/jwt.go", false, "non-matching directory"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			patch := newTestPatch([]string{tc.path}, "")
			err := sg.ValidatePatch(patch)
			if tc.want && err != nil {
				t.Errorf("ValidatePatch(%q) = %v, want nil", tc.path, err)
			}
			if !tc.want && err == nil {
				t.Errorf("ValidatePatch(%q) = nil, want error", tc.path)
			}
		})
	}
}

func TestValidatePatch_RecursiveDirectoryPattern(t *testing.T) {
	scope := &ScopeDeclaration{
		AllowedFiles: []string{"internal/handler/..."},
	}
	sg := NewScopeGuard(scope, nil)

	tests := []struct {
		path string
		want bool
		desc string
	}{
		{"internal/handler/user.go", true, "direct child"},
		{"internal/handler/admin/roles.go", true, "nested child"},
		{"internal/handler.go", false, "sibling file"},
		{"internal/auth/jwt.go", false, "different branch"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			patch := newTestPatch([]string{tc.path}, "")
			err := sg.ValidatePatch(patch)
			if tc.want && err != nil {
				t.Errorf("ValidatePatch(%q) = %v, want nil", tc.path, err)
			}
			if !tc.want && err == nil {
				t.Errorf("ValidatePatch(%q) = nil, want error", tc.path)
			}
		})
	}
}

func TestExtractFilesFromPatch_ChangesOnly(t *testing.T) {
	patch := newTestPatch([]string{"a/b/c.go", "d/e/f.go"}, "")
	files := extractFilesFromPatch(patch)
	if len(files) != 2 {
		t.Fatalf("extractFilesFromPatch = %d files, want 2", len(files))
	}
}

func TestExtractFilesFromPatch_DiffOnly(t *testing.T) {
	content := `--- a/src/a.go
+++ b/src/a.go
@@ -1 +1 @@
-foo
+bar`
	patch := newTestPatch(nil, content)
	files := extractFilesFromPatch(patch)
	if len(files) != 1 {
		t.Fatalf("extractFilesFromPatch = %d files, want 1", len(files))
	}
	if files[0] != "src/a.go" {
		t.Errorf("extractFilesFromPatch = %q, want %q", files[0], "src/a.go")
	}
}

func TestExtractFilesFromPatch_Deduplicates(t *testing.T) {
	content := `--- a/pkg/x.go
+++ b/pkg/x.go`
	patch := newTestPatch([]string{"pkg/x.go"}, content)
	files := extractFilesFromPatch(patch)
	if len(files) != 1 {
		t.Errorf("extractFilesFromPatch = %d files, want 1 (deduplicated)", len(files))
	}
}

func TestCountDiffLines(t *testing.T) {
	tests := []struct {
		content string
		want    int
		desc    string
	}{
		{"", 0, "empty content"},
		{
			`--- a/x.go
+++ b/x.go
@@ -1 +1 @@
-foo
+bar`,
			2, "simple add/remove",
		},
		{
			`context line
 another context`,
			0, "only context lines"},
		{
			`--- a/x.go
+++ b/x.go
@@ -1,5 +1,8 @@
-func old() {}
+func new() {
+  line1
+  line2
+  line3
+}`,
			6, "mixed diff with header",
		},
		{
			`+added
+also added
 context
-removed`,
			3, "mixed lines",
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := countDiffLines(tc.content); got != tc.want {
				t.Errorf("countDiffLines = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestMatchPath(t *testing.T) {
	tests := []struct {
		path    string
		pattern string
		want    bool
		desc    string
	}{
		{"foo.go", "foo.go", true, "exact match"},
		{"foo.go", "bar.go", false, "exact mismatch"},
		{"pkg/orders/*.go", "pkg/orders/calc.go", false, "reversed args"},
		{"pkg/orders/calc.go", "pkg/orders/*.go", true, "glob match"},
		{"pkg/orders/calc.go", "pkg/orders/*.py", false, "glob mismatch"},
		{"a/b/c/d.go", "a/...", true, "recursive prefix"},
		{"a/b.go", "a/...", true, "recursive prefix direct"},
		{"b/c/d.go", "a/...", false, "recursive prefix not matching"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := matchPath(tc.path, tc.pattern); got != tc.want {
				t.Errorf("matchPath(%q, %q) = %v, want %v", tc.path, tc.pattern, got, tc.want)
			}
		})
	}
}

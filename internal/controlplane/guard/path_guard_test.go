package guard

import (
	"errors"
	"testing"
)

func TestValidateMutationTarget_RejectsReservedKeywords(t *testing.T) {
	sg := NewScopeGuard(&ScopeDeclaration{AllowedFiles: []string{"any"}}, nil)

	tests := []struct {
		target string
		desc   string
	}{
		{"workspace", "bare workspace"},
		{"Workspace", "capitalized Workspace"},
		{"WORKSPACE", "uppercase WORKSPACE"},
		{"workspace/", "workspace with trailing slash"},
		{"root", "bare root"},
		{"cwd", "bare cwd"},
		{".", "dot only"},
		{"/", "root slash"},
		{"[n/a]", "bracket placeholder"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			err := sg.ValidateMutationTarget(tc.target, nil)
			if err == nil {
				t.Errorf("ValidateMutationTarget(%q) = nil, want error", tc.target)
				return
			}
			if !errors.Is(err, ErrReservedTarget) {
				t.Errorf("ValidateMutationTarget(%q) = %v, want ErrReservedTarget", tc.target, err)
			}
		})
	}
}

func TestValidateMutationTarget_RejectsReservedWithExtension(t *testing.T) {
	sg := NewScopeGuard(&ScopeDeclaration{AllowedFiles: []string{"any"}}, nil)

	tests := []struct {
		target string
		desc   string
	}{
		{"workspace.go", "workspace.go file"},
		{"root.js", "root.js file"},
		{"cwd.py", "cwd.py file"},
		{"workspace.html", "workspace.html file"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			err := sg.ValidateMutationTarget(tc.target, nil)
			if err == nil {
				t.Errorf("ValidateMutationTarget(%q) = nil, want error", tc.target)
				return
			}
			if !errors.Is(err, ErrReservedTarget) {
				t.Errorf("ValidateMutationTarget(%q) = %v, want ErrReservedTarget", tc.target, err)
			}
		})
	}
}

func TestValidateMutationTarget_RejectsNestedReserved(t *testing.T) {
	sg := NewScopeGuard(&ScopeDeclaration{AllowedFiles: []string{"any"}}, nil)

	tests := []struct {
		target string
		desc   string
	}{
		{"src/workspace", "nested workspace dir"},
		{"project/root/file.go", "path containing root"},
		{"lib/cwd/utils.go", "path containing cwd"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			err := sg.ValidateMutationTarget(tc.target, nil)
			if err == nil {
				t.Errorf("ValidateMutationTarget(%q) = nil, want error", tc.target)
				return
			}
			if !errors.Is(err, ErrReservedTarget) {
				t.Errorf("ValidateMutationTarget(%q) = %v, want ErrReservedTarget", tc.target, err)
			}
		})
	}
}

func TestValidateMutationTarget_AcceptsValidPaths(t *testing.T) {
	sg := NewScopeGuard(&ScopeDeclaration{AllowedFiles: []string{"any"}}, nil)

	tests := []struct {
		target string
		desc   string
	}{
		{"web/templates/header.html", "normal file path"},
		{"internal/handler/user.go", "go source file"},
		{"src/index.js", "js file"},
		{"README.md", "readme"},
		{"config.yaml", "yaml config"},
		{"pkg/orders/calculator.go", "deeply nested file"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			err := sg.ValidateMutationTarget(tc.target, nil)
			if err != nil {
				t.Errorf("ValidateMutationTarget(%q) = %v, want nil", tc.target, err)
			}
		})
	}
}

func TestValidateMutationTarget_PlannedTargetsScope(t *testing.T) {
	sg := NewScopeGuard(&ScopeDeclaration{}, nil)

	tests := []struct {
		target         string
		plannedTargets []string
		wantErr        bool
		errIs          error
		desc           string
	}{
		{"web/templates/header.html", []string{"web/templates/header.html"}, false, nil, "exact match"},
		{"web/templates/header.html", []string{"web/templates/*.html"}, false, nil, "glob match"},
		{"web/templates/header.html", []string{"web/..."}, false, nil, "recursive prefix match"},
		{"internal/auth/jwt.go", []string{"web/templates/header.html"}, true, ErrScopeViolation, "not in planned targets"},
		{"workspace", []string{"web/templates/header.html"}, true, ErrReservedTarget, "reserved keyword beats scope check"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			err := sg.ValidateMutationTarget(tc.target, tc.plannedTargets)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ValidateMutationTarget(%q, %v) = nil, want error", tc.target, tc.plannedTargets)
					return
				}
				if tc.errIs != nil && !errors.Is(err, tc.errIs) {
					t.Errorf("ValidateMutationTarget(%q, %v) = %v, want %v", tc.target, tc.plannedTargets, err, tc.errIs)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateMutationTarget(%q, %v) = %v, want nil", tc.target, tc.plannedTargets, err)
			}
		})
	}
}

func TestValidateMutationTarget_EmptyPlannedTargets(t *testing.T) {
	sg := NewScopeGuard(&ScopeDeclaration{}, nil)

	// When plannedTargets is nil/empty, only reserved keyword check applies.
	if err := sg.ValidateMutationTarget("any/path.go", nil); err != nil {
		t.Errorf("ValidateMutationTarget with nil planned = %v, want nil", err)
	}
	if err := sg.ValidateMutationTarget("any/path.go", []string{}); err != nil {
		t.Errorf("ValidateMutationTarget with empty planned = %v, want nil", err)
	}
}

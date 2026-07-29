package retrieval

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func setupTestWorkspace(t *testing.T) (string, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "izen-target-resolver-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	// Create a realistic directory structure.
	dirs := []string{
		"web/templates",
		"web/static",
		"internal/handler",
		"internal/auth",
		"pkg/orders",
	}
	files := map[string]string{
		"web/templates/header.html": "<header>Header</header>",
		"web/templates/footer.html": "<footer>Footer</footer>",
		"web/static/style.css":      "body { margin: 0; }",
		"internal/handler/user.go":  "package handler\nfunc GetUser() {}",
		"internal/handler/order.go": "package handler\nfunc GetOrder() {}",
		"internal/auth/jwt.go":      "package auth\nfunc Validate() {}",
		"pkg/orders/calculator.go":  "package orders\nfunc Calculate() {}",
		"pkg/orders/types.go":       "package orders\ntype Order struct{}",
	}

	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", d, err)
		}
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write file %s: %v", path, err)
		}
	}

	return dir, func() { _ = os.RemoveAll(dir) }
}

func TestTargetPathResolver_Resolve_ExactPath(t *testing.T) {
	dir, cleanup := setupTestWorkspace(t)
	defer cleanup()

	resolver := NewTargetPathResolver(dir)
	ctx := context.Background()

	resolved, err := resolver.Resolve(ctx, "web/templates/header.html")
	if err != nil {
		t.Fatalf("Resolve(header.html) = %v, want nil", err)
	}
	if resolved != "web/templates/header.html" {
		t.Errorf("Resolve = %q, want %q", resolved, "web/templates/header.html")
	}
}

func TestTargetPathResolver_Resolve_FuzzyFilename(t *testing.T) {
	dir, cleanup := setupTestWorkspace(t)
	defer cleanup()

	resolver := NewTargetPathResolver(dir)
	ctx := context.Background()

	// Fuzzy match: only the base filename is given, resolver should find it.
	resolved, err := resolver.Resolve(ctx, "header.html")
	if err != nil {
		t.Fatalf("Resolve(header.html) = %v, want nil", err)
	}
	if resolved != "web/templates/header.html" {
		t.Errorf("Resolve = %q, want %q", resolved, "web/templates/header.html")
	}
}

func TestTargetPathResolver_Resolve_FuzzyPartialPath(t *testing.T) {
	dir, cleanup := setupTestWorkspace(t)
	defer cleanup()

	resolver := NewTargetPathResolver(dir)
	ctx := context.Background()

	resolved, err := resolver.Resolve(ctx, "handler/user.go")
	if err != nil {
		t.Fatalf("Resolve(handler/user.go) = %v, want nil", err)
	}
	if resolved != "internal/handler/user.go" {
		t.Errorf("Resolve = %q, want %q", resolved, "internal/handler/user.go")
	}
}

func TestTargetPathResolver_Resolve_NotFound(t *testing.T) {
	dir, cleanup := setupTestWorkspace(t)
	defer cleanup()

	resolver := NewTargetPathResolver(dir)
	ctx := context.Background()

	_, err := resolver.Resolve(ctx, "nonexistent/file.go")
	if err == nil {
		t.Fatal("Resolve(nonexistent) = nil, want error")
	}
}

func TestTargetPathResolver_Resolve_EmptyPath(t *testing.T) {
	dir, cleanup := setupTestWorkspace(t)
	defer cleanup()

	resolver := NewTargetPathResolver(dir)
	ctx := context.Background()

	_, err := resolver.Resolve(ctx, "")
	if err == nil {
		t.Fatal("Resolve('') = nil, want error")
	}
}

func TestTargetPathResolver_Resolve_RootPaths(t *testing.T) {
	dir, cleanup := setupTestWorkspace(t)
	defer cleanup()

	resolver := NewTargetPathResolver(dir)
	ctx := context.Background()

	tests := []struct {
		path string
		desc string
	}{
		{".", "dot only"},
		{"/", "root slash"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := resolver.Resolve(ctx, tc.path)
			if err == nil {
				t.Errorf("Resolve(%q) = nil, want error", tc.path)
			}
		})
	}
}

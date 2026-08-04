package risk

import (
	"testing"

	"github.com/PizenLabs/izen/internal/lea"
)

func TestTier_String(t *testing.T) {
	tests := []struct {
		tier Tier
		want string
	}{
		{Tier0, "Tier0"},
		{Tier1, "Tier1"},
		{Tier2, "Tier2"},
		{Tier3, "Tier3"},
		{Tier(99), "TierUnknown"},
	}
	for _, tc := range tests {
		if got := tc.tier.String(); got != tc.want {
			t.Errorf("Tier(%d).String() = %q, want %q", int(tc.tier), got, tc.want)
		}
	}
}

func TestClassify_EmptyTargets(t *testing.T) {
	c := NewClassifier(nil)
	if got := c.Classify(nil); got != Tier0 {
		t.Errorf("Classify(nil) = %s, want Tier0", got)
	}
	if got := c.Classify([]Target{}); got != Tier0 {
		t.Errorf("Classify([]) = %s, want Tier0", got)
	}
}

func TestClassify_Tier0(t *testing.T) {
	c := NewClassifier(nil)
	tests := []struct {
		path string
		desc string
	}{
		{"README.md", "markdown file"},
		{"docs/guide.txt", "text file"},
		{"CHANGELOG.rst", "rst file"},
		{"docs/index.adoc", "asciidoc"},
		{"config.yaml", "yaml config"},
		{".gitignore", "gitignore"},
		{".editorconfig", "editorconfig"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			targets := []Target{{File: tc.path}}
			if got := c.Classify(targets); got != Tier0 {
				t.Errorf("Classify(%q) = %s, want Tier0", tc.path, got)
			}
		})
	}
}

func TestClassify_Tier1(t *testing.T) {
	c := NewClassifier(nil)
	tests := []struct {
		path string
		desc string
	}{
		{"auth_test.go", "Go test file in auth dir"},
		{"handler_spec.rb", "Ruby spec file"},
		{"styles.css", "CSS file"},
		{"themes/dark.scss", "SCSS file"},
		{"components/button.less", "LESS file"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			targets := []Target{{File: tc.path}}
			if got := c.Classify(targets); got != Tier1 {
				t.Errorf("Classify(%q) = %s, want Tier1", tc.path, got)
			}
		})
	}
}

func TestClassify_Tier2_Default(t *testing.T) {
	c := NewClassifier(nil)
	tests := []struct {
		path string
		desc string
	}{
		{"main.go", "Go source file"},
		{"internal/handler/handler.go", "handler source"},
		{"pkg/orders/orders.go", "orders package"},
		{"src/components/Button.tsx", "React component"},
		{"lib/core.py", "Python core module"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			targets := []Target{{File: tc.path}}
			if got := c.Classify(targets); got != Tier2 {
				t.Errorf("Classify(%q) = %s, want Tier2", tc.path, got)
			}
		})
	}
}

func TestClassify_Tier3_PathPatterns(t *testing.T) {
	c := NewClassifier(nil)
	tests := []struct {
		path string
		desc string
	}{
		{"internal/auth/jwt.go", "auth dir"},
		{"auth/token.go", "auth subdir"},
		{"pkg/auth/oauth.go", "auth package"},
		{"crypto/aes.go", "crypto dir"},
		{"internal/crypto/keys.go", "nested crypto"},
		{"security/policy.go", "security dir"},
		{"db/migrations/001_init.sql", "db migration"},
		{"internal/db/migrations/002_add_users.sql", "nested migration"},
		{"permissions/rbac.go", "permissions dir"},
		{"network/proxy.go", "network dir"},
		{"pkg/networking/tls.go", "networking package"},
		{"/etc/nginx/nginx.conf", "etc system file"},
		{"usr/local/bin/deploy.sh", "usr system file"},
		{"Dockerfile", "Dockerfile root"},
		{"deploy/Dockerfile", "Dockerfile nested"},
		{".github/workflows/ci.yml", "GH workflows"},
		{"Jenkinsfile", "Jenkinsfile"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			targets := []Target{{File: tc.path}}
			if got := c.Classify(targets); got != Tier3 {
				t.Errorf("Classify(%q) = %s, want Tier3", tc.path, got)
			}
		})
	}
}

func TestClassify_MixedTargets_Escalates(t *testing.T) {
	c := NewClassifier(nil)
	// Even one Tier 3 file among many Tier 2 files escalates the whole batch
	targets := []Target{
		{File: "main.go"},
		{File: "internal/handler/user.go"},
		{File: "internal/auth/jwt.go"},
		{File: "pkg/utils/helper.go"},
	}
	if got := c.Classify(targets); got != Tier3 {
		t.Errorf("Classify with auth file = %s, want Tier3", got)
	}
}

func TestClassify_Tier3_ShortCircuits(t *testing.T) {
	c := NewClassifier(nil)
	// The third target is Tier 3; classification should short-circuit before
	// evaluating the fourth (which is Tier 2), but result is Tier 3 regardless.
	targets := []Target{
		{File: "README.md"},            // Tier 0
		{File: "handler_test.go"},      // Tier 1
		{File: "internal/auth/key.go"}, // Tier 3
		{File: "pkg/core/engine.go"},   // Tier 2 (would be evaluated if not short-circuited)
	}
	if got := c.Classify(targets); got != Tier3 {
		t.Errorf("Classify = %s, want Tier3", got)
	}
}

func TestClassify_FilePathNormalization(t *testing.T) {
	c := NewClassifier(nil)
	tests := []struct {
		path string
		want Tier
		desc string
	}{
		{"./internal/auth/jwt.go", Tier3, "leading dot slash"},
		{"internal//auth//jwt.go", Tier3, "double slashes"},
		{"./README.md", Tier0, "markdown with leading dot"},
		{"foo/../auth/jwt.go", Tier3, "path traversal to auth"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			targets := []Target{{File: tc.path}}
			if got := c.Classify(targets); got != tc.want {
				t.Errorf("Classify(%q) = %s, want %s", tc.path, got, tc.want)
			}
		})
	}
}

func TestClassify_SymbolImpactEscalation(t *testing.T) {
	g := lea.NewFileGraph("testroot")

	// Create a Tier 2 file that defines exported symbol ComputeTotal
	g.AddFile(lea.FileNode{
		Path:    "pkg/orders/calculator.go",
		Package: "orders",
		Symbols: []lea.Symbol{
			{Name: "ComputeTotal", Kind: lea.SymbolFunction, File: "pkg/orders/calculator.go", Exported: true},
		},
		Imports: []string{},
	})

	// Create a Tier 3 file (matches auth pattern) that imports orders and
	// references the symbol
	g.AddFile(lea.FileNode{
		Path:    "internal/auth/handler.go",
		Package: "auth",
		Symbols: []lea.Symbol{
			{Name: "ComputeTotal", Kind: lea.SymbolFunction, File: "internal/auth/handler.go", Exported: false, Parent: "AuthHandler"},
		},
		Imports: []string{
			"github.com/PizenLabs/izen/pkg/orders",
		},
	})

	c := NewClassifier(g)

	t.Run("escalates when symbol is referenced by Tier 3 file", func(t *testing.T) {
		targets := []Target{{
			File:   "pkg/orders/calculator.go",
			Symbol: "ComputeTotal",
		}}
		if got := c.Classify(targets); got != Tier3 {
			t.Errorf("Classify with symbol referenced by auth = %s, want Tier3", got)
		}
	})

	t.Run("no escalation when symbol not found in graph", func(t *testing.T) {
		targets := []Target{{
			File:   "pkg/orders/calculator.go",
			Symbol: "UnknownSymbol",
		}}
		if got := c.Classify(targets); got != Tier2 {
			t.Errorf("Classify with unknown symbol = %s, want Tier2", got)
		}
	})

	t.Run("no escalation without graph", func(t *testing.T) {
		c2 := NewClassifier(nil)
		targets := []Target{{
			File:   "pkg/orders/calculator.go",
			Symbol: "ComputeTotal",
		}}
		if got := c2.Classify(targets); got != Tier2 {
			t.Errorf("Classify without graph = %s, want Tier2", got)
		}
	})
}

func TestClassify_SymbolNoEscalationWhenNotReferenced(t *testing.T) {
	g := lea.NewFileGraph("testroot")
	g.AddFile(lea.FileNode{
		Path:    "pkg/orders/calculator.go",
		Package: "orders",
		Symbols: []lea.Symbol{
			{Name: "ComputeTotal", Kind: lea.SymbolFunction, File: "pkg/orders/calculator.go", Exported: true},
		},
	})

	c := NewClassifier(g)
	targets := []Target{{
		File:   "pkg/orders/calculator.go",
		Symbol: "ComputeTotal",
	}}
	if got := c.Classify(targets); got != Tier2 {
		t.Errorf("Classify without Tier 3 dependent = %s, want Tier2", got)
	}
}

func TestClassify_SymbolEscalationWithPathTraversal(t *testing.T) {
	g := lea.NewFileGraph("testroot")
	g.AddFile(lea.FileNode{
		Path:    "pkg/orders/calculator.go",
		Package: "orders",
		Symbols: []lea.Symbol{
			{Name: "CalculateTax", Kind: lea.SymbolFunction, File: "pkg/orders/calculator.go", Exported: true},
		},
	})
	g.AddFile(lea.FileNode{
		Path:    "internal/auth/tax.go",
		Package: "auth",
		Symbols: []lea.Symbol{
			{Name: "CalculateTax", Kind: lea.SymbolFunction, File: "internal/auth/tax.go", Exported: false},
		},
		Imports: []string{"github.com/PizenLabs/izen/pkg/orders"},
	})

	c := NewClassifier(g)
	targets := []Target{{
		File:   "pkg/orders/calculator.go",
		Symbol: "CalculateTax",
	}}
	if got := c.Classify(targets); got != Tier3 {
		t.Errorf("Classify with auth-dependent symbol = %s, want Tier3", got)
	}
}

func TestMatchImport(t *testing.T) {
	tests := []struct {
		importPath string
		pkg        string
		want       bool
		desc       string
	}{
		{"github.com/PizenLabs/izen/pkg/orders", "orders", true, "exact suffix match"},
		{"orders", "orders", true, "bare package match"},
		{"fmt", "orders", false, "different package"},
		{"", "orders", false, "empty import"},
		{"github.com/foo/bar", "", false, "empty package"},
		{"github.com/PizenLabs/izen/pkg/order", "orders", false, "prefix but not exact suffix"},
		{"github.com/PizenLabs/izen/pkg/net/orders", "orders", true, "nested suffix match"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			if got := matchImport(tc.importPath, tc.pkg); got != tc.want {
				t.Errorf("matchImport(%q, %q) = %v, want %v", tc.importPath, tc.pkg, got, tc.want)
			}
		})
	}
}

package extractors

import (
	"testing"

	sympkg "github.com/PizenLabs/izen/internal/retrieval/symbol"
)

func assertTrue(t *testing.T, cond bool, msg string) {
	t.Helper()
	if !cond {
		t.Errorf("expected true: %s", msg)
	}
}

func assertFalse(t *testing.T, cond bool, msg string) {
	t.Helper()
	if cond {
		t.Errorf("expected false: %s", msg)
	}
}

func assertEqual(t *testing.T, expected, actual interface{}) {
	t.Helper()
	if expected != actual {
		t.Errorf("expected %v, got %v", expected, actual)
	}
}

func TestShouldIgnoreDir(t *testing.T) {
	dirs := []string{
		".git", ".idea", ".vscode", "node_modules", "venv", ".venv",
		"env", "__pycache__", "target", "vendor", "build", "dist",
		".next", "bin", "obj",
	}
	for _, d := range dirs {
		t.Run(d, func(t *testing.T) {
			assertTrue(t, sympkg.ShouldIgnoreDir(d), "should ignore "+d)
		})
	}

	valid := []string{"src", "lib", "internal", "cmd", "pkg"}
	for _, d := range valid {
		t.Run(d, func(t *testing.T) {
			assertFalse(t, sympkg.ShouldIgnoreDir(d), "should not ignore "+d)
		})
	}
}

func TestShouldIgnorePath(t *testing.T) {
	root := "/home/user/project"
	cases := []struct {
		path     string
		expected bool
	}{
		{"/home/user/project/src/main.go", false},
		{"/home/user/project/.git/config", true},
		{"/home/user/project/node_modules/pkg/lib.js", true},
		{"/home/user/project/venv/lib/python3/site-packages/foo.py", true},
		{"/home/user/project/.venv/bin/python", true},
		{"/home/user/project/env/lib/libc.so", true},
		{"/home/user/project/__pycache__/module.cpython-311.pyc", true},
		{"/home/user/project/target/release/app", true},
		{"/home/user/project/vendor/github.com/pkg/lib.go", true},
		{"/home/user/project/build/output.js", true},
		{"/home/user/project/dist/bundle.js", true},
		{"/home/user/project/.next/server/pages/index.js", true},
		{"/home/user/project/bin/exec", true},
		{"/home/user/project/obj/Debug/lib.a", true},
		{"/home/user/project/internal/ui/workspace.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			result := sympkg.ShouldIgnorePath(tc.path, root)
			if result != tc.expected {
				t.Errorf("ShouldIgnorePath(%q) = %v, want %v", tc.path, result, tc.expected)
			}
		})
	}
}

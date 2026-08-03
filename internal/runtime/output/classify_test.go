package output

import "testing"

func TestClassifyProgrammaticInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want ToolType
	}{
		{"go", []string{"test", "./..."}, ToolGoTest},
		{"go", []string{"test", "-v", "-run=TestFoo", "./..."}, ToolGoTest},
		{"go", []string{"build", "./..."}, ToolGeneric},
		{"go", []string{"vet", "./..."}, ToolLinterGo},
		{"cargo", []string{"test", "--", "--nocapture"}, ToolRustTest},
		{"cargo", []string{"build"}, ToolGeneric},
		{"git", []string{"status"}, ToolGitStatus},
		{"git", []string{"status", "--porcelain"}, ToolGitStatus},
		{"git", []string{"diff"}, ToolGeneric},
		{"golangci-lint", []string{"run"}, ToolLinterGo},
		{"staticcheck", []string{"./..."}, ToolLinterGo},
		{"revive", []string{"./..."}, ToolLinterGo},
		{"bash", []string{"-c", "ls"}, ToolGeneric},
		{"/usr/local/bin/golangci-lint", []string{"run"}, ToolLinterGo},
	}
	for _, c := range cases {
		if got := Classify(c.name, c.args); got != c.want {
			t.Errorf("Classify(%q, %v) = %s, want %s", c.name, c.args, got, c.want)
		}
	}
}

func TestClassifyCommandString(t *testing.T) {
	cases := []struct {
		command string
		want    ToolType
	}{
		{"go test ./...", ToolGoTest},
		{"go test -v -race ./internal/...", ToolGoTest},
		{"bash -c \"go test ./... 2>&1\"", ToolGoTest},
		{"sh -c 'go test -run=Foo ./...'", ToolGoTest},
		{"go test ./...; echo done", ToolGoTest},
		{"go build ./...", ToolGeneric},
		{"cargo test", ToolRustTest},
		{"cargo test -- --nocapture", ToolRustTest},
		{"git status", ToolGitStatus},
		{"git status --porcelain", ToolGitStatus},
		{"git diff HEAD~1", ToolGeneric},
		{"golangci-lint run ./...", ToolLinterGo},
		{"go vet ./...", ToolLinterGo},
		{"staticcheck ./...", ToolLinterGo},
		{"echo hello", ToolGeneric},
		{"", ToolGeneric},
	}
	for _, c := range cases {
		if got := ClassifyCommand(c.command); got != c.want {
			t.Errorf("ClassifyCommand(%q) = %s, want %s", c.command, got, c.want)
		}
	}
}

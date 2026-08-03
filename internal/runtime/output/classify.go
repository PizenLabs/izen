package output

import (
	"path/filepath"
	"strings"
)

// goLinters are the Go linter binaries whose output the Semantic Compressor
// flattens into uniform <file>:<line>:<col>: [<rule>] <message> lines.
var goLinters = []string{
	"golangci-lint",
	"staticcheck",
	"revive",
	"golint",
	"govet",
	"errcheck",
	"ineffassign",
}

// ClassifyCommand inspects a command string and tags the execution context. It
// first tries the leading binary name plus its arguments, then falls back to a
// substring scan of the whole invocation so `bash -c "go test ./..."`-style
// shell wrappers are classified correctly.
func ClassifyCommand(command string) ToolType {
	fields := strings.Fields(command)
	if len(fields) > 0 {
		if t := Classify(fields[0], fields[1:]); t != ToolGeneric {
			return t
		}
	}
	return classifyInvocation(command)
}

// Classify tags the execution context from a programmatic invocation: the
// binary name and its arguments. Callers that already split the command
// (exec.Command("go", "test", ...)) should use this directly.
func Classify(name string, args []string) ToolType {
	bin := filepath.Base(name)
	switch bin {
	case "go":
		if hasArg(args, "test") {
			return ToolGoTest
		}
		if hasArg(args, "vet") {
			return ToolLinterGo
		}
	case "cargo":
		if hasArg(args, "test") {
			return ToolRustTest
		}
	case "git":
		if hasArg(args, "status") {
			return ToolGitStatus
		}
	}
	for _, l := range goLinters {
		if bin == l {
			return ToolLinterGo
		}
	}
	return ToolGeneric
}

// classifyInvocation scans the full command string for known tool invocations,
// handling `bash -c`, `sh -c`, and wrapped pipelines that hide the leading
// binary token.
func classifyInvocation(command string) ToolType {
	low := strings.ToLower(command)
	switch {
	case strings.Contains(low, "go test"), strings.Contains(low, "go.exe test"):
		return ToolGoTest
	case strings.Contains(low, "cargo test"), strings.Contains(low, "cargo.exe test"):
		return ToolRustTest
	case strings.Contains(low, "golangci-lint"),
		strings.Contains(low, "staticcheck"),
		strings.Contains(low, "go vet"),
		strings.Contains(low, "go.exe vet"):
		return ToolLinterGo
	case strings.Contains(low, "git status"):
		return ToolGitStatus
	default:
		return ToolGeneric
	}
}

// hasArg reports whether args contains the exact token sub.
func hasArg(args []string, sub string) bool {
	for _, a := range args {
		if strings.TrimSpace(a) == sub {
			return true
		}
	}
	return false
}

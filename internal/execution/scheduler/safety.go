package scheduler

// ToolSafety classifies a tool name as read-only (parallel-safe) or
// mutating (serial barrier). Unknown tools are conservative: mutating.
type ToolSafety func(string) bool

// IsReadOnlyTool reports whether the named tool is safe to run concurrently.
// Read-only tools do not mutate workspace state and can run via errgroup/
// WaitGroup bounded by runtime.NumCPU(). Mutating tools (write_file,
// apply_patch, bash, etc.) act as join barriers.
func IsReadOnlyTool(name string) bool {
	switch name {
	case "read_file", "glob", "grep", "fetch_web", "list_files", "search":
		return true
	default:
		return false
	}
}

// ReadOnlyTools is the canonical read-only set, exported for docs/tests.
var ReadOnlyTools = []string{"read_file", "glob", "grep", "fetch_web", "list_files", "search"}

// MutatingTools is documentation for the barrier class.
var MutatingTools = []string{"write_file", "apply_patch", "bash", "edit", "delete_file"}

package policy

// CapabilityGraph is the read-only physical-facts surface the PolicyEngine
// consumes. It answers ONLY "what tools and capabilities does the physical
// environment possess, and does its scope cover this target?" — it never
// answers "may this action run?". The concrete graph is assembled in the
// composition root from the Workspace Capability Graph (pkg/engine/layer1)
// and the runtime CapabilitySet (internal/core/capability); the PolicyEngine
// never performs OS probing or tool discovery itself.
//
// CapabilityGraph deliberately carries no Allow/Deny or mode vocabulary, so
// the two governance concerns cannot drift: the graph states physical facts,
// the PolicyEngine derives the permission verdict.
type CapabilityGraph interface {
	// Supports reports whether the workspace exposes the named capability.
	Supports(cap string) bool
	// Resolve returns the concrete command bound to the named capability,
	// and whether the capability is supported.
	Resolve(cap string) (string, bool)
	// CanMutateFile reports whether the physical mutation scope covers the
	// given file path.
	CanMutateFile(path string) bool
	// CanExecuteCommand reports whether the physical environment's granted
	// execute scope covers the command.
	CanExecuteCommand(cmd string) bool
}

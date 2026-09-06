package compose

// Bootstrap is the sole composition root for the binary. It wires the full
// Application dependency graph — CapabilityGuard, OCCGate,
// DiskCheckpointCoordinator, Substrate, and RuntimeExecutor — into the
// canonical appruntime.Runtime. The UI consumes the Runtime exclusively via
// read-only interfaces and intent dispatch channels; it never constructs
// engines, stores, or substrate adapters directly.
//
// Bootstrap is an alias for Wire that enforces the single-root invariant:
// cmd/izen and all headless entry points must call Bootstrap, never
// construct substrate, guard, or executor components ad-hoc. This guarantees
// that 100% of mutations originate from internal/runtime/compose and execute
// via RuntimeExecutor → Substrate.ExecuteUnit.
func Bootstrap(opts ...Option) (*Application, error) {
	return Wire(opts...)
}

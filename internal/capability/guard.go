package capability

// Re-export the domain capability guard for the Phase 4 pipeline binding.
// The single mutation authority pipe routes through RuntimeExecutor → CapabilityGuard → Substrate.
import "github.com/PizenLabs/izen/internal/core/domain/authorization"

// CapabilityGuard is the authorization gate for the execution pipeline.
type CapabilityGuard = authorization.CapabilityGuard

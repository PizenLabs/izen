package autonomy

import (
	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// behaviorScope returns the $prompt provenance the behavioral tests exercise.
func behaviorScope() domain.ScopeProvenance { return domain.ScopeDynamic }

// behaviorSubstrate returns the mutation authority the behavioral tests route
// repairs through.
func behaviorSubstrate(root string) substrate.ProposalExecutor {
	return substrate.NewConcreteSubstrate(root)
}

package execution

import (
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/protocol"
)

func TestG3ExecutorRejectsMultiFilePlanningUnderReadOnlyContract(t *testing.T) {
	direct := protocol.Describe(protocol.DirectCompletion)
	profile := strategy.ExecutionStrategyProfile{
		Strategy:        strategy.MultiFilePlanning,
		MaxOutputTokens: 512,
	}
	err := validateExecutionContract(ExecuteRequest{
		InteractionContract: protocol.DirectCompletion,
		Contract:            &direct,
		MaxOutputTokens:     512,
	}, profile, true)
	if !errors.Is(err, protocol.ErrAuthorityCeilingExceeded) {
		t.Fatalf("error = %v, want authority ceiling rejection", err)
	}
}

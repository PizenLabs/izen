package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/protocol"
)

// TestG8AskStagedOperationsFailClosedBeforeProviderOrMutation is a boundary
// fault-injection test, not just a policy-unit test. A legacy caller that
// omits a descriptor must not be able to turn an /ask request into an agentic
// mutation merely by attaching a staged FILE_MUTATE or SHELL_EXEC scope.
func TestG8AskStagedOperationsFailClosedBeforeProviderOrMutation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "index.html")
	const original = "<html><body>stable</body></html>\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	profile := strategy.ExecutionStrategyProfile{
		Strategy:       strategy.TargetedMutation,
		StrategyReason: "G8 contract fault injection",
	}
	executor := NewRuntimeExecutor(root, config.Default(), nil, nil, "")

	for _, operation := range []protocol.Operation{protocol.OperationFileMutate, protocol.OperationShell} {
		t.Run(string(operation), func(t *testing.T) {
			result, err := executor.Execute(context.Background(), ExecuteRequest{
				RequestID: "g8-ask-" + string(operation),
				Mode:      "ask",
				Prompt:    "explain the current page",
				Target:    "index.html",
				Strategy:  &profile,
				StagedSubTasks: []SubTaskScope{{
					ID:        "g8-staged",
					Operation: string(operation),
				}},
			})
			if !errors.Is(err, ErrAuthorityExceeded) {
				t.Fatalf("staged %s error = %v, want ErrAuthorityExceeded", operation, err)
			}
			if result == nil || result.Proof == nil {
				t.Fatal("contract rejection did not return an execution proof")
			}
			if result.PendingPatchID != "" || len(executor.PendingPatchIDs()) != 0 {
				t.Fatal("contract rejection exposed an approval surface")
			}
			contents, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(contents) != original {
				t.Fatalf("workspace changed during rejected /ask %s: %q", operation, contents)
			}
		})
	}
}

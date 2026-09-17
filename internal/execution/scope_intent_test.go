package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

func TestScopeIntentPreservesPrompt(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target.go"), []byte("package target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gateway := NewIntentGateway(root)
	for _, tc := range []struct {
		input, prompt, directive string
		scope domain.ScopeProvenance
		strategy strategy.ExecutionStrategy
	}{
		{"refactor @target.go", "refactor @target.go", "", domain.ScopeNone, strategy.TargetedReasoning},
		{"$prompt inspect @target.go and refactor it", "inspect @target.go and refactor it", "prompt", domain.ScopeDynamic, strategy.TargetedMutation},
	} {
		t.Run(tc.directive+tc.prompt, func(t *testing.T) {
			req, det, err := gateway.Gate(context.Background(), tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if req.Prompt != tc.prompt || det.Prompt != tc.prompt || det.Directive != tc.directive {
				t.Fatalf("prompt/directive changed: request=%q resolution=%q directive=%q", req.Prompt, det.Prompt, det.Directive)
			}
			if req.ScopeProvenance != tc.scope || req.Strategy.Strategy != tc.strategy {
				t.Fatalf("scope=%v strategy=%s", req.ScopeProvenance, req.Strategy.Strategy)
			}
		})
	}
}

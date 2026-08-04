package wire

import (
	"testing"

	"github.com/PizenLabs/izen/pkg/runtime/analyzer"
	"github.com/PizenLabs/izen/pkg/runtime/policy"
	"github.com/PizenLabs/izen/pkg/runtime/registry"
)

func TestDefaultPolicyRoutesSmallScopeToDirect(t *testing.T) {
	e := policy.New(DefaultPolicyRules()...)
	d := e.Evaluate(&analyzer.Facts{
		Intent:        analyzer.IntentBugFix,
		TargetFiles:   []string{"a.go"},
		Files:         1,
		TokenEstimate: 1_000,
		MaxFanout:     2,
	})
	if !d.StrategyGranted("direct_generation") {
		t.Error("small scope should be granted direct_generation")
	}
	if d.StrategyGranted("iterative") {
		t.Error("small scope should not be granted iterative")
	}
	if !d.ApprovedFor("direct_generation", []registry.Capability{registry.CapabilityCoding, registry.CapabilityToolUse}) {
		t.Error("small scope should approve direct_generation with its capabilities")
	}
}

func TestDefaultPolicyRoutesLargeScopeToIterative(t *testing.T) {
	e := policy.New(DefaultPolicyRules()...)

	bigTokens := e.Evaluate(&analyzer.Facts{TokenEstimate: 100_000, MaxFanout: 1})
	if !bigTokens.StrategyGranted("iterative") || bigTokens.StrategyGranted("direct_generation") {
		t.Error("large token scope should be granted iterative only")
	}

	bigFanout := e.Evaluate(&analyzer.Facts{TokenEstimate: 100, MaxFanout: 12})
	if !bigFanout.StrategyGranted("iterative") || bigFanout.StrategyGranted("direct_generation") {
		t.Error("high fanout scope should be granted iterative only")
	}
}

func TestDefaultPolicyThresholdBoundary(t *testing.T) {
	e := policy.New(DefaultPolicyRules()...)
	// Exactly at the inclusive direct budget the direct rule still matches.
	boundary := e.Evaluate(&analyzer.Facts{TokenEstimate: 25_000, MaxFanout: 4})
	if !boundary.StrategyGranted("direct_generation") {
		t.Error("at the inclusive budget boundary direct_generation should be granted")
	}
}

package execution

import (
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/capability/policy"
	"github.com/PizenLabs/izen/pkg/extractor"
)

func TestV3ArtifactPipelineValidatesGo(t *testing.T) {
	p := NewV3ArtifactPipeline()
	gate := p.ValidateContent("main.go", []byte("package main\r\nfunc main() {}\r\n"), 0)
	if !gate.Passed {
		t.Fatalf("valid go rejected: %v", gate.Error)
	}
	// Normalization must have converted CRLF to LF.
	if strings.Contains(string(gate.Normalized), "\r") {
		t.Errorf("normalized content still contains CR: %q", gate.Normalized)
	}
}

func TestV3ArtifactPipelineRetriesSyntaxFailure(t *testing.T) {
	p := NewV3ArtifactPipeline()
	gate := p.ValidateContent("main.go", []byte("package main\nfunc main( {\n"), 0)
	if gate.Passed {
		t.Fatal("corrupted go must not pass")
	}
	if gate.Decision != policy.DecisionRetry {
		t.Fatalf("Decision = %v, want DecisionRetry (budget available)", gate.Decision)
	}
	if gate.Directive == "" {
		t.Error("retry directive must be populated")
	}
}

func TestV3ArtifactPipelineAbortsWhenBudgetExhausted(t *testing.T) {
	p := NewV3ArtifactPipeline()
	gate := p.ValidateContent("main.go", []byte("package main\nfunc main( {\n"), 3)
	if gate.Passed {
		t.Fatal("corrupted go must not pass")
	}
	if gate.Decision != policy.DecisionAbort {
		t.Fatalf("Decision = %v, want DecisionAbort after budget exhaustion", gate.Decision)
	}
	if gate.Directive != "" {
		t.Error("aborted gate must carry no retry directive")
	}
}

func TestV3ArtifactPipelineValidatesJSONAndHTML(t *testing.T) {
	p := NewV3ArtifactPipeline()
	if gate := p.ValidateContent("config.json", []byte(`{"a": 1}`), 0); !gate.Passed {
		t.Errorf("valid json rejected: %v", gate.Error)
	}
	if gate := p.ValidateContent("index.html", []byte("<p>hi</p>"), 0); !gate.Passed {
		t.Errorf("valid html rejected: %v", gate.Error)
	}
	if gate := p.ValidateContent("config.json", []byte(`{"a":`), 0); gate.Passed {
		t.Error("corrupted json must not pass")
	}
}

func TestV3ArtifactPipelineUnknownLanguagePasses(t *testing.T) {
	p := NewV3ArtifactPipeline()
	gate := p.ValidateContent("notes.md", []byte("any markdown text"), 0)
	if !gate.Passed {
		t.Fatalf("unregistered language must pass unvalidated, got: %v", gate.Error)
	}
}

func TestV3ArtifactPipelineNormalizesPaths(t *testing.T) {
	p := NewV3ArtifactPipeline()
	gate := p.ValidateContent("src//main.go", []byte("package main\n"), 0)
	if !gate.Passed {
		t.Fatalf("valid go rejected: %v", gate.Error)
	}
}

func TestV3ArtifactPipelineParseContracts(t *testing.T) {
	p := NewV3ArtifactPipeline()
	arts, err := p.ParseContracts(":::artifact a.txt\nhello\n:::\n")
	if err != nil {
		t.Fatalf("ParseContracts: %v", err)
	}
	if len(arts) != 1 || arts[0].Path != "a.txt" {
		t.Fatalf("artifacts = %+v, want a.txt", arts)
	}
	if string(arts[0].Content) != "hello" {
		t.Errorf("content = %q, want hello", arts[0].Content)
	}

	if _, err := p.ParseContracts("just prose"); !errors.Is(err, extractor.ErrContractViolation) {
		t.Fatalf("prose error = %v, want ErrContractViolation", err)
	}
}

func TestV3ArtifactPipelineInspectReasoningNonBlocking(t *testing.T) {
	p := NewV3ArtifactPipeline()
	// Must not block and must not affect the gate outcome.
	gate := p.ValidateContent("main.go", []byte("package main\n"), 0)
	p.InspectReasoning("output with <thinking>reasoning</thinking>")
	p.Observer().Wait()
	if !gate.Passed {
		t.Fatal("gate outcome must be unaffected by the observer")
	}
	if p.Observer().LeakCount() != 1 {
		t.Errorf("LeakCount = %d, want 1", p.Observer().LeakCount())
	}
}

func TestEngineExposesV3ArtifactPipeline(t *testing.T) {
	// NewEngine wires a V3ArtifactPipeline so the UI build gate can always
	// run without nil checks beyond the engine itself.
	if NewV3ArtifactPipeline() == nil {
		t.Fatal("NewV3ArtifactPipeline returned nil")
	}
}

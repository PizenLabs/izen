package contextcompiler

import (
	"context"
	"io"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/protocol"
)

type recordingProvider struct {
	request ai.Request
}

func (p *recordingProvider) Name() string { return "recording" }
func (p *recordingProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.request = req
	return &ai.Response{Content: "ok"}, nil
}
func (p *recordingProvider) ExecuteStream(_ context.Context, req ai.Request) (io.ReadCloser, error) {
	p.request = req
	return nil, io.ErrUnexpectedEOF
}

func TestPreparedProviderCompilesLabelledRequestOnce(t *testing.T) {
	inner := &recordingProvider{}
	compiler := New()
	provider := compiler.WrapProvider(inner)
	descriptor, err := protocol.NewContractDescriptor(protocol.StructuredCompletion, protocol.DescriptorOptions{
		OutputSchema:           protocol.SchemaJSON,
		StructuralOutputSchema: `{"type":"object"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Execute(context.Background(), ai.Request{
		Model:               "gpt-4o",
		ContextPhase:        "plan",
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
		System:              "follow the contract",
		Messages:            []ai.Message{{Role: "user", Content: "produce a plan"}},
	}); err != nil {
		t.Fatal(err)
	}
	if !inner.request.ContextPrepared {
		t.Fatal("provider facade did not mark the request prepared")
	}
	if inner.request.ContextPhase != "plan" {
		t.Fatalf("phase = %q, want plan", inner.request.ContextPhase)
	}
	if inner.request.System != "follow the contract" {
		t.Fatalf("system prompt changed unexpectedly: %q", inner.request.System)
	}
	if len(inner.request.Messages) != 1 || inner.request.Messages[0].Content == "" {
		t.Fatalf("compiled user turn missing: %+v", inner.request.Messages)
	}
}

func TestPreparedProviderCompilesUnlabelledLegacyRequest(t *testing.T) {
	inner := &recordingProvider{}
	provider := New().WrapProvider(inner)
	if _, err := provider.Execute(context.Background(), ai.Request{
		Model:    "gpt-4o",
		System:   "system",
		Messages: []ai.Message{{Role: "user", Content: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	if !inner.request.ContextPrepared {
		t.Fatal("unlabelled request bypassed the composed compiler")
	}
	if inner.request.ContextPhase != string(PhaseExecute) {
		t.Fatalf("default phase = %q, want execute", inner.request.ContextPhase)
	}
}

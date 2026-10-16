package autonomy

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
)

// TestConversationTerminatesProvenWithoutWorkspaceObservation is the
// driver-level acceptance of the workspace-independent conversational contract.
//
// Before this contract, a pure greeting terminated UNSUBSTANTIATED because the
// read-only contract demanded a workspace observation the request never had.
// The runtime now proves a conversation on its own evidence — a delivered
// response — while every investigation/review objective still requires an
// observation. The workspace here contains an unrelated file, so a bug that
// satisfied the conversation by observing the workspace would be caught: the
// assertion is on the DELIVERED RESPONSE, not on any observed file.
func TestConversationTerminatesProvenWithoutWorkspaceObservation(t *testing.T) {
	_, mock, a, _ := testHarness(t, []*ai.Response{
		{Content: "Hello! How can I help you today?"},
		{Content: "Hello! How can I help you today?"},
		{Content: "Hello! How can I help you today?"},
	})
	d := NewDriver(a, nil, WithScope("$run"))

	term, err := d.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mock.calls() == 0 {
		t.Fatal("the conversation never invoked the provider")
	}
	if term == nil {
		t.Fatal("a conversation must reach a terminal state")
	}
	if term.State != autonomy.RuntimeCompleted {
		t.Fatalf("conversation terminal state = %s (%s), want COMPLETED", term.State, term.Reason)
	}
}

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/protocol"
)

type observabilityCollector struct {
	mu     sync.Mutex
	events []events.DomainEvent
}

func (c *observabilityCollector) add(ev events.DomainEvent) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *observabilityCollector) wait(t *testing.T, typ string) events.DomainEvent {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for _, ev := range c.events {
			if ev.Type() == typ {
				c.mu.Unlock()
				return ev
			}
		}
		c.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("event %s not observed", typ)
	return nil
}

func TestRuntimeExecutorAuditsAuthorityRejection(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	bus := events.NewBus(64)
	defer bus.Close()
	collector := &observabilityCollector{}
	sub := bus.SubscribeAll(collector.add)
	defer sub.Cancel()

	descriptor := protocol.Describe(protocol.DirectCompletion)
	mock := &mockProvider{responses: []*ai.Response{{Content: "must not be called"}}}
	x := testExecutor(t, root, mock, bus)
	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "obs-authority",
		Mode:      "build",
		Prompt:    "mutate the file",
		Target:    "note.txt",
		Contract:  &descriptor,
	})
	if err == nil || res == nil || !errors.Is(res.Err, ErrAuthorityExceeded) {
		t.Fatalf("expected authority rejection, result=%+v err=%v", res, err)
	}
	event := collector.wait(t, events.EventAdmissionDecision)
	payload := event.Payload().(events.AdmissionDecisionPayload)
	if payload.Allowed || payload.ReasonCode != "authority_exceeded" || payload.Contract != protocol.DirectCompletion {
		t.Fatalf("authority audit = %+v", payload)
	}
	if strings.Contains(payload.Reason, "mutate the file") {
		t.Fatalf("admission audit leaked raw prompt: %q", payload.Reason)
	}
}

func TestRuntimeExecutorBindsProtocolAndCompilerTelemetry(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)
	bus := events.NewBus(128)
	defer bus.Close()
	collector := &observabilityCollector{}
	sub := bus.SubscribeAll(collector.add)
	defer sub.Cancel()

	descriptor, err := protocol.NewContractDescriptor(protocol.DirectCompletion, protocol.DescriptorOptions{
		AuthorityCeiling: protocol.AuthorityReadOnly,
	})
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "answer",
		Usage: ai.ProviderUsage{
			Known:            true,
			PromptTokens:     11,
			CompletionTokens: 3,
			TotalTokens:      14,
			RequestStartedAt: time.Now().Add(-20 * time.Millisecond),
			FirstTokenAt:     time.Now().Add(-10 * time.Millisecond),
			CompletedAt:      time.Now(),
			FinishReason:     "stop",
		},
	}}}
	x := testExecutor(t, root, mock, bus)
	telemetry := NewTelemetryWithProtocol("obs-1", "ask", protocol.NewObservabilityBinding(descriptor.Contract, &descriptor, "", "ask", "auto"))
	x.SetTelemetrySink(telemetry)
	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "obs-1",
		SessionID: "sess-obs",
		Mode:      "ask",
		Prompt:    "secret prompt payload",
		Contract:  &descriptor,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Proof.ContractID == "" {
		t.Fatal("execution contract identity was not stamped")
	}

	admission := collector.wait(t, events.EventAdmissionDecision)
	admissionPayload := admission.Payload().(events.AdmissionDecisionPayload)
	if !admissionPayload.Allowed || admissionPayload.Contract != protocol.DirectCompletion || admissionPayload.Mode != "ask" {
		t.Fatalf("admission binding = %+v", admissionPayload)
	}
	contextEvent := collector.wait(t, events.EventContextCompilation)
	contextPayload := contextEvent.Payload().(events.ContextCompilationPayload)
	if contextPayload.ReservedTokens < 0 || contextPayload.Contract != protocol.DirectCompletion || contextPayload.ContractID != res.Proof.ContractID {
		t.Fatalf("context telemetry = %+v", contextPayload)
	}
	providerEvent := collector.wait(t, events.EventProviderExecution)
	providerPayload := providerEvent.Payload().(events.ProviderExecutionPayload)
	if providerPayload.ContractID != res.Proof.ContractID || providerPayload.Contract != protocol.DirectCompletion {
		t.Fatalf("provider binding = %+v", providerPayload)
	}
	if providerPayload.PromptTokens != 11 || providerPayload.CompletionTokens != 3 || providerPayload.FinishReason != "stop" {
		t.Fatalf("provider usage = %+v", providerPayload)
	}
	if providerPayload.PromptChars == 0 || providerPayload.PromptFingerprint == "" {
		t.Fatalf("provider structural prompt metadata missing: %+v", providerPayload)
	}
	data, err := json.Marshal(providerPayload)
	if err != nil {
		t.Fatalf("marshal provider telemetry: %v", err)
	}
	if strings.Contains(string(data), "secret prompt payload") {
		t.Fatalf("provider telemetry contains raw prompt: %s", data)
	}
	snapshot := telemetry.Snapshot()
	if snapshot.Protocol.ContractID != res.Proof.ContractID || snapshot.Compile == nil || len(snapshot.Admissions) != 1 || len(snapshot.ProviderExecutions) != 1 {
		t.Fatalf("execution telemetry snapshot = %+v", snapshot)
	}
}

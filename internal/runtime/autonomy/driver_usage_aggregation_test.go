package autonomy

// ── A PARKED RUN MUST STILL REPORT WHAT IT SPENT ─────────────────────────────
//
// The reported defect: the session footer read `↑0 · ↓0` after runs that had
// billed thousands of tokens through a real provider.
//
// A parked run is the hardest case to get right. It is exactly the case where
// execution STOPS — at a human boundary, with a staged candidate and no applied
// mutation — so a usage path that only commits on a terminal result reports
// nothing at all, and the footer renders the absence of a measurement as a
// measured zero.
//
// These tests pin the aggregate at the driver for the park path: whatever the
// provider billed must survive into AggregatedUsage, with the known/unknown
// distinction intact so the UI can say "unknown" rather than "0".

import (
	"context"
	"io"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// billingShapeProvider streams and reports authoritative usage through the
// `ai.UsageProvider` seam, exactly as a real provider does.
type billingShapeProvider struct {
	calls int
}

func (p *billingShapeProvider) Name() string { return "billing-shape" }

func (p *billingShapeProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	p.calls++
	return &ai.Response{
		Content: "```html\n<!DOCTYPE html>\n<html><body><h1>Hello IZEN</h1></body></html>\n```",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 900, CompletionTokens: 120, FinishReason: "stop"},
	}, nil
}

func (p *billingShapeProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	p.calls++
	return &billingShapeStream{
		body: "```html\n<!DOCTYPE html>\n<html><body><h1>Hello IZEN</h1></body></html>\n```",
		usage: ai.ProviderUsage{
			Known: true, PromptTokens: 1234, CompletionTokens: 567,
			ReasoningTokens: 40, TotalTokens: 1841, FinishReason: "stop",
		},
	}, nil
}

// unknownUsageProvider models a provider that bills but does not report a
// usage object. Its usage must remain UNKNOWN, never become a zero.
type unknownUsageProvider struct{}

func (unknownUsageProvider) Name() string { return "no-usage-report" }

func (unknownUsageProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	return &ai.Response{Content: "```html\n<html><body><h1>x</h1></body></html>\n```"}, nil
}

func (unknownUsageProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return &bareShapeStream{body: "```html\n<html><body><h1>x</h1></body></html>\n```"}, nil
}

type billingShapeStream struct {
	body  string
	usage ai.ProviderUsage
	pos   int
}

func (s *billingShapeStream) Read(p []byte) (int, error) {
	if s.pos >= len(s.body) {
		return 0, io.EOF
	}
	n := copy(p, s.body[s.pos:])
	s.pos += n
	return n, nil
}
func (s *billingShapeStream) Close() error            { return nil }
func (s *billingShapeStream) Usage() ai.ProviderUsage { return s.usage }
func (s *billingShapeStream) FinishReason() string    { return s.usage.FinishReason }

type bareShapeStream struct {
	body string
	pos  int
}

func (s *bareShapeStream) Read(p []byte) (int, error) {
	if s.pos >= len(s.body) {
		return 0, io.EOF
	}
	n := copy(p, s.body[s.pos:])
	s.pos += n
	return n, nil
}
func (s *bareShapeStream) Close() error { return nil }

// runToPark drives a full driver run that is expected to stop at a human
// boundary, and reports whether it actually parked there.
func runToPark(t *testing.T, provider ai.Provider) (*Driver, ai.ProviderUsage) {
	t.Helper()
	root := t.TempDir()
	writeTarget(t, root, "index.html", "<!DOCTYPE html>\n<html><body><h1>Old</h1></body></html>\n")
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, provider, bus)
	d := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus,
		WithLoopBounds(lifecycleBounds()))

	term, err := d.Run(context.Background(), "change the heading in index.html")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("precondition: the run reached a terminal state %v, so this is not the park path", term)
	}
	if d.Boundary() == nil {
		t.Fatal("precondition: the run did not park at a human boundary")
	}
	in, out, known := d.AggregatedUsage()
	return d, ai.ProviderUsage{
		Known:            known,
		PromptTokens:     in,
		CompletionTokens: out,
	}
}

// TestDriverAggregatesUsageOnTheParkPath is the direct regression for the
// footer that read `↑0 · ↓0`: a run that parks after a real billed invocation
// must still expose that invocation's token counts.
func TestDriverAggregatesUsageOnTheParkPath(t *testing.T) {
	_, usage := runToPark(t, &billingShapeProvider{})
	if !usage.Known {
		t.Fatal("the provider reported usage but the parked run aggregates it as unknown — the footer renders that absence as ↑0 · ↓0")
	}
	if usage.PromptTokens != 1234 || usage.CompletionTokens != 567 {
		t.Errorf("aggregated usage = ↑%d · ↓%d, want the provider's ↑1234 · ↓567",
			usage.PromptTokens, usage.CompletionTokens)
	}
}

// TestDriverParkPathNeverFabricatesAZero pins the honesty half of the contract:
// a provider that reports nothing must leave the aggregate UNKNOWN. A zero here
// would be indistinguishable from "nothing was spent", which is a false
// statement about billing.
func TestDriverParkPathNeverFabricatesAZero(t *testing.T) {
	_, usage := runToPark(t, unknownUsageProvider{})
	if usage.Known {
		t.Fatalf("a provider that reported no usage produced a KNOWN aggregate (↑%d · ↓%d); unknown must stay unknown",
			usage.PromptTokens, usage.CompletionTokens)
	}
}

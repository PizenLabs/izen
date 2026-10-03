package execution_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/capability"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// stubProvider is a deterministic reasoning backend. It records what it was
// asked and answers with a scripted response, which is what makes the provider
// path testable without a network and without weakening the assertions.
type stubProvider struct {
	response string
	err      error
	calls    int
	lastReq  ai.Request
	lastBody string
}

func (p *stubProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.calls++
	p.lastReq = req
	p.lastBody = req.Messages[len(req.Messages)-1].Content
	if p.err != nil {
		return nil, p.err
	}
	return &ai.Response{Content: p.response}, nil
}

func (p *stubProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("streaming is not used by the repair proposer")
}

func (p *stubProvider) Name() string { return "stub" }

// TestProviderRepairProposerParsesStrictProposal: a compliant structured answer
// becomes a proposal, and the evidence reaches the model.
func TestProviderRepairProposerParsesStrictProposal(t *testing.T) {
	stub := &stubProvider{response: `{"defect_code":"MISSING_SUBRESOURCE","target":"styles.css","content":"body{}","rationale":"the runtime 404ed"}`}
	p := &execution.ProviderRepairProposer{Provider: stub, Model: "test/model"}

	defect := capability.Defect{
		Code:       capability.CodeMissingSubresource,
		Summary:    "referenced resource \"style.css\" is not served",
		Entry:      "index.html",
		Candidates: []string{"styles.css"},
		Evidence:   []string{"runtime.fetch"},
	}
	entry := execution.Observation{
		BaseURL: "http://127.0.0.1:1234",
		Result: capability.Observation{
			BaseURL: "http://127.0.0.1:1234",
			Resources: []capability.ResourceProbe{
				{Ref: "style.css", Local: true, Status: 404},
				{Ref: "https://cdn.example.com/x.css", Local: false},
			},
		},
	}

	proposal, err := p.ProposeRepair(context.Background(), defect, entry)
	if err != nil {
		t.Fatalf("ProposeRepair: %v", err)
	}
	if proposal.Content != "body{}" {
		t.Fatalf("content = %q", proposal.Content)
	}
	if proposal.DefectCode != capability.CodeMissingSubresource {
		t.Fatalf("defect code = %q", proposal.DefectCode)
	}
	// The declared target travels as ADVISORY evidence. The runtime ignores it;
	// recording it is the point.
	if proposal.Target != "styles.css" {
		t.Fatalf("advisory target = %q", proposal.Target)
	}
	// The prompt must carry the real evidence: the status codes, the candidate,
	// and the cross-origin exclusion.
	for _, want := range []string{
		"style.css -> HTTP 404",
		"cross-origin, not probed",
		"the workspace provides: styles.css",
		"MISSING_SUBRESOURCE",
	} {
		if !strings.Contains(stub.lastBody, want) {
			t.Fatalf("repair prompt is missing %q:\n%s", want, stub.lastBody)
		}
	}
}

// TestProviderRepairProposerDeclinesUnusableResponses: every unusable response
// is a refusal, never a partial mutation.
func TestProviderRepairProposerDeclinesUnusableResponses(t *testing.T) {
	defect := capability.Defect{Code: capability.CodeMissingSubresource, Entry: "index.html"}
	cases := []struct {
		name     string
		response string
	}{
		{"empty", ""},
		{"explicit refusal", "NO_PROPOSAL"},
		{"prose", "I looked at the file and it seems the stylesheet path is wrong."},
		{"no content field", `{"defect_code":"MISSING_SUBRESOURCE","target":"a.css"}`},
		{"empty content", `{"defect_code":"MISSING_SUBRESOURCE","content":"   "}`},
		{"wrong defect", `{"defect_code":"DOCUMENT_STRUCTURE_INVALID","content":"<html></html>"}`},
		{"malformed json", `{"defect_code": `},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubProvider{response: tc.response}
			p := &execution.ProviderRepairProposer{Provider: stub, Model: "test/model"}
			if _, err := p.ProposeRepair(context.Background(), defect, execution.Observation{}); err == nil {
				t.Fatal("an unusable response must be refused, never applied")
			}
		})
	}
}

// TestProviderRepairProposerRefusesMissingWiring: no provider, no model.
func TestProviderRepairProposerRefusesMissingWiring(t *testing.T) {
	defect := capability.Defect{Code: capability.CodeMissingSubresource, Entry: "index.html"}
	if _, err := (&execution.ProviderRepairProposer{Model: "test/model"}).ProposeRepair(context.Background(), defect, execution.Observation{}); err == nil {
		t.Fatal("a proposer with no provider must refuse")
	}
	stub := &stubProvider{response: "{}"}
	if _, err := (&execution.ProviderRepairProposer{Provider: stub}).ProposeRepair(context.Background(), defect, execution.Observation{}); err == nil {
		t.Fatal("a proposer with no explicit model must refuse rather than fall back to a default")
	}
	if stub.calls != 0 {
		t.Fatal("a proposer with no model must not spend a provider call")
	}
}

// TestProviderRepairProposerRequestsStrictSchema: the proposer must ask for
// structured output, or a prose answer becomes an unparsable repair.
func TestProviderRepairProposerRequestsStrictSchema(t *testing.T) {
	stub := &stubProvider{response: `{"defect_code":"X","content":"y"}`}
	p := &execution.ProviderRepairProposer{Provider: stub, Model: "test/model"}
	if _, err := p.ProposeRepair(context.Background(), capability.Defect{Code: "X", Entry: "a"}, execution.Observation{}); err != nil {
		t.Fatal(err)
	}
	rf := stub.lastReq.ResponseFormat
	if rf == nil || rf.Type != "json_schema" || !rf.Strict || len(rf.Schema) == 0 {
		t.Fatalf("response format = %+v, want a strict json_schema request", rf)
	}
	if stub.lastReq.MaxTokens <= 0 {
		t.Fatal("a repair proposal must be budget-bounded")
	}
}

// TestProviderRepairProposerTransportFailureIsDeclined: a provider error must
// not be mistaken for a repair.
func TestProviderRepairProposerTransportFailureIsDeclined(t *testing.T) {
	stub := &stubProvider{err: errors.New("connection reset")}
	p := &execution.ProviderRepairProposer{Provider: stub, Model: "test/model"}
	_, err := p.ProposeRepair(context.Background(),
		capability.Defect{Code: capability.CodeMissingSubresource, Entry: "index.html"}, execution.Observation{})
	if err == nil {
		t.Fatal("a transport failure must surface as an error")
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("err = %v, want the transport cause preserved", err)
	}
}

// TestParseRepairProposalToleratesFencedOutput: a model that wraps its answer
// in a fence is still answering. Rejecting it would turn formatting into a
// declined repair.
func TestParseRepairProposalToleratesFencedOutput(t *testing.T) {
	body := "Here is the repair:\n```json\n{\"defect_code\":\"MISSING_SUBRESOURCE\",\"content\":\"body{}\"}\n```\n"
	proposal, err := execution.ParseRepairProposal(body, capability.Defect{Code: capability.CodeMissingSubresource})
	if err != nil {
		t.Fatalf("ParseRepairProposal: %v", err)
	}
	if proposal.Content != "body{}" {
		t.Fatalf("content = %q", proposal.Content)
	}
}

// TestParseRepairProposalHandlesBracesInsideContent: the proposal content is
// source code, so a brace inside a string must not terminate the object early.
func TestParseRepairProposalHandlesBracesInsideContent(t *testing.T) {
	body := `{"defect_code":"MISSING_SUBRESOURCE","content":"body::after { content: \"}\"; }","rationale":"nested"}`
	proposal, err := execution.ParseRepairProposal(body, capability.Defect{Code: capability.CodeMissingSubresource})
	if err != nil {
		t.Fatalf("ParseRepairProposal: %v", err)
	}
	if !strings.Contains(proposal.Content, `content: "}"`) {
		t.Fatalf("content was truncated at an interior brace: %q", proposal.Content)
	}
	if proposal.Rationale != "nested" {
		t.Fatalf("rationale = %q, want the trailing field parsed", proposal.Rationale)
	}
}

// TestGoldenObjective_WithProviderReasoner is the end-to-end objective driven
// through the real provider adapter rather than a scripted proposer. It proves
// the provider path is the same path, not a parallel one.
func TestGoldenObjective_WithProviderReasoner(t *testing.T) {
	root := goldenFixture(t)
	// The provider repairs the broken reference and closes the element by
	// deriving the change from the evidence it is shown.
	stub := &stubProvider{response: ""}
	proposer := &execution.ProviderRepairProposer{Provider: stub, Model: "test/model"}

	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()

	// Drive one round with the provider answering a proposal derived from the
	// observed bytes. The provider's declared target is deliberately wrong, so a
	// passing test proves the runtime overrode it.
	body, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	fixed := strings.Replace(string(body), `href="style.css"`, `href="styles.css"`, 1)
	fixed = strings.Replace(fixed, "  <script", "  </main>\n  <script", 1)
	stub.response = `{"defect_code":"` + capability.CodeMissingSubresource +
		`","target":"styles.css","content":` + jsonQuote(fixed) + `,"rationale":"404 observed"}`

	loop, err := execution.NewBehaviorLoop(execution.BehaviorLoopConfig{
		Runtime:   rt,
		Grant:     behavioralGrant(),
		Proposer:  proposer,
		Mutate:    substrate.NewConcreteSubstrate(root),
		Authorize: authorizeWorkspaceWrite,
	})
	if err != nil {
		t.Fatalf("NewBehaviorLoop: %v", err)
	}
	result := loop.Run(context.Background())
	if !result.Proven {
		t.Fatalf("provider-driven objective did not prove: block=%v defects=%s",
			result.Block, result.Final.DefectLine())
	}
	if stub.calls == 0 {
		t.Fatal("the provider was never consulted")
	}
	html, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `href="styles.css"`) {
		t.Fatalf("provider-driven repair did not land:\n%s", html)
	}
	// The declared "styles.css" target must NOT have been written.
	css, err := os.ReadFile(filepath.Join(root, "styles.css"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(css), "<!DOCTYPE") {
		t.Fatalf("the runtime wrote the proposal to the model's declared target:\n%s", css)
	}
}

func jsonQuote(s string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r", "\t", "\\t")
	return "\"" + replacer.Replace(s) + "\""
}

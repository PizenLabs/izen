package execution

// Forensic request-construction evidence (Phase 1/2 of the 5,883-token
// investigation). This test runs the REAL autonomous and /build execution
// paths against a capturing provider and reproduces the EXACT serialized
// OpenRouter request body (mirroring providers.openrouterRequest's JSON tags)
// to prove, at the wire level, what max_tokens / reasoning fields are sent.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/events"
)

// wireMsg mirrors providers.openrouterMessage.
type wireMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// wireReasoning mirrors providers.openrouterReasoning.
type wireReasoning struct {
	Effort    string `json:"effort,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

// wireBody mirrors providers.openrouterRequest JSON tags exactly.
type wireBody struct {
	Model         string             `json:"model"`
	Messages      []wireMsg          `json:"messages"`
	MaxTokens     int                `json:"max_tokens,omitempty"`
	Temperature   float64            `json:"temperature,omitempty"`
	Stop          []string           `json:"stop,omitempty"`
	Stream        bool               `json:"stream"`
	StreamOptions *streamOptionsWire `json:"stream_options,omitempty"`
	Reasoning     *wireReasoning     `json:"reasoning,omitempty"`
}

type streamOptionsWire struct {
	IncludeUsage bool `json:"include_usage"`
}

// serializeWire reconstructs the OpenRouter body from the ai.Request exactly as
// providers.OpenRouterProvider.buildRequest does (openrouter.go:395-422).
func serializeWire(model string, req ai.Request, stream bool) wireBody {
	msgs := []wireMsg{}
	if req.System != "" {
		msgs = append(msgs, wireMsg{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, wireMsg{Role: m.Role, Content: m.Content})
	}
	body := wireBody{
		Model:     model,
		Messages:  msgs,
		MaxTokens: req.MaxTokens,
		Stream:    stream,
	}
	if stream {
		body.StreamOptions = &streamOptionsWire{IncludeUsage: true}
	}
	if req.Reasoning != nil {
		r := &wireReasoning{Effort: req.Reasoning.Level}
		switch {
		case req.Reasoning.CoTLimit > 0:
			r.MaxTokens = req.Reasoning.CoTLimit
		case req.Reasoning.BudgetTokens > 0:
			r.MaxTokens = req.Reasoning.BudgetTokens
		}
		if r.Effort != "" || r.MaxTokens != 0 {
			body.Reasoning = r
		}
	}
	return body
}

func marshalBody(body wireBody) string {
	data, _ := json.MarshalIndent(body, "", "  ")
	return string(data)
}

// reproIndexHTML is a representative index.html (≈6.5KB) matching the original
// 2,185-token repro envelope. It is retained as documentation of the incident
// shape; since Boundary 2 (preflight guard, I5) such a target can never reach
// the wire under a 1024-token budget, so the wire-forensics test exercises
// reproIndexHTMLCompact — a feasible full-rewrite target.
const reproIndexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Landing Page</title>
  <style>
    :root { --primary:#2563eb; --bg:#0f172a; --text:#e2e8f0; }
    * { margin:0; padding:0; box-sizing:border-box; }
    body { font-family:system-ui,sans-serif; background:var(--bg); color:var(--text); }
    header { padding:2rem; border-bottom:1px solid #1e293b; }
    nav { display:flex; justify-content:space-between; align-items:center; max-width:1200px; margin:0 auto; }
    nav ul { display:flex; gap:1.5rem; list-style:none; }
    nav a { color:var(--text); text-decoration:none; }
    .hero { max-width:1200px; margin:0 auto; padding:6rem 2rem; text-align:center; }
    .hero h1 { font-size:3rem; margin-bottom:1rem; }
    .hero p { font-size:1.25rem; opacity:0.8; margin-bottom:2rem; }
    .btn { display:inline-block; padding:0.75rem 1.5rem; background:var(--primary); color:#fff; border-radius:8px; text-decoration:none; }
    .features { max-width:1200px; margin:0 auto; padding:4rem 2rem; display:grid; grid-template-columns:repeat(3,1fr); gap:2rem; }
    .feature { padding:1.5rem; border:1px solid #1e293b; border-radius:12px; }
    .feature h3 { margin-bottom:0.5rem; }
    footer { padding:2rem; border-top:1px solid #1e293b; text-align:center; opacity:0.6; }
  </style>
</head>
<body>
  <header>
    <nav>
      <div class="logo">Acme</div>
      <ul>
        <li><a href="#features">Features</a></li>
        <li><a href="#pricing">Pricing</a></li>
        <li><a href="#contact">Contact</a></li>
      </ul>
    </nav>
  </header>
  <section class="hero">
    <h1>Build Better Software</h1>
    <p>The modern platform for teams that ship.</p>
    <a href="#pricing" class="btn">Get Started</a>
  </section>
  <section class="features" id="features">
    <div class="feature"><h3>Lightning Fast</h3><p>Deploy in seconds with our global edge network.</p></div>
    <div class="feature"><h3>Secure</h3><p>Enterprise-grade security on every plan.</p></div>
    <div class="feature"><h3>Simple</h3><p>An intuitive dashboard your whole team will love.</p></div>
  </section>
  <footer>&copy; 2026 Acme Corp</footer>
</body>
</html>
`

// reproIndexHTMLCompact is a feasible full-rewrite fixture (~0.9KB): small
// enough that TargetFileTokens × FullRewriteTokenMultiplier fits the 1024-token
// strategy budget at Boundary 2, so the wire body is actually produced.
const reproIndexHTMLCompact = `<!DOCTYPE html>
<html lang="en">
<head>
  <title>Landing Page</title>
  <style>
    body { font-family:system-ui,sans-serif; background:#0f172a; color:#e2e8f0; }
    .btn { padding:0.75rem 1.5rem; background:#2563eb; color:#fff; border-radius:8px; }
  </style>
</head>
<body>
  <header>
    <nav><div class="logo">Acme</div></nav>
  </header>
  <section class="hero">
    <h1>Build Better Software</h1>
    <p>The modern platform for teams that ship.</p>
    <a href="#pricing" class="btn">Get Started</a>
  </section>
  <footer>&copy; 2026 Acme Corp</footer>
</body>
</html>
`

// TestForensicWireBody_AutonomousVsBuild reconstructs the exact wire body of
// the autonomous and /build executions of the repro objective and proves the
// request-level facts: identical system/user prompts, the strategy budget
// present as max_tokens (post-fix) or absent (pre-fix), and NO reasoning field
// on either path.
func TestForensicWireBody_AutonomousVsBuild(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", reproIndexHTMLCompact)
	bus := events.NewBus(events.DefaultBufferSize)

	model := "cohere/north-mini-code:free"

	runPath := func(mode string, preFix bool) ai.Request {
		mock := &mockProvider{responses: []*ai.Response{{
			Content: sampleReplace,
			Usage:   ai.ProviderUsage{Known: true},
		}}}
		cfg := config.Default()
		cfg.Models.SessionModel = model
		x := NewRuntimeExecutor(root, cfg, mock, bus, "")
		x.SetVerifier(trivialVerifier(root))
		x.SetAuthorization(testAuthorization())

		var req ExecuteRequest
		if mode == "build" {
			greq, _, err := NewIntentGateway(root).Gate(context.Background(),
				"$prompt check this file @index.html and rewrite the code for me")
			if err != nil {
				t.Fatalf("Gate: %v", err)
			}
			req = greq
		} else { // autonomy — mirrors ExecutorAdapter.Execute (adapter.go:98-119)
			profile := NewIntentGateway(root).SelectStrategy(
				"check this file @index.html and rewrite the code for me")
			req = ExecuteRequest{
				RequestID:       "forensic-auto",
				Mode:            "autonomy",
				Prompt:          "check this file @index.html and rewrite the code for me",
				Targets:         []string{"index.html"},
				Strategy:        &profile,
				MaxOutputTokens: profile.MaxOutputTokens,
			}
			if preFix {
				req.MaxOutputTokens = 0 // pre-fix adapter never propagated the budget
			}
		}
		res, err := x.Execute(context.Background(), req)
		if err != nil {
			t.Fatalf("[%s] Execute: %v", mode, err)
		}
		_ = res
		if len(mock.requests) != 1 {
			t.Fatalf("[%s] provider requests = %d, want 1", mode, len(mock.requests))
		}
		return mock.requests[0]
	}

	autonomousPostFix := runPath("autonomy", false)
	autonomousPreFix := runPath("autonomy", true)
	build := runPath("build", false)

	// /build and autonomous POST-FIX must produce byte-identical requests.
	if build.System != autonomousPostFix.System ||
		build.MaxTokens != autonomousPostFix.MaxTokens ||
		len(build.Messages) != len(autonomousPostFix.Messages) {
		t.Errorf("/build and autonomy post-fix requests differ:\nbuild %+v\nauto  %+v", build, autonomousPostFix)
	} else {
		for i := range build.Messages {
			if build.Messages[i].Content != autonomousPostFix.Messages[i].Content {
				t.Errorf("message[%d] differs between /build and autonomy post-fix", i)
			}
		}
	}

	t.Logf("=== AUTONOMOUS POST-FIX REQUEST (ai.Request) ===")
	t.Logf("  build.MaxTokens: %d  auto.MaxTokens: %d  (equal: %v)",
		build.MaxTokens, autonomousPostFix.MaxTokens, build.MaxTokens == autonomousPostFix.MaxTokens)
	t.Logf("  system identical: %v", build.System == autonomousPostFix.System)
	t.Logf("  user message identical: %v",
		len(build.Messages) == len(autonomousPostFix.Messages) &&
			len(build.Messages) > 0 && build.Messages[0].Content == autonomousPostFix.Messages[0].Content)
	t.Logf("  System chars: %d  (est tokens chars/4: %d)", len(autonomousPostFix.System), len(autonomousPostFix.System)/4)
	t.Logf("  Messages: %d", len(autonomousPostFix.Messages))
	for i, m := range autonomousPostFix.Messages {
		t.Logf("    msg[%d] role=%-8s contentChars=%d", i, m.Role, len(m.Content))
	}
	t.Logf("  MaxTokens: %d   Reasoning: %v", autonomousPostFix.MaxTokens, autonomousPostFix.Reasoning)
	totalChars := len(autonomousPostFix.System)
	for _, m := range autonomousPostFix.Messages {
		totalChars += len(m.Content)
	}
	t.Logf("  TOTAL request chars: %d  → est tokens (chars/4): %d", totalChars, totalChars/4)
	t.Logf("  WIRE BODY (stream=true, POST-FIX):\n%s", marshalBody(serializeWire(model, autonomousPostFix, true)))

	preFixBody := serializeWire(model, autonomousPreFix, true)
	preFixJSON, _ := json.Marshal(preFixBody)
	t.Logf("  PRE-FIX max_tokens field present: %v", strings.Contains(string(preFixJSON), "max_tokens"))
	t.Logf("  PRE-FIX wire body:\n%s", marshalBody(preFixBody))

	// Assertions that pin the forensic facts.
	if autonomousPostFix.Reasoning != nil {
		t.Errorf("autonomous request carried a reasoning config (%+v) — production code never sets one", autonomousPostFix.Reasoning)
	}
	if autonomousPostFix.MaxTokens == 0 {
		t.Errorf("autonomous post-fix MaxTokens = 0 — budget must be propagated")
	}
	postJSON, _ := json.Marshal(serializeWire(model, autonomousPostFix, true))
	if !strings.Contains(string(postJSON), `"max_tokens"`) {
		t.Errorf("post-fix wire body omits max_tokens: %s", postJSON)
	}
	if strings.Contains(string(postJSON), `"reasoning"`) {
		t.Errorf("wire body contains a reasoning field: %s", postJSON)
	}
}

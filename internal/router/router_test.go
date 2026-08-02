package router

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// scriptedClassifier simulates a language-agnostic semantic classifier: it
// understands the same intent expressed in any natural language and projects it
// onto the canonical execution phase. It is the test double for the LLM-backed
// classifier in production.
type scriptedClassifier struct {
	mu       sync.Mutex
	calls    int
	plan     []string
	lowConf  map[string]float64
	fallback ClassificationResult
}

func (s *scriptedClassifier) Classify(_ context.Context, input string) (ClassificationResult, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	for _, p := range s.plan {
		if input == p {
			return ClassificationResult{
				Intent:      IntentPlan,
				Confidence:  0.92,
				Language:    detectLanguage(input),
				Explanation: "frontend UI redesign intent detected",
			}, nil
		}
	}
	if conf, ok := s.lowConf[input]; ok {
		return ClassificationResult{
			Intent:      IntentUnknown,
			Confidence:  conf,
			Language:    detectLanguage(input),
			Explanation: "ambiguous: no canonical phase confidently identified",
		}, nil
	}
	return s.fallback, nil
}

func (s *scriptedClassifier) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newScriptedClassifier() *scriptedClassifier {
	return &scriptedClassifier{
		plan: []string{
			"Rewrite the profile website",      // en
			"Làm lại giao diện trang cá nhân",  // vi
			"重写个人主页网站",                         // zh
			"Переписать личный сайт-портфолио", // ru
			"Refaire le site web personnel",    // fr
		},
		lowConf: map[string]float64{
			"Đọc project giúp mình": 0.31, // ambiguous
		},
		fallback: ClassificationResult{Intent: IntentAsk, Confidence: 0.75, Language: "latin", Explanation: "default"},
	}
}

// ── Cross-Lingual Equivalence Test ───────────────────────────────────────────
//
// Verifies that variations across multiple natural language families (English,
// Vietnamese, Chinese, Russian, French) all classify consistently to the
// intended execution phase (Plan / Frontend UI), and that the hybrid gateway
// never mangled or fast-path-absorbed any of them.

func TestCrossLingualEquivalence(t *testing.T) {
	inputs := []string{
		"Rewrite the profile website",      // English
		"Làm lại giao diện trang cá nhân",  // Vietnamese
		"重写个人主页网站",                         // Chinese
		"Переписать личный сайт-портфолио", // Russian
		"Refaire le site web personnel",    // French
	}

	bus := events.NewBus(16)
	defer bus.Close()

	var mu sync.Mutex
	var classified []events.IntentClassifiedPayload
	var approval []events.ApprovalRequestedPayload
	bus.Subscribe(events.EventIntentClassified, func(ev events.DomainEvent) {
		mu.Lock()
		classified = append(classified, ev.Payload().(events.IntentClassifiedPayload))
		mu.Unlock()
	})
	bus.Subscribe(events.EventApprovalRequested, func(ev events.DomainEvent) {
		mu.Lock()
		approval = append(approval, ev.Payload().(events.ApprovalRequestedPayload))
		mu.Unlock()
	})

	clf := newScriptedClassifier()
	r := NewRouter(clf, nil).WithEventBus(bus)

	for _, in := range inputs {
		res, err := r.Route(context.Background(), in)
		if err != nil {
			t.Fatalf("Route(%q) error: %v", in, err)
		}
		if res.Intent != IntentPlan {
			t.Errorf("Route(%q) intent = %q, want %q (Plan / Frontend UI)", in, res.Intent, IntentPlan)
		}
		if res.Confidence < 0.6 {
			t.Errorf("Route(%q) confidence = %.2f, want >= 0.6", in, res.Confidence)
		}
		if res.ConfirmationRequirement {
			t.Errorf("Route(%q) ConfirmationRequirement = true, want false (high confidence)", in)
		}
		if res.Explanation == "" {
			t.Errorf("Route(%q) missing explanation", in)
		}
	}

	// The gateway must have consulted the semantic classifier for every input
	// (no input was absorbed by the deterministic fast path).
	if got := clf.CallCount(); got != len(inputs) {
		t.Errorf("classifier calls = %d, want %d (none fast-pathed)", got, len(inputs))
	}

	if !waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(classified) == len(inputs) }) {
		t.Fatalf("IntentClassified events = %d, want %d", len(classified), len(inputs))
	}
	mu.Lock()
	for _, ev := range classified {
		if ev.Intent != string(IntentPlan) {
			t.Errorf("event intent = %q, want plan", ev.Intent)
		}
	}
	// No approval events for high-confidence cross-lingual classifications.
	if len(approval) != 0 {
		t.Errorf("ApprovalRequested events = %d, want 0", len(approval))
	}
	mu.Unlock()
}

// ── Ambiguity Handling Test ───────────────────────────────────────────────────
//
// Verifies that ambiguous prompts yield low confidence and emit an
// ApprovalRequested / confirmation event for UI clarification instead of
// making a blind guess.

func TestAmbiguityHandling(t *testing.T) {
	ambiguous := "Đọc project giúp mình" // "Read the project for me" — ambiguous intent

	bus := events.NewBus(16)
	defer bus.Close()

	var mu sync.Mutex
	var approval []events.ApprovalRequestedPayload
	var classified []events.IntentClassifiedPayload
	bus.Subscribe(events.EventApprovalRequested, func(ev events.DomainEvent) {
		mu.Lock()
		approval = append(approval, ev.Payload().(events.ApprovalRequestedPayload))
		mu.Unlock()
	})
	bus.Subscribe(events.EventIntentClassified, func(ev events.DomainEvent) {
		mu.Lock()
		classified = append(classified, ev.Payload().(events.IntentClassifiedPayload))
		mu.Unlock()
	})

	clf := newScriptedClassifier()
	r := NewRouter(clf, nil).WithEventBus(bus)

	res, err := r.Route(context.Background(), ambiguous)
	if err != nil {
		t.Fatalf("Route(%q) error: %v", ambiguous, err)
	}

	if !res.ConfirmationRequirement {
		t.Errorf("ConfirmationRequirement = false, want true (ambiguous low-confidence input)")
	}
	if res.Confidence >= 0.6 {
		t.Errorf("confidence = %.2f, want < 0.6", res.Confidence)
	}

	if !waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(approval) == 1 }) {
		t.Fatalf("ApprovalRequested events = %d, want 1", len(approval))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(approval) != 1 {
		t.Fatalf("ApprovalRequested events = %d, want 1", len(approval))
	}
	if !strings.Contains(approval[0].Reason, "ambiguous") {
		t.Errorf("approval reason = %q, want ambiguous justification", approval[0].Reason)
	}
	// The disambiguation event must accompany the classification event.
	if len(classified) != 1 {
		t.Errorf("IntentClassified events = %d, want 1", len(classified))
	}
}

// ── Deterministic Fast Path ───────────────────────────────────────────────────
//
// The fast path MUST NOT invoke the semantic classifier, and its results must
// be deterministic with full confidence.

func TestFastPathNeverInvokesSemanticClassifier(t *testing.T) {
	inputs := []string{
		"/plan migrate the database",
		"/build fix typo in @README.md",
		"/ask explain the graph engine",
		"/investigate why is the build failing",
		"/review the last change",
		"$hot touch @README.md",
		"fix --force @main.go",
		"update --high @config.yml",
		"edit @main.go:42",
		"fix src/handler.go:123",
	}

	for _, in := range inputs {
		clf := newScriptedClassifier()
		r := NewRouter(clf, nil)
		res, err := r.Route(context.Background(), in)
		if err != nil {
			t.Fatalf("Route(%q) error: %v", in, err)
		}
		if res.Confidence != 1.0 {
			t.Errorf("Route(%q) confidence = %.2f, want 1.0", in, res.Confidence)
		}
		if res.ConfirmationRequirement {
			t.Errorf("Route(%q) ConfirmationRequirement = true, want false", in)
		}
		// The classifier must never be invoked on the fast path.
		if got := clf.CallCount(); got != 0 {
			t.Errorf("Route(%q) classifier calls = %d, want 0", in, got)
		}
	}
}

func TestFastPathIntentMapping(t *testing.T) {
	tests := []struct {
		input string
		want  Intent
	}{
		{"/plan something", IntentPlan},
		{"/build something", IntentBuild},
		{"/ask something", IntentAsk},
		{"/investigate something", IntentInvestigate},
		{"/review something", IntentReview},
		{"$hot fix it", IntentBuild},
		{"--force apply", IntentBuild},
		{"--high run", IntentBuild},
		{"@main.go:42", IntentPlan},
		{"src/util.go:7", IntentPlan},
	}
	for _, tc := range tests {
		res, err := NewRouter(newScriptedClassifier(), nil).Route(context.Background(), tc.input)
		if err != nil {
			t.Fatalf("Route(%q) error: %v", tc.input, err)
		}
		if res.Intent != tc.want {
			t.Errorf("Route(%q) intent = %q, want %q", tc.input, res.Intent, tc.want)
		}
	}
}

// ── Deterministic Fast Path Zero-Allocation ───────────────────────────────────
//
// NFR: zero additional heap allocations on the deterministic fast path.

func TestFastPathZeroAllocation(t *testing.T) {
	inputs := []string{
		"/plan migrate the database",
		"$hot touch @README.md",
		"fix --force @main.go",
		"@main.go:42",
		"/review the last change",
	}
	for _, in := range inputs {
		allocs := testing.AllocsPerRun(100, func() {
			_, _ = fastPath(in)
		})
		if allocs != 0 {
			t.Errorf("fastPath(%q) allocations = %.1f, want 0", in, allocs)
		}
	}
}

// ── Configurable Confidence Policy ────────────────────────────────────────────

func TestConfidencePolicyAppliesThreshold(t *testing.T) {
	p := ConfidencePolicy{Threshold: 0.6}

	low := p.Apply(ClassificationResult{Intent: IntentPlan, Confidence: 0.45})
	if !low.ConfirmationRequirement {
		t.Errorf("low confidence (0.45) must require confirmation")
	}

	high := p.Apply(ClassificationResult{Intent: IntentPlan, Confidence: 0.9})
	if high.ConfirmationRequirement {
		t.Errorf("high confidence (0.9) must not require confirmation")
	}

	// Threshold above 1.0 clamps to 1.0, so anything below max is flagged.
	clamped := (ConfidencePolicy{Threshold: 2.0}).Apply(ClassificationResult{Confidence: 0.99})
	if !clamped.ConfirmationRequirement {
		t.Errorf("threshold 2.0 clamped to 1.0 must flag 0.99 confidence")
	}
	// Threshold exactly 1.0 with max confidence is NOT flagged.
	exact := (ConfidencePolicy{Threshold: 1.0}).Apply(ClassificationResult{Confidence: 1.0})
	if exact.ConfirmationRequirement {
		t.Errorf("threshold 1.0 with confidence 1.0 must not require confirmation")
	}
	// Negative threshold clamps to 0.0, so nothing is flagged.
	never := (ConfidencePolicy{Threshold: -1}).Apply(ClassificationResult{Confidence: 0.01})
	if never.ConfirmationRequirement {
		t.Errorf("negative threshold clamped to 0.0 must never flag")
	}
}

func TestDefaultConfidencePolicy(t *testing.T) {
	if got := DefaultConfidencePolicy().Threshold; got != 0.6 {
		t.Errorf("default threshold = %.2f, want 0.6", got)
	}
}

// ── PromptIntentClassifier (JSON parsing + language-agnostic contract) ───────

func TestPromptIntentClassifierParsesJSON(t *testing.T) {
	clf := NewPromptIntentClassifier(func(_ context.Context, system, input string) (string, error) {
		if !strings.Contains(system, "canonical execution phase") {
			t.Errorf("system prompt missing phase guidance")
		}
		return `{"intent":"plan","confidence":0.91,"language":"en","explanation":"frontend redesign"}`, nil
	})

	res, err := clf.Classify(context.Background(), "Rewrite the profile website")
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}
	if res.Intent != IntentPlan || res.Confidence != 0.91 || res.Language != "en" {
		t.Errorf("res = %+v", res)
	}
	if res.Explanation == "" {
		t.Error("missing explanation")
	}
}

func TestPromptIntentClassifierToleratesFencedOutput(t *testing.T) {
	clf := NewPromptIntentClassifier(func(_ context.Context, _, _ string) (string, error) {
		return "```json\n{\"intent\":\"investigate\",\"confidence\":0.84,\"language\":\"ru\",\"explanation\":\"crash analysis\"}\n```", nil
	})
	res, err := clf.Classify(context.Background(), "Переписать личный сайт-портфолио")
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}
	if res.Intent != IntentInvestigate || res.Language != "ru" {
		t.Errorf("res = %+v", res)
	}
}

func TestPromptIntentClassifierClampsConfidence(t *testing.T) {
	clf := NewPromptIntentClassifier(func(_ context.Context, _, _ string) (string, error) {
		return `{"intent":"build","confidence":1.7,"language":"zh","explanation":"x"}`, nil
	})
	res, err := clf.Classify(context.Background(), "重写个人主页网站")
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}
	if res.Confidence != 1.0 {
		t.Errorf("confidence = %.2f, want clamped to 1.0", res.Confidence)
	}
}

func TestPromptIntentClassifierUnknownIntent(t *testing.T) {
	clf := NewPromptIntentClassifier(func(_ context.Context, _, _ string) (string, error) {
		return `{"intent":"frobnicate","confidence":0.5,"language":"en","explanation":"x"}`, nil
	})
	res, err := clf.Classify(context.Background(), "something")
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}
	if res.Intent != IntentUnknown {
		t.Errorf("intent = %q, want unknown", res.Intent)
	}
}

func TestPromptIntentClassifierPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	clf := NewPromptIntentClassifier(func(_ context.Context, _, _ string) (string, error) {
		return "", context.Canceled
	})
	if _, err := clf.Classify(ctx, "anything"); err == nil {
		t.Fatal("Classify must propagate context cancellation")
	}
}

// ── Language-agnostic script detection ────────────────────────────────────────

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Rewrite the profile website", "latin"},
		{"Làm lại giao diện trang cá nhân", "latin"},
		{"重写个人主页网站", "cjk"},
		{"Переписать личный сайт-портфолио", "cyrillic"},
		{"Refaire le site web personnel", "latin"},
		{"", "unknown"},
	}
	for _, tc := range tests {
		if got := detectLanguage(tc.input); got != tc.want {
			t.Errorf("detectLanguage(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── Router nil-classifier guard ───────────────────────────────────────────────

func TestRouterWithoutClassifierReturnsError(t *testing.T) {
	r := NewRouter(nil, nil)
	if _, err := r.Route(context.Background(), "some natural language prompt"); err == nil {
		t.Fatal("Route with nil classifier must return ErrNoClassifier")
	}
	// Fast-path inputs still resolve deterministically without a classifier.
	res, err := r.Route(context.Background(), "/plan something")
	if err != nil {
		t.Fatalf("fast-path Route error: %v", err)
	}
	if res.Intent != IntentPlan {
		t.Errorf("intent = %q, want plan", res.Intent)
	}
}

// waitFor polls a condition with a deadline, matching the events test helper.
func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

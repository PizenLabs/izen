package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/layer0"
	"github.com/PizenLabs/izen/pkg/engine/layer1"
	"github.com/PizenLabs/izen/pkg/engine/layer2"
	"github.com/PizenLabs/izen/pkg/engine/layer3"
	"github.com/PizenLabs/izen/pkg/engine/layer4"
)

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// ── Event constructors and layer attribution ─────────────────────────────────

func TestEventTypeLayerAttribution(t *testing.T) {
	cases := []struct {
		typ   EventType
		layer string
	}{
		{EventKnowledgeResolved, "layer0"},
		{EventCapabilityDetected, "layer1"},
		{EventContextGoverned, "layer2"},
		{EventPipelineStep, "layer3"},
		{EventValidationDAG, "layer4"},
	}
	for _, c := range cases {
		if got := c.typ.Layer(); got != c.layer {
			t.Errorf("%s: expected layer %q, got %q", c.typ, c.layer, got)
		}
	}
	if got := EventType("bogus").Layer(); got != "unknown" {
		t.Errorf("unexpected layer %q for unknown type", got)
	}
}

func TestNewKnowledgeResolved_ProjectsMetrics(t *testing.T) {
	k := &layer0.ResolvedKnowledge{
		Root:                  "/ws",
		PrimaryManager:        layer0.ManagerGo,
		Managers:              []layer0.Manager{layer0.ManagerGo, layer0.ManagerPnpm},
		ActiveConventions:     make([]layer0.Convention, 3),
		StructuralConstraints: make([]layer0.Constraint, 2),
		Conflicts:             make([]layer0.Conflict, 1),
	}
	ev := NewKnowledgeResolved(k, 5*time.Millisecond)
	if ev.Type() != EventKnowledgeResolved {
		t.Fatalf("unexpected type %s", ev.Type())
	}
	p := ev.Payload().(*KnowledgeResolvedPayload)
	if p.Root != "/ws" || p.PrimaryManager != "go" {
		t.Fatalf("unexpected payload %+v", p)
	}
	if p.Managers != 2 || p.Conventions != 3 || p.Constraints != 2 || p.Conflicts != 1 {
		t.Fatalf("unexpected metric counts %+v", p)
	}
	if p.Duration != 5*time.Millisecond {
		t.Fatalf("unexpected duration %v", p.Duration)
	}
}

func TestNewCapabilityDetected_NilGraph(t *testing.T) {
	ev := NewCapabilityDetected(nil, time.Millisecond)
	p := ev.Payload().(*CapabilityDetectedPayload)
	if p.Stack != string(layer1.StackUnknown) {
		t.Fatalf("expected unknown stack, got %q", p.Stack)
	}
	if len(p.Capabilities) != 0 {
		t.Fatalf("expected no capabilities, got %v", p.Capabilities)
	}
}

func TestNewContextGoverned_ProjectsStats(t *testing.T) {
	ctx := &layer2.ExecutionContext{
		Stats: layer2.ContextStats{
			Files:           6,
			Symbols:         42,
			Tokens:          12000,
			BudgetTokens:    16000,
			BudgetMet:       true,
			CompressedFiles: 3,
			StrippedBodies:  9,
		},
		Policy: layer2.ContextPolicy{MaxTokenBudget: 16000, MaxFiles: 16, MaxSymbols: 512, CompressionRatio: 0.4},
	}
	ev := NewContextGoverned(layer2.ContextRequest{TargetFile: "main.go"}, ctx, time.Millisecond)
	p := ev.Payload().(*ContextGovernedPayload)
	if p.TargetFile != "main.go" {
		t.Fatalf("unexpected target %q", p.TargetFile)
	}
	if p.Files != 6 || p.Symbols != 42 || p.TokensUsed != 12000 || p.TokenBudget != 16000 {
		t.Fatalf("unexpected stats %+v", p)
	}
	if !p.BudgetMet || p.CompressionRatio != 0.4 || p.CompressedFiles != 3 || p.StrippedBodies != 9 {
		t.Fatalf("unexpected governance %+v", p)
	}
}

func TestNewPipelineStep_Constructors(t *testing.T) {
	done := NewPipelineStepDone("run-7", layer3.IntentRefactor, layer3.RouteGenerative, layer3.StageExecute, 2, 3, 4500, time.Millisecond)
	p := done.Payload().(*PipelineStepPayload)
	if p.RunID != "run-7" || p.Intent != "refactor" || p.Strategy != "generative" || p.Stage != "execute" {
		t.Fatalf("unexpected done payload %+v", p)
	}
	if p.StageIndex != 2 || p.Patches != 3 || p.Tokens != 4500 || p.State != string(layer3.StateDone) || p.Err != "" {
		t.Fatalf("unexpected done fields %+v", p)
	}

	failed := NewPipelineStepFailed("run-7", layer3.IntentBugFix, layer3.RouteGenerative, layer3.StageValidate, 3, errors.New("boom"), time.Millisecond)
	f := failed.Payload().(*PipelineStepPayload)
	if f.State != string(layer3.StateFailed) || f.Err != "boom" || f.Patches != 0 {
		t.Fatalf("unexpected failed payload %+v", f)
	}

	cancelled := NewPipelineStepCancelled("run-7", layer3.IntentBugFix, layer3.RouteGenerative, layer3.StageValidate, 3, context.Canceled, time.Millisecond)
	c := cancelled.Payload().(*PipelineStepPayload)
	if c.State != string(layer3.StateCancelled) || c.Err != context.Canceled.Error() {
		t.Fatalf("unexpected cancelled payload %+v", c)
	}
}

func TestNewValidationDAG_ProjectsPassFailShortCircuit(t *testing.T) {
	res := &layer4.Result{
		OK: false,
		Nodes: map[string]layer4.NodeResult{
			"structural": {Stage: layer4.StageStructural, Status: layer4.StatusPassed},
			"syntax":     {Stage: layer4.StageSyntax, Status: layer4.StatusFailed},
			"build":      {Stage: layer4.StageBuild, Status: layer4.StatusSkipped},
		},
		Cancelled: []string{"build"},
		Err:       errors.New("syntax error"),
	}
	ev := NewValidationDAG(res, 2*time.Millisecond)
	p := ev.Payload().(*ValidationDAGPayload)
	if p.OK {
		t.Fatal("expected failed DAG")
	}
	if p.NodesTotal != 3 || p.NodesPassed != 1 || p.NodesFailed != 1 || p.NodesSkipped != 1 {
		t.Fatalf("unexpected node counts %+v", p)
	}
	if !p.ShortCircuited || p.Err != "syntax error" {
		t.Fatalf("unexpected short-circuit info %+v", p)
	}
	if len(p.FailedStages) != 1 || p.FailedStages[0] != "syntax" {
		t.Fatalf("unexpected failed stages %v", p.FailedStages)
	}
	if len(p.Stages) != 3 {
		t.Fatalf("expected 3 distinct stages, got %v", p.Stages)
	}
}

// ── Event bus ───────────────────────────────────────────────────────────────

func TestEventBus_ConcurrentPublishing(t *testing.T) {
	bus := NewEventBus(4096)
	defer bus.Close()

	var allKnowledge atomic.Int64
	var topicKnowledge atomic.Int64
	all := bus.SubscribeAll(func(ev Event) {
		if ev.Type() == EventKnowledgeResolved {
			allKnowledge.Add(1)
		}
	})
	defer all.Cancel()
	topic := bus.Subscribe(EventKnowledgeResolved, func(ev Event) { topicKnowledge.Add(1) })
	defer topic.Cancel()

	const goroutines = 8
	const per = 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < per; j++ {
				bus.Publish(NewKnowledgeResolved(nil, 0))
				bus.Publish(NewValidationDAG(nil, 0))
			}
		}()
	}
	wg.Wait()

	want := int64(goroutines * per)
	waitFor(t, func() bool { return allKnowledge.Load() == want && topicKnowledge.Load() == want })
	if got := allKnowledge.Load(); got != want {
		t.Fatalf("SubscribeAll: expected %d knowledge events, got %d", want, got)
	}
	if got := topicKnowledge.Load(); got != want {
		t.Fatalf("Subscribe topic: expected %d knowledge events, got %d", want, got)
	}
}

func TestEventBus_NonBlockingUnderSlowConsumer(t *testing.T) {
	bus := NewEventBus(1)
	sub := bus.SubscribeAll(func(Event) {
		time.Sleep(10 * time.Millisecond)
	})
	defer func() {
		sub.Cancel()
		bus.Close()
	}()

	start := time.Now()
	const n = 200
	for i := 0; i < n; i++ {
		bus.Publish(NewContextGoverned(layer2.ContextRequest{}, nil, 0))
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("publish blocked on slow consumer: %v", elapsed)
	}
	waitFor(t, func() bool { return sub.Dropped() > 0 })
	if sub.Dropped() == 0 {
		t.Fatal("expected dropped events under a slow consumer")
	}
}

func TestEventBus_TopicIsolation(t *testing.T) {
	bus := NewEventBus(16)
	var got atomic.Int64
	sub := bus.Subscribe(EventContextGoverned, func(Event) { got.Add(1) })
	defer func() {
		sub.Cancel()
		bus.Close()
	}()

	bus.Publish(NewKnowledgeResolved(nil, 0))
	bus.Publish(NewValidationDAG(nil, 0))
	bus.Publish(NewContextGoverned(layer2.ContextRequest{}, nil, 0))
	waitFor(t, func() bool { return got.Load() == 1 })
	if got.Load() != 1 {
		t.Fatalf("expected exactly 1 context event, got %d", got.Load())
	}
}

func TestEventBus_Close(t *testing.T) {
	bus := NewEventBus(16)
	bus.Close()
	if sub := bus.Subscribe(EventKnowledgeResolved, func(Event) {}); sub != nil {
		t.Fatal("subscribe after close must return nil")
	}
	bus.Publish(NewKnowledgeResolved(nil, 0))
	bus.Close()
}

// ── Timeline ────────────────────────────────────────────────────────────────

func TestTimeline_OrderedAndExportable(t *testing.T) {
	tl := NewTimeline("sess-1")
	evs := []Event{
		NewKnowledgeResolved(nil, time.Millisecond),
		NewCapabilityDetected(nil, time.Millisecond),
		NewContextGoverned(layer2.ContextRequest{}, nil, time.Millisecond),
		NewPipelineStepDone("run-1", layer3.IntentRefactor, layer3.RouteGenerative, layer3.StageExecute, 2, 3, 4500, time.Millisecond),
		NewValidationDAG(nil, time.Millisecond),
	}
	for _, e := range evs {
		tl.Record(e)
	}
	if tl.Len() != len(evs) {
		t.Fatalf("expected %d events, got %d", len(evs), tl.Len())
	}
	if tl.SessionID() != "sess-1" {
		t.Fatalf("unexpected session id %q", tl.SessionID())
	}

	got := tl.Events()
	for i := range evs {
		if got[i] != evs[i] {
			t.Fatalf("order mismatch at index %d", i)
		}
	}
	got[0] = nil
	if tl.Events()[0] != evs[0] {
		t.Fatal("Events must return a defensive copy")
	}

	tr := tl.Snapshot()
	if len(tr.Events) != len(evs) {
		t.Fatalf("snapshot has %d events, want %d", len(tr.Events), len(evs))
	}
	if tr.Events[3].Layer != "layer3" || tr.Events[3].Type != EventPipelineStep || tr.Events[3].Index != 3 {
		t.Fatalf("unexpected trace event %+v", tr.Events[3])
	}
	if tr.Events[0].Duration != time.Millisecond {
		t.Fatalf("trace event duration not populated: %v", tr.Events[0].Duration)
	}

	data, err := tl.ExportJSON()
	if err != nil {
		t.Fatalf("export json: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("exported json invalid: %v", err)
	}
	if out["session_id"] != "sess-1" {
		t.Fatalf("unexpected exported session %v", out["session_id"])
	}
	events, ok := out["events"].([]any)
	if !ok || len(events) != len(evs) {
		t.Fatalf("unexpected exported events %v", out["events"])
	}
}

func TestTimeline_MaxEventsEvictsOldest(t *testing.T) {
	tl := NewTimeline("sess", WithMaxEvents(3))
	published := make([]Event, 0, 5)
	for i := 0; i < 5; i++ {
		ev := NewKnowledgeResolved(nil, time.Duration(i))
		published = append(published, ev)
		tl.Record(ev)
	}
	if tl.Len() != 3 {
		t.Fatalf("expected 3 retained events, got %d", tl.Len())
	}
	evs := tl.Events()
	for i := range evs {
		if evs[i] != published[2+i] {
			t.Fatalf("expected oldest eviction; retained %d is not published[%d]", i, 2+i)
		}
	}
}

func TestTimeline_FromBusPreservesOrder(t *testing.T) {
	bus := NewEventBus(64)
	tl := NewTimeline("sess-bus")
	sub := bus.SubscribeAll(tl.Record)
	defer func() {
		sub.Cancel()
		bus.Close()
	}()

	const n = 25
	for i := 0; i < n; i++ {
		bus.Publish(NewPipelineStepDone("run", layer3.IntentFormat, layer3.RouteASTRewrite, layer3.StageExecute, i, 1, 0, time.Millisecond))
	}
	waitFor(t, func() bool { return tl.Len() == n })
	evs := tl.Events()
	for i, ev := range evs {
		if p := ev.Payload().(*PipelineStepPayload); p.StageIndex != i {
			t.Fatalf("bus delivery order broken at %d: index %d", i, p.StageIndex)
		}
	}
}

func TestRenderCompact(t *testing.T) {
	tl := NewTimeline("s")
	tl.Record(NewKnowledgeResolved(nil, time.Millisecond))
	tl.Record(NewValidationDAG(nil, time.Millisecond))
	out := tl.RenderCompact()
	for _, want := range []string{"layer0", "knowledge_resolved", "layer4", "validation_dag"} {
		if !strings.Contains(out, want) {
			t.Fatalf("compact render missing %q:\n%s", want, out)
		}
	}
}

// ── Replay ──────────────────────────────────────────────────────────────────

func TestReplayTimeline_ReconstructsDecisionPath(t *testing.T) {
	failRes := &layer4.Result{
		OK: false,
		Nodes: map[string]layer4.NodeResult{
			"structural": {Stage: layer4.StageStructural, Status: layer4.StatusPassed},
			"syntax":     {Stage: layer4.StageSyntax, Status: layer4.StatusFailed},
			"build":      {Stage: layer4.StageBuild, Status: layer4.StatusSkipped},
		},
		Cancelled: []string{"build"},
		Err:       errors.New("syntax error"),
	}
	events := []Event{
		NewKnowledgeResolved(&layer0.ResolvedKnowledge{PrimaryManager: layer0.ManagerGo}, time.Millisecond),
		NewCapabilityDetected(nil, time.Millisecond),
		NewContextGoverned(layer2.ContextRequest{TargetFile: "main.go"}, &layer2.ExecutionContext{
			Stats: layer2.ContextStats{Tokens: 8000, BudgetTokens: 16000, BudgetMet: true, Files: 4, Symbols: 64},
		}, time.Millisecond),
		NewPipelineStepDone("run-1", layer3.IntentBugFix, layer3.RouteGenerative, layer3.StageExecute, 2, 2, 4500, time.Millisecond),
		NewPipelineStepFailed("run-1", layer3.IntentBugFix, layer3.RouteGenerative, layer3.StageValidate, 3, errors.New("validation failed"), time.Millisecond),
		NewValidationDAG(failRes, time.Millisecond),
	}
	replay := ReplayTimeline(events)
	if replay.Decisions != len(events) {
		t.Fatalf("expected %d decisions, got %d", len(events), replay.Decisions)
	}
	if replay.Passed != 4 {
		t.Fatalf("expected 4 passed, got %d", replay.Passed)
	}
	if replay.Failed != 2 {
		t.Fatalf("expected 2 failed, got %d", replay.Failed)
	}

	step := replay.Steps[4]
	if step.Decision != "pipeline step validate" || step.Outcome != "failed" {
		t.Fatalf("unexpected pipeline step %+v", step)
	}
	if step.Metrics["intent"] != "bug_fix" || step.Metrics["strategy"] != "generative" {
		t.Fatalf("unexpected pipeline metrics %v", step.Metrics)
	}
	if step.Metrics["error"] != "validation failed" {
		t.Fatalf("missing error metric %v", step.Metrics)
	}

	last := replay.Steps[5]
	if last.Decision != "ran validation DAG" || last.Outcome != "failed" {
		t.Fatalf("unexpected validation step %+v", last)
	}
	if last.Metrics["nodes_passed"] != "1" || last.Metrics["nodes_failed"] != "1" || last.Metrics["nodes_skipped"] != "1" {
		t.Fatalf("unexpected node metrics %v", last.Metrics)
	}
	if last.Metrics["short_circuited"] != "true" || last.Metrics["error"] != "syntax error" {
		t.Fatalf("unexpected short-circuit metrics %v", last.Metrics)
	}

	ctxStep := replay.Steps[2]
	if ctxStep.Decision != "assembled governed context" || ctxStep.Outcome != "budget_met=true" {
		t.Fatalf("unexpected context step %+v", ctxStep)
	}
	if ctxStep.Metrics["compression_ratio"] == "" {
		t.Fatal("missing compression ratio metric")
	}
}

func TestReplayTimeline_EmptyAndNilTolerant(t *testing.T) {
	replay := ReplayTimeline(nil)
	if replay.Decisions != 0 || replay.Passed != 0 || replay.Failed != 0 {
		t.Fatalf("unexpected empty replay %+v", replay)
	}
	replay = ReplayTimeline([]Event{nil, NewKnowledgeResolved(nil, 0), nil})
	if replay.Decisions != 1 || replay.Passed != 1 {
		t.Fatalf("nil events must be skipped, got %+v", replay)
	}
}

// ── Strategy optimizer ──────────────────────────────────────────────────────

func TestCapabilityHash_DeterministicAndOrderIndependent(t *testing.T) {
	a := CapabilityHash([]layer1.Capability{layer1.CapBuild, layer1.CapTest, layer1.CapLint})
	b := CapabilityHash([]layer1.Capability{layer1.CapLint, layer1.CapTest, layer1.CapBuild})
	c := CapabilityHash([]layer1.Capability{layer1.CapBuild, layer1.CapTest, layer1.CapLint})
	if a != b || a != c {
		t.Fatalf("hash must be order-independent and deterministic: %q %q %q", a, b, c)
	}
	d := CapabilityHash([]layer1.Capability{layer1.CapBuild, layer1.CapTest})
	if a == d {
		t.Fatal("different capability sets must hash differently")
	}
	if len(a) != 16 {
		t.Fatalf("expected 16 hex characters, got %d", len(a))
	}
}

func TestNewStrategyKey_Deterministic(t *testing.T) {
	policy := layer2.DefaultPolicy()
	a := NewStrategyKey(layer3.IntentRefactor, CapabilityHash(nil), policy)
	b := NewStrategyKey(layer3.IntentRefactor, CapabilityHash(nil), policy)
	if a != b {
		t.Fatalf("equal inputs must produce equal keys: %q vs %q", a, b)
	}
	if a == NewStrategyKey(layer3.IntentBugFix, CapabilityHash(nil), policy) {
		t.Fatal("different intents must produce different keys")
	}
	other := policy
	other.MaxTokenBudget = 32000
	if a == NewStrategyKey(layer3.IntentRefactor, CapabilityHash(nil), other) {
		t.Fatal("different policies must produce different keys")
	}
}

func TestOptimizer_LearnsAndRecommends(t *testing.T) {
	opt := NewStrategyOptimizer()
	caps := []layer1.Capability{layer1.CapBuild, layer1.CapTest}
	capHash := CapabilityHash(caps)

	policyA := layer2.ContextPolicy{MaxTokenBudget: 8000, MaxFiles: 8, MaxSymbols: 256, CompressionRatio: 0.5}
	policyB := layer2.ContextPolicy{MaxTokenBudget: 16000, MaxFiles: 16, MaxSymbols: 512, CompressionRatio: 0.4}

	for i := 0; i < 3; i++ {
		opt.Record(ResultSample{Intent: layer3.IntentBugFix, CapHash: capHash, Policy: policyA, Strategy: "generative", Passed: false, Tokens: 9000, Latency: 3 * time.Second})
	}
	for i := 0; i < 5; i++ {
		opt.Record(ResultSample{Intent: layer3.IntentBugFix, CapHash: capHash, Policy: policyB, Strategy: "generative", Passed: true, Tokens: 7000, Latency: 2 * time.Second})
	}

	rec := opt.RecommendPolicy(layer3.IntentBugFix, caps)
	if rec.Fallback {
		t.Fatal("expected a learned recommendation")
	}
	if rec.Policy != policyB {
		t.Fatalf("expected policy B, got %+v", rec.Policy)
	}
	if rec.Strategy != "generative" {
		t.Fatalf("expected strategy generative, got %q", rec.Strategy)
	}
	if rec.PassRate != 1.0 {
		t.Fatalf("expected pass rate 1.0, got %v", rec.PassRate)
	}
	if rec.Samples != 5 {
		t.Fatalf("expected 5 samples, got %d", rec.Samples)
	}
	if rec.Confidence <= 0 || rec.Confidence > 1 {
		t.Fatalf("expected confidence within (0,1], got %v", rec.Confidence)
	}

	keyB := NewStrategyKey(layer3.IntentBugFix, capHash, policyB)
	stats, ok := opt.Stats(keyB)
	if !ok {
		t.Fatal("expected stats for policy B cohort")
	}
	if stats.Runs != 5 || stats.Passes != 5 {
		t.Fatalf("unexpected stats %+v", stats)
	}
	if stats.AvgTokens != 7000 {
		t.Fatalf("expected avg tokens 7000, got %v", stats.AvgTokens)
	}
	if stats.AvgLatency != 2*time.Second {
		t.Fatalf("expected avg latency 2s, got %v", stats.AvgLatency)
	}
}

func TestOptimizer_WeightsProportionalToPassRate(t *testing.T) {
	opt := NewStrategyOptimizer()
	caps := []layer1.Capability{layer1.CapTest}
	capHash := CapabilityHash(caps)

	policyA := layer2.ContextPolicy{MaxTokenBudget: 8000, MaxFiles: 8, MaxSymbols: 256, CompressionRatio: 0.5}
	policyB := layer2.ContextPolicy{MaxTokenBudget: 16000, MaxFiles: 16, MaxSymbols: 512, CompressionRatio: 0.4}

	for i := 0; i < 5; i++ {
		opt.Record(ResultSample{Intent: layer3.IntentFormat, CapHash: capHash, Policy: policyA, Passed: i < 3, Tokens: 100})
		opt.Record(ResultSample{Intent: layer3.IntentFormat, CapHash: capHash, Policy: policyB, Passed: i < 4, Tokens: 100})
	}

	w := opt.StrategyWeights(layer3.IntentFormat, caps)
	keyA := NewStrategyKey(layer3.IntentFormat, capHash, policyA)
	keyB := NewStrategyKey(layer3.IntentFormat, capHash, policyB)
	if len(w) != 2 {
		t.Fatalf("expected 2 weighted cohorts, got %d", len(w))
	}
	if w[keyB] <= w[keyA] {
		t.Fatalf("policy B weight %v must exceed policy A weight %v", w[keyB], w[keyA])
	}
	if sum := w[keyA] + w[keyB]; sum < 0.99 || sum > 1.01 {
		t.Fatalf("weights must sum to 1, got %v", sum)
	}
}

func TestOptimizer_FallbackWithoutHistory(t *testing.T) {
	opt := NewStrategyOptimizer()
	caps := []layer1.Capability{layer1.CapBuild}
	rec := opt.RecommendPolicy(layer3.IntentRefactor, caps)
	if !rec.Fallback {
		t.Fatal("expected fallback with no history")
	}
	if rec.Policy != layer2.DefaultPolicy() {
		t.Fatal("expected the layer2 default policy on fallback")
	}
	if rec.Samples != 0 || rec.Confidence != 0 {
		t.Fatalf("unexpected fallback fields %+v", rec)
	}

	// A single sample is below the minimum sample threshold and must not
	// drive a recommendation.
	opt.Record(ResultSample{
		Intent:  layer3.IntentRefactor,
		CapHash: CapabilityHash(nil),
		Policy:  layer2.DefaultPolicy(),
		Passed:  true,
	})
	rec = opt.RecommendPolicy(layer3.IntentRefactor, nil)
	if !rec.Fallback {
		t.Fatal("a single sample must not drive a recommendation")
	}
}

func TestOptimizer_TieBreakPrefersMoreSamples(t *testing.T) {
	opt := NewStrategyOptimizer()
	caps := []layer1.Capability{layer1.CapLint}
	capHash := CapabilityHash(caps)

	policyA := layer2.ContextPolicy{MaxTokenBudget: 8000, MaxFiles: 8, MaxSymbols: 256, CompressionRatio: 0.5}
	policyB := layer2.ContextPolicy{MaxTokenBudget: 16000, MaxFiles: 16, MaxSymbols: 512, CompressionRatio: 0.4}

	for i := 0; i < 3; i++ {
		opt.Record(ResultSample{Intent: layer3.IntentNewFeature, CapHash: capHash, Policy: policyA, Passed: i < 2})
	}
	for i := 0; i < 5; i++ {
		opt.Record(ResultSample{Intent: layer3.IntentNewFeature, CapHash: capHash, Policy: policyB, Passed: i < 4})
	}

	rec := opt.RecommendPolicy(layer3.IntentNewFeature, caps)
	if rec.Policy != policyB {
		t.Fatalf("tie must break toward the cohort with more samples, got %+v", rec.Policy)
	}
	if rec.Samples != 5 {
		t.Fatalf("expected 5 samples, got %d", rec.Samples)
	}
}

func TestOptimizer_ScopesByIntentAndCapabilities(t *testing.T) {
	opt := NewStrategyOptimizer()
	capHashGo := CapabilityHash([]layer1.Capability{layer1.CapBuild, layer1.CapTest})

	policy := layer2.DefaultPolicy()
	for i := 0; i < 4; i++ {
		opt.Record(ResultSample{Intent: layer3.IntentBugFix, CapHash: capHashGo, Policy: policy, Passed: true})
	}

	rec := opt.RecommendPolicy(layer3.IntentBugFix, []layer1.Capability{layer1.CapBuild, layer1.CapTest})
	if rec.Fallback {
		t.Fatal("go capability cohort must be learned")
	}
	recNode := opt.RecommendPolicy(layer3.IntentBugFix, []layer1.Capability{layer1.CapBuild})
	if !recNode.Fallback {
		t.Fatal("node capability cohort must not reuse the go cohort")
	}
	recRefactor := opt.RecommendPolicy(layer3.IntentRefactor, []layer1.Capability{layer1.CapBuild, layer1.CapTest})
	if !recRefactor.Fallback {
		t.Fatal("different intent must not reuse the bug_fix cohort")
	}
}

func TestOptimizer_ConcurrentRecordAndRecommend(t *testing.T) {
	opt := NewStrategyOptimizer()
	caps := []layer1.Capability{layer1.CapTest}
	policy := layer2.DefaultPolicy()

	const workers = 8
	const per = 100
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < per; j++ {
				opt.Record(ResultSample{
					Intent:   layer3.IntentFormat,
					CapHash:  CapabilityHash(caps),
					Policy:   policy,
					Strategy: "ast_rewrite",
					Passed:   true,
					Tokens:   100,
					Latency:  time.Millisecond,
				})
				_ = opt.RecommendPolicy(layer3.IntentFormat, caps)
				_ = opt.StrategyWeights(layer3.IntentFormat, caps)
			}
		}()
	}
	wg.Wait()

	want := workers * per
	stats, ok := opt.Stats(NewStrategyKey(layer3.IntentFormat, CapabilityHash(caps), policy))
	if !ok {
		t.Fatal("expected stats after concurrent recording")
	}
	if stats.Runs != want {
		t.Fatalf("expected %d runs, got %d", want, stats.Runs)
	}
	if stats.Passes != want {
		t.Fatalf("expected %d passes, got %d", want, stats.Passes)
	}
}

package ui

import (
	"strings"
	"testing"
	"time"
)

// ── LIVE tok/s METER (reasoning-aware) ─────────────────────────────────────
// The footer rate meter must stay live (>0) while ANY stream chunk — content
// or reasoning/thinking — is actively arriving, including the thinking phase
// before the first authoritative provider usage chunk. The authoritative
// stage token count itself is never estimated (see execution_ux_test.go).

// TestEstimateStreamTokens pins the per-chunk estimator: empty contributes
// nothing, every non-empty chunk contributes at least one token.
func TestEstimateStreamTokens(t *testing.T) {
	if got := estimateStreamTokens(""); got != 0 {
		t.Errorf("empty chunk = %d, want 0", got)
	}
	if got := estimateStreamTokens("x"); got != 1 {
		t.Errorf("1-char chunk = %d, want minimum 1", got)
	}
	if got := estimateStreamTokens(strings.Repeat("a", 400)); got != 100 {
		t.Errorf("400-char chunk = %d, want 100", got)
	}
}

// TestStreamRateLiveOnReasoningChunks pins the B.1 fix: a reasoning/thinking
// chunk advances the live rate meter while the authoritative stage count
// stays 0 (no provider usage has arrived yet).
func TestStreamRateLiveOnReasoningChunks(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	m.streamStartTime = time.Now().Add(-2 * time.Second)

	res, _ := m.Update(thinkingTokenMsg("pondering the architecture and weighing trade-offs"))
	m2 := res.(*model)

	if m2.streamLiveTokens <= 0 {
		t.Fatalf("live tokens = %d after a reasoning chunk, want > 0", m2.streamLiveTokens)
	}
	// Authoritative count is untouched: no provider usage arrived.
	if snap := m2.stageSnapshot(); snap.Tokens != 0 {
		t.Fatalf("stage tokens = %d — a chunk estimate leaked into the authoritative record", snap.Tokens)
	}
	if rate := m2.streamTokenRate(m2.stageSnapshot()); rate <= 0 {
		t.Fatalf("tok/s = %v during reasoning streaming, want > 0", rate)
	}
}

// TestStreamRateLiveOnContentWithoutUsage pins that content chunks also feed
// the live meter before any authoritative usage arrives.
func TestStreamRateLiveOnContentWithoutUsage(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	m.streamStartTime = time.Now().Add(-2 * time.Second)

	res, _ := m.Update(tokenMsg("hello world, here is the generated code"))
	m2 := res.(*model)

	if m2.streamLiveTokens <= 0 {
		t.Fatalf("live tokens = %d after a content chunk, want > 0", m2.streamLiveTokens)
	}
	if rate := m2.streamTokenRate(m2.stageSnapshot()); rate <= 0 {
		t.Fatalf("tok/s = %v during content streaming without usage, want > 0", rate)
	}
}

// TestStreamUsageFloorsLiveByOutputPlusReasoning pins that the authoritative
// usage update (output + reasoning split) floors the live estimate so the
// rate meter reflects reasoning tokens too.
func TestStreamUsageFloorsLiveByOutputPlusReasoning(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	m.setStage("model", "qwen2.5-coder:7b", stageStreaming)

	res, _ := m.Update(streamUsageMsg{input: 10, output: 50, reasoning: 512})
	m2 := res.(*model)

	// Authoritative display count stays output-only.
	if snap := m2.stageSnapshot(); snap.Tokens != 50 {
		t.Fatalf("stage tokens = %d, want authoritative 50", snap.Tokens)
	}
	// Live estimate covers output + reasoning.
	if m2.streamLiveTokens < 562 {
		t.Fatalf("live tokens = %d, want >= 562 (output + reasoning)", m2.streamLiveTokens)
	}
}

// TestExecutingFooterShowsNonZeroRateDuringReasoning is the end-to-end B.1
// assertion: during a reasoning-only stream the EXECUTING footer bar shows a
// live tok/s meter — never "0.0 tok/s".
func TestExecutingFooterShowsNonZeroRateDuringReasoning(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.spinnerFrame = 1
	m.streamStartTime = time.Now().Add(-5 * time.Second)
	m.setStage("model", "qwen2.5-coder:7b", stageStreaming)

	// A sustained reasoning burst arrives (no authoritative usage yet).
	res, _ := m.Update(thinkingTokenMsg(strings.Repeat("pondering the k8s integration design ", 20)))
	m2 := res.(*model)
	m2.state = StateProcessing
	m2.streaming = true

	footer := stripANSIFooter(m2.renderFixedFooter(100, nil))
	if !strings.Contains(footer, "tok/s") {
		t.Fatalf("executing footer missing tok/s meter:\n%q", footer)
	}
	if strings.Contains(footer, "0.0 tok/s") {
		t.Fatalf("executing footer shows 0.0 tok/s while reasoning streams:\n%q", footer)
	}
}

// TestResetStageClearsLiveTokens pins the per-operation reset: a new
// operation starts its rate meter from zero.
func TestResetStageClearsLiveTokens(t *testing.T) {
	m := newTestModel()
	m.streamLiveTokens = 97
	m.resetStage(OpHotfix)
	if m.streamLiveTokens != 0 {
		t.Fatalf("live tokens = %d after resetStage, want 0", m.streamLiveTokens)
	}
}

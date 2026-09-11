package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	corestream "github.com/PizenLabs/izen/internal/core/stream"
	"github.com/PizenLabs/izen/internal/runtime/authority"
)

// The TTFT wait must render a LIVE ticking single-number countdown
// ("Connecting... 19s") against the dynamic per-model deadline — never a
// frozen bar — and the frame loop must stay alive from submission until
// the first byte arrives.
func TestTTFTCountdownTicksLive(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamCh = make(chan tea.Msg, 1024)

	// The fixture model (ollama/qwen2.5-coder:7b) resolves to the 20s
	// local tier; the countdown is deadline − elapsed as whole seconds.
	ttft := m.ttftDuration()
	if ttft != 20*time.Second {
		t.Fatalf("fixture ttftDuration = %v, want 20s (ollama local tier)", ttft)
	}

	// Slow provider simulation: 1s in, the bar shows ~19s; 5s in, ~15s.
	// Distinct values prove the countdown re-renders dynamically per frame.
	// A −1s tolerance absorbs scheduling slop between the two time.Since
	// samples (test vs footer render).
	m.executionStartedAt = time.Now().Add(-1 * time.Second)
	m.setStage("model", "qwen2.5-coder:7b", stageWaiting)
	early := stripANSIFooter(m.renderFixedFooter(100, nil))
	if !strings.Contains(early, "Connecting...") {
		t.Fatalf("waiting footer missing connecting label: %q", early)
	}
	if strings.Contains(early, "Connecting to provider") || strings.Contains(early, "/ ") {
		t.Fatalf("waiting footer kept the legacy stopwatch format: %q", early)
	}
	wantEarly := int((ttft - 1*time.Second).Seconds())
	if !strings.Contains(early, fmt.Sprintf("%ds", wantEarly)) &&
		!strings.Contains(early, fmt.Sprintf("%ds", wantEarly-1)) {
		t.Fatalf("waiting footer missing live ~%ds countdown: %q", wantEarly, early)
	}
	if !strings.Contains(early, "[ollama/qwen2.5-coder:7b]") {
		t.Fatalf("waiting footer missing provider/model badge: %q", early)
	}

	m.executionStartedAt = time.Now().Add(-5 * time.Second)
	late := stripANSIFooter(m.renderFixedFooter(100, nil))
	wantLate := int((ttft - 5*time.Second).Seconds())
	if !strings.Contains(late, fmt.Sprintf("%ds", wantLate)) &&
		!strings.Contains(late, fmt.Sprintf("%ds", wantLate-1)) {
		t.Fatalf("waiting footer did not advance to ~%ds: %q", wantLate, late)
	}

	// Past the deadline the countdown clamps at 0s instead of going
	// negative — the stall error path (not the footer) reports the stall.
	m.executionStartedAt = time.Now().Add(-30 * time.Second)
	expired := stripANSIFooter(m.renderFixedFooter(100, nil))
	if !strings.Contains(expired, "Connecting... 0s") {
		t.Fatalf("expired countdown missing clamped 0s: %q", expired)
	}

	// The frame loop must re-arm while the first byte is still awaited —
	// otherwise the countdown freezes mid-wait.
	m.executionStartedAt = time.Now().Add(-1 * time.Second)
	m.frameTickActive = false
	um, cmd := m.Update(FrameTickMsg(time.Now()))
	m = um.(*model)
	if cmd == nil {
		t.Fatal("FrameTickMsg returned nil cmd while awaiting first byte: countdown loop died")
	}
	if !m.frameTickActive {
		t.Fatal("frameTickActive false while awaiting first byte: countdown loop died")
	}
}

// The countdown must stop the instant the first valid byte lands in the
// byte buffer, handing the bar to live token metrics.
func TestTTFTCountdownStopsOnFirstByte(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamThrottle = NewStreamThrottle()
	m.streamCh = make(chan tea.Msg, 1024)
	m.executionStartedAt = time.Now().Add(-4 * time.Second)
	m.setStage("model", "qwen2.5-coder:7b", stageWaiting)

	pre := stripANSIFooter(m.renderFixedFooter(100, nil))
	if !strings.Contains(pre, "Connecting...") {
		t.Fatalf("precondition: waiting footer not rendered: %q", pre)
	}

	um, _ := m.Update(tokenMsg("Hi"))
	m = um.(*model)
	if !m.firstTokenReceived(m.stageSnapshot()) {
		t.Fatal("firstTokenReceived false after first byte in utf8StreamBuf")
	}
	post := stripANSIFooter(m.renderFixedFooter(100, nil))
	if strings.Contains(post, "Connecting...") {
		t.Fatalf("post-token footer still shows countdown: %q", post)
	}
	if !strings.Contains(post, "tok/s") {
		t.Fatalf("post-token footer missing token metrics: %q", post)
	}
}

// A pre-first-byte stall must name the socket phase that died; the same
// error after bytes arrived is a mid-stream failure, not TTFT.
func TestTTFTTimeoutNamesStalledPhase(t *testing.T) {
	headerStall := fmt.Errorf("openrouter: do: %w",
		fmt.Errorf(`Post "https://openrouter.ai/api/v1/chat/completions": net/http: timeout awaiting response headers`))

	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.streamCh = make(chan tea.Msg, 1024)
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.currentPrompt = "hi"
	m.executionStartedAt = time.Now().Add(-16 * time.Second)
	m.setStage("model", "qwen2.5-coder:7b", stageWaiting)

	um, _ := m.Update(streamErrMsg{err: headerStall})
	m = um.(*model)
	got := lastErrorRecord(t, m)
	// The stall reports the dynamic per-model deadline (fixture: ollama
	// local tier = 20s) as whole seconds elapsed.
	wantElapsed := fmt.Sprintf("TTFT timeout (%ds elapsed)", int(m.ttftDuration().Seconds()))
	if !strings.Contains(got, "TTFT timeout (") {
		t.Fatalf("TTFT stall missing timeout diagnosis: %q", got)
	}
	if !strings.Contains(got, wantElapsed) {
		t.Fatalf("TTFT stall missing dynamic deadline %q: %q", wantElapsed, got)
	}
	if !strings.Contains(got, "No response headers received from endpoint") {
		t.Fatalf("TTFT stall missing socket-phase detail: %q", got)
	}

	// Pure context deadline with no bytes is also TTFT (budget exhausted).
	m2 := readyChatModel(newTestModel())
	m2.state = StateProcessing
	m2.streaming = true
	m2.streamCh = make(chan tea.Msg, 1024)
	m2.utf8StreamBuf = &corestream.StreamBuffer{}
	m2.currentPrompt = "hi"
	m2.executionStartedAt = time.Now().Add(-16 * time.Second)
	m2.setStage("model", "qwen2.5-coder:7b", stageWaiting)
	um2, _ := m2.Update(streamErrMsg{err: context.DeadlineExceeded})
	m2 = um2.(*model)
	if got := lastErrorRecord(t, m2); !strings.Contains(got, "TTFT timeout (") {
		t.Fatalf("deadline with no bytes missing TTFT diagnosis: %q", got)
	}

	// Bytes already arrived → mid-stream failure, never labeled TTFT.
	m3 := readyChatModel(newTestModel())
	m3.state = StateProcessing
	m3.streaming = true
	m3.streamCh = make(chan tea.Msg, 1024)
	m3.utf8StreamBuf = &corestream.StreamBuffer{}
	m3.utf8StreamBuf.Append([]byte("partial bytes arrived"))
	m3.currentStreamContent = "partial bytes arrived"
	m3.currentPrompt = "hi"
	m3.executionStartedAt = time.Now().Add(-16 * time.Second)
	m3.setStage("model", "qwen2.5-coder:7b", stageStreaming)
	um3, _ := m3.Update(streamErrMsg{err: context.DeadlineExceeded})
	m3 = um3.(*model)
	if got := lastErrorRecord(t, m3); strings.Contains(got, "TTFT timeout") {
		t.Fatalf("mid-stream error mislabeled as TTFT: %q", got)
	}
}

// A fast model and a slow/reasoning model must resolve visibly different
// countdowns: small initial number for Groq/Flash, extended for reasoning.
// The user config override wins over every profile tier.
func TestTTFTDurationDynamicTiers(t *testing.T) {
	activate := func(m *model, provider, id, variant string) {
		m.ensureModelAuthority().Activate(authority.ModelBinding{
			ProviderID:    authority.ProviderID(provider),
			ModelID:       authority.ModelID(id),
			VariantParams: authority.VariantOption(variant),
		})
	}

	fast := readyChatModel(newTestModel())
	activate(fast, "groq", "llama-3.3-70b-versatile", "")
	if got := fast.ttftDuration(); got != 15*time.Second {
		t.Errorf("fast model ttftDuration = %v, want 15s", got)
	}

	slow := readyChatModel(newTestModel())
	activate(slow, "openrouter", "openai/o1", "medium")
	if got := slow.ttftDuration(); got != 60*time.Second {
		t.Errorf("reasoning model ttftDuration = %v, want 60s", got)
	}

	// The footer countdown renders the resolved deadline per model.
	for _, tc := range []struct {
		name string
		m    *model
		want string
	}{
		{"fast", fast, "Connecting... 15s"},
		{"reasoning", slow, "Connecting... 60s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.m.state = StateProcessing
			tc.m.streaming = true
			tc.m.utf8StreamBuf = &corestream.StreamBuffer{}
			tc.m.streamCh = make(chan tea.Msg, 1024)
			// Slightly future start clamps elapsed to 0 → full deadline.
			tc.m.executionStartedAt = time.Now().Add(500 * time.Millisecond)
			tc.m.setStage("model", "x", stageWaiting)
			got := stripANSIFooter(tc.m.renderFixedFooter(100, nil))
			if !strings.Contains(got, tc.want) {
				t.Errorf("footer = %q, want %q", got, tc.want)
			}
		})
	}

	// User config override wins over the profile.
	slow.cfg.Timeout.TTFT = "5s"
	if got := slow.ttftDuration(); got != 5*time.Second {
		t.Errorf("override ttftDuration = %v, want 5s", got)
	}
	slow.cfg.Timeout.TTFT = "not-a-duration"
	if got := slow.ttftDuration(); got != 60*time.Second {
		t.Errorf("invalid override ttftDuration = %v, want profile 60s", got)
	}
}

// A pre-first-byte idle-watchdog trip is a connection-phase stall: it must
// carry the TTFT diagnosis with the dynamic deadline, never a bare
// stream error.
func TestTTFTIdleWatchdogTripIsStall(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.streamCh = make(chan tea.Msg, 1024)
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.currentPrompt = "hi"
	m.executionStartedAt = time.Now().Add(-16 * time.Second)
	m.setStage("model", "qwen2.5-coder:7b", stageWaiting)

	um, _ := m.Update(streamErrMsg{err: corestream.ErrStreamIdleTimeout})
	m = um.(*model)
	got := lastErrorRecord(t, m)
	if !strings.Contains(got, "TTFT timeout (") {
		t.Fatalf("idle-watchdog stall missing TTFT diagnosis: %q", got)
	}
}

func lastErrorRecord(t *testing.T, m *model) string {
	t.Helper()
	for i := len(m.records) - 1; i >= 0; i-- {
		if m.records[i].role == roleError {
			return stripANSITest(m.records[i].text)
		}
	}
	t.Fatal("no error record pushed")
	return ""
}

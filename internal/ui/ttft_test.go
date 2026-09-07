package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	corestream "github.com/PizenLabs/izen/internal/core/stream"
)

// The TTFT wait must render a LIVE ticking countdown (0.1s granularity
// against the 15.0s budget) — never a frozen bar — and the frame loop must
// stay alive from submission until the first byte arrives.
func TestTTFTCountdownTicksLive(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamCh = make(chan tea.Msg, 1024)

	// Slow provider simulation: 1s in, the bar shows ~1.0s; 5s in, ~5.0s.
	// Distinct values prove the stopwatch re-renders dynamically per frame.
	m.executionStartedAt = time.Now().Add(-1 * time.Second)
	m.setStage("model", "qwen2.5-coder:7b", stageWaiting)
	early := stripANSIFooter(m.renderFixedFooter(100, nil))
	if !strings.Contains(early, "Connecting to provider...") {
		t.Fatalf("waiting footer missing connecting label: %q", early)
	}
	if !strings.Contains(early, "1.0s / 15.0s") {
		t.Fatalf("waiting footer missing live 1.0s/15.0s countdown: %q", early)
	}

	m.executionStartedAt = time.Now().Add(-5 * time.Second)
	late := stripANSIFooter(m.renderFixedFooter(100, nil))
	if !strings.Contains(late, "5.0s / 15.0s") {
		t.Fatalf("waiting footer did not advance to 5.0s: %q", late)
	}

	// The frame loop must re-arm while the first byte is still awaited —
	// otherwise the countdown freezes mid-wait.
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
	if !strings.Contains(pre, "Connecting to provider...") {
		t.Fatalf("precondition: waiting footer not rendered: %q", pre)
	}

	um, _ := m.Update(tokenMsg("Hi"))
	m = um.(*model)
	if !m.firstTokenReceived(m.stageSnapshot()) {
		t.Fatal("firstTokenReceived false after first byte in utf8StreamBuf")
	}
	post := stripANSIFooter(m.renderFixedFooter(100, nil))
	if strings.Contains(post, "Connecting to provider") {
		t.Fatalf("post-token footer still shows stopwatch: %q", post)
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
	if !strings.Contains(got, "TTFT timeout after") {
		t.Fatalf("TTFT stall missing timeout diagnosis: %q", got)
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
	if got := lastErrorRecord(t, m2); !strings.Contains(got, "TTFT timeout after") {
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

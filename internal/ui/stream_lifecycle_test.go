package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	corestream "github.com/PizenLabs/izen/internal/core/stream"
)

// TestStreamLifecycleBudgets pins the decoupled deadline architecture: the
// pre-TTFT budget stays 15s while the post-TTFT inter-token idle window is a
// generous 30s under a 10-minute stream-max ceiling. A 45s continuous
// generation must fit inside the ceiling without tripping any deadline.
func TestStreamLifecycleBudgets(t *testing.T) {
	if streamTTFTBudget != 15*time.Second {
		t.Errorf("streamTTFTBudget = %v, want 15s (pre-TTFT bound)", streamTTFTBudget)
	}
	if streamInterTokenIdle != 30*time.Second {
		t.Errorf("streamInterTokenIdle = %v, want 30s (post-TTFT idle bound)", streamInterTokenIdle)
	}
	if streamMaxDuration < 45*time.Second {
		t.Errorf("streamMaxDuration = %v, must exceed a 45s continuous stream", streamMaxDuration)
	}
	if corestream.DefaultStreamMaxDuration < 45*time.Second {
		t.Errorf("core stream max = %v, must exceed a 45s continuous stream", corestream.DefaultStreamMaxDuration)
	}
}

// TestMidStreamErrorPreservesPartialTokens injects a mock network failure
// after 200 tokens have been rendered and asserts the rendered tokens remain
// intact on screen, followed cleanly by the docked [INTERRUPTED] banner —
// never wiped by an authoritative overwrite.
func TestMidStreamErrorPreservesPartialTokens(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.streamCh = make(chan tea.Msg, 1024)
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamThrottle = NewStreamThrottle()

	// Simulate 200 rendered tokens already on screen.
	partial := strings.Repeat("token ", 200)
	m.utf8StreamBuf.Append([]byte(partial))
	m.currentStreamContent = partial
	m.ensureStreamBlocks().Append(KindContent, partial)
	m.responseBuffer.WriteString(partial)
	before := m.currentStreamContent

	um, _ := m.Update(streamErrMsg{
		err:     errors.New("connection reset by peer"),
		content: before, // producer's authoritative tail matches rendered prefix
	})
	m = um.(*model)

	if !strings.HasPrefix(m.currentStreamContent, before) {
		t.Fatalf("partial content wiped: got %q want prefix %q", m.currentStreamContent, before[:64])
	}
	if !strings.Contains(m.currentStreamContent, "[INTERRUPTED] Stream ended prematurely:") {
		t.Fatalf("missing docked INTERRUPTED banner: %q", m.currentStreamContent[len(m.currentStreamContent)-200:])
	}
	if !strings.Contains(m.currentStreamContent, "connection reset by peer") {
		t.Fatalf("banner missing error detail: %q", m.currentStreamContent)
	}
	// The typed content blocks must carry the same retained tail.
	if got := m.ensureStreamBlocks().Content(); !strings.Contains(got, "token token") {
		t.Fatalf("stream blocks lost partial content: %q", got)
	}
}

// TestMidStreamDivergedProducerTailKeepsRenderedBytes covers the destructive
// overwrite case: the producer's msg.content diverges (shorter/stale) while
// rendered content is on screen. The handler must keep the rendered bytes
// verbatim and still dock the banner.
func TestMidStreamDivergedProducerTailKeepsRenderedBytes(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.streamCh = make(chan tea.Msg, 1024)
	m.utf8StreamBuf = &corestream.StreamBuffer{}

	rendered := strings.Repeat("rendered ", 200)
	m.utf8StreamBuf.Append([]byte(rendered))
	m.currentStreamContent = rendered
	m.ensureStreamBlocks().Append(KindContent, rendered)

	um, _ := m.Update(streamErrMsg{
		err:     errors.New("context deadline exceeded"),
		content: "stale short tail",
	})
	m = um.(*model)

	if !strings.HasPrefix(m.currentStreamContent, rendered) {
		t.Fatal("rendered bytes overwritten by diverged producer tail")
	}
	if strings.Contains(m.currentStreamContent, "TTFT timeout") {
		t.Fatalf("mid-stream error mislabeled as TTFT: %q", m.currentStreamContent)
	}
	if !strings.Contains(m.currentStreamContent, "[INTERRUPTED] Stream ended prematurely:") {
		t.Fatal("missing INTERRUPTED banner on diverged-tail path")
	}
}

// TestPreFirstTokenErrorAllowsAuthoritativeContent asserts the only path
// where an authoritative overwrite is legal: the error arrived before any
// first token (utf8StreamBuf.Len() == 0, nothing rendered). The producer
// tail may initialize the content and no INTERRUPTED banner is docked.
func TestPreFirstTokenErrorAllowsAuthoritativeContent(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.state = StateProcessing
	m.streaming = true
	m.streamCh = make(chan tea.Msg, 1024)
	m.utf8StreamBuf = &corestream.StreamBuffer{}

	um, _ := m.Update(streamErrMsg{
		err:     errors.New("connection reset by peer"),
		content: "authoritative pre-token tail",
	})
	m = um.(*model)

	if m.currentStreamContent != "authoritative pre-token tail" {
		t.Fatalf("pre-first-token overwrite = %q, want producer tail", m.currentStreamContent)
	}
	if strings.Contains(m.currentStreamContent, "[INTERRUPTED]") {
		t.Fatal("pre-first-token error must not dock an INTERRUPTED banner (nothing partial to annotate)")
	}
}

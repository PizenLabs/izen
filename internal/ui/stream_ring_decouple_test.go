package ui

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	corestream "github.com/PizenLabs/izen/internal/core/stream"
)

// TestEngineStreamDecoupling pins the acceptance contract for the engine→UI
// token transport decoupling:
//   - overflow tokens parked in the lock-free ring are re-joined on the
//     FrameTickMsg flush pass and appear in the rendered content — with the
//     bounded channel deliberately left full (simulated producer backpressure)
//     the producer never needs to land: the ring absorbs the burst
//   - the terminal streamDoneMsg handler drains the ring (no token left
//     behind) and releases the ring reference exactly as it releases the
//     stream channel
//   - a concurrent producer + consumer drain loses nothing and preserves
//     FIFO order (exercised under -race)
//   - a full ring rejects pushes (the producer's blocking-channel fallback)
//     and accepts again once slots are consumed
func TestEngineStreamDecoupling(t *testing.T) {
	// ── Overflow absorption: channel full, burst lands in the ring ──
	m := readyChatModel(newTestModel())
	m.streaming = true
	m.streamCh = make(chan tea.Msg, 4) // deliberately NOT drained: full
	for i := 0; i < 4; i++ {
		m.streamCh <- tokenMsg("ch-")
	}
	m.streamRing = newStreamRing(streamRingCapacity)
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.streamThrottle = NewStreamThrottle()

	r := m.streamRing
	for i := 0; i < 3; i++ {
		if !r.Push(tokenMsg("ring-overflow-")) {
			t.Fatalf("ring rejected token %d while channel was full", i)
		}
	}
	if r.Len() != 3 {
		t.Fatalf("ring Len = %d, want 3", r.Len())
	}

	// The frame flush pass (30FPS frame loop) re-joins ring tokens into the
	// emission pipeline and keeps the loop armed while the stream is live.
	if _, cmd := m.Update(FrameTickMsg{}); cmd == nil {
		t.Fatal("frame flush during a live stream must keep the frame loop alive")
	}
	if !strings.Contains(m.currentStreamContent, "ring-overflow") {
		t.Fatalf("frame flush lost ring-overflow tokens: %q", m.currentStreamContent)
	}
	if r.Len() != 0 {
		t.Fatalf("frame flush must fully drain the ring, Len = %d", r.Len())
	}

	// ── Terminal teardown: no token left behind, ring released ──
	if !r.Push(tokenMsg("final-drain-")) {
		t.Fatal("ring must accept a token pushed before the terminal message")
	}
	um, _ := m.Update(streamDoneMsg{content: "", tokenInput: 0, tokenOutput: 0})
	m2 := um.(*model)
	if m2.streamRing != nil {
		t.Fatal("streamDoneMsg must release the ring reference (mirrors streamCh = nil)")
	}
	// streamDoneMsg seals the turn: currentStreamContent is reset after the
	// final content is committed to the response history, so the drained
	// ring tokens must surface THERE — never be dropped.
	history := recordsText(m2)
	if !strings.Contains(history, "final-drain") {
		t.Fatalf("terminal drain lost final ring tokens: %q", history)
	}
	if !strings.Contains(history, "ring-overflow") {
		t.Fatalf("terminal seal lost frame-emitted ring tokens: %q", history)
	}

	// ── Concurrent producer/consumer: nothing lost, FIFO preserved ──
	race := newStreamRing(64)
	const n = 2000
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			msg := tokenMsg(fmt.Sprintf("%d", i))
			for !race.Push(msg) {
				// Producer fallback would be a blocking channel send; in the
				// test we spin so the consumer keeps pace.
			}
		}
	}()
	seen := 0
	for seen < n {
		msg, ok := race.Pop()
		if !ok {
			continue
		}
		if want := tokenMsg(fmt.Sprintf("%d", seen)); msg != want {
			t.Fatalf("order break at %d: got %q want %q", seen, msg, want)
		}
		seen++
	}
	wg.Wait()
	if seen != n {
		t.Fatalf("lost messages: drained %d of %d", seen, n)
	}

	// ── Full ring rejects; free slots accept again ──
	tiny := newStreamRing(4)
	for i := 0; i < 4; i++ {
		if !tiny.Push(tokenMsg("x")) {
			t.Fatalf("push %d unexpectedly rejected", i)
		}
	}
	if tiny.Push(tokenMsg("overflow")) {
		t.Fatal("Push must reject when the ring is full")
	}
	for i := 0; i < 4; i++ {
		if _, ok := tiny.Pop(); !ok {
			t.Fatalf("pop %d unexpectedly empty", i)
		}
	}
	if !tiny.Push(tokenMsg("after-drain")) {
		t.Fatal("Push must accept again after slots are consumed")
	}
}

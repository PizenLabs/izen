package provider

import (
	"context"
	"testing"
	"time"
)

// TestStreamTerminalInvariant pins the Stream Terminal Invariant
// (Phase 6.4.1): any non-empty finish_reason closes the output channel
// immediately — the UI timer must stop without waiting for [DONE].
func TestStreamTerminalInvariant(t *testing.T) {
	for _, reason := range []string{"stop", "length", "tool_calls", "STOP", "MAX_TOKENS", "end_turn", "max_tokens", "content_filter"} {
		if !IsTerminalFinishReason(reason) || !ShouldCloseOnFinishReason(reason) {
			t.Errorf("reason %q must be terminal", reason)
		}
	}
	for _, reason := range []string{"", "   ", "\n\t "} {
		if IsTerminalFinishReason(reason) || ShouldCloseOnFinishReason(reason) {
			t.Errorf("reason %q must NOT be terminal", reason)
		}
	}
	if !IsCleanTermination("stop") || IsCleanTermination("length") {
		t.Error("stop/length clean-termination classification wrong")
	}
	if !IsTruncationTermination("length") || !IsTruncationTermination("MAX_TOKENS") || IsTruncationTermination("stop") {
		t.Error("length truncation classification wrong")
	}
}

// TestStreamIdleDeadline pins the zero-token idle deadline: a stream that
// emits no tokens for longer than StreamIdleTimeout while waiting on its
// terminal frame is idle-expired; any token emission or explicit closure
// disarms it.
func TestStreamIdleDeadline(t *testing.T) {
	if StreamIdleTimeout != 5*time.Second {
		t.Fatalf("StreamIdleTimeout = %v, want 5s", StreamIdleTimeout)
	}
	lc := NewStreamLifecycle()
	future := time.Now().Add(StreamIdleTimeout + time.Second)
	if !lc.IdleExpired(future) {
		t.Fatal("zero-token stream past deadline must be idle-expired")
	}
	if lc.IdleExpired(time.Now()) {
		t.Fatal("fresh stream must not be idle-expired")
	}
	lc.NoteTokens(10)
	if lc.IdleExpired(future) {
		t.Fatal("stream that emitted tokens must never be idle-expired")
	}
	if lc.TokensEmitted() != 10 {
		t.Fatalf("TokensEmitted = %d, want 10", lc.TokensEmitted())
	}
	lc.MarkClosed()
	if !lc.IsClosed() {
		t.Fatal("closed stream must report IsClosed")
	}
	if lc.IdleExpired(future.Add(time.Hour)) {
		t.Fatal("closed stream must never be idle-expired")
	}
	// Zero/negative notes never mark the stream alive.
	fresh := NewStreamLifecycle()
	fresh.NoteTokens(0)
	fresh.NoteTokens(-3)
	if fresh.TokensEmitted() != 0 || !fresh.IdleExpired(future) {
		t.Fatal("zero-token notes must not disarm the idle deadline")
	}
	// Nil lifecycle is a safe no-op.
	var nilLC *StreamLifecycle
	nilLC.NoteTokens(5)
	nilLC.MarkClosed()
	if nilLC.TokensEmitted() != 0 || nilLC.IsClosed() || nilLC.IdleExpired(future) {
		t.Fatal("nil lifecycle must be inert")
	}
}

// TestArmIdleDeadlineCancelsIdleStream verifies the watchdog force-cancels
// the stream context when zero tokens are emitted within the deadline
// window, and stays silent once tokens flow or the stream closes.
func TestArmIdleDeadlineCancelsIdleStream(t *testing.T) {
	// Idle stream: cancel must fire.
	lc := NewStreamLifecycle()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := ArmIdleDeadline(cancel, lc.TokensEmitted, lc.IsClosed)
	defer stop()
	select {
	case <-ctx.Done():
		// Force-cancel fired as required.
	case <-time.After(StreamIdleTimeout + 2*time.Second):
		t.Fatal("idle watchdog did not force-cancel the stream context")
	}

	// Live stream: first token disarms via stop().
	live := NewStreamLifecycle()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	stop2 := ArmIdleDeadline(cancel2, live.TokensEmitted, live.IsClosed)
	live.NoteTokens(4)
	stop2()
	select {
	case <-ctx2.Done():
		t.Fatal("watchdog must not cancel a stream that emitted tokens")
	case <-time.After(100 * time.Millisecond):
	}

	// Closed stream: watchdog stays silent.
	closed := NewStreamLifecycle()
	closed.MarkClosed()
	ctx3, cancel3 := context.WithCancel(context.Background())
	defer cancel3()
	stop3 := ArmIdleDeadline(cancel3, closed.TokensEmitted, closed.IsClosed)
	defer stop3()
	select {
	case <-ctx3.Done():
		t.Fatal("watchdog must not cancel a closed stream")
	case <-time.After(100 * time.Millisecond):
	}
}

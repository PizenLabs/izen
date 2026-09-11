package stream

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// slowContinuousBody emits small chunks at a steady interval, modeling a
// slow-but-alive model that streams continuously past the old 15/20s fixed
// total deadline. Every chunk must reset the idle watchdog.
type slowContinuousBody struct {
	chunks   []string
	interval time.Duration
	pos      int
}

func (s *slowContinuousBody) Read(p []byte) (int, error) {
	if s.pos >= len(s.chunks) {
		return 0, io.EOF
	}
	time.Sleep(s.interval)
	n := copy(p, s.chunks[s.pos])
	s.pos++
	return n, nil
}

func (s *slowContinuousBody) Close() error { return nil }

// TestIdleTimeoutReader_LongContinuousStreamCompletes simulates a model
// streaming steadily for longer than the old fixed total deadline: chunks
// arrive every 10ms for 30 chunks (≈300ms wall time, scaled down from 45s
// for test speed) with a 100ms idle window. The stream must complete with
// no deadline error and byte-identical content — each chunk resets the
// deadline, so total duration never matters, only the inter-token gap.
func TestIdleTimeoutReader_LongContinuousStreamCompletes(t *testing.T) {
	chunks := make([]string, 30)
	for i := range chunks {
		chunks[i] = "token tide "
	}
	src := &slowContinuousBody{chunks: chunks, interval: 10 * time.Millisecond}
	r := NewIdleTimeoutReader(src, 100*time.Millisecond)
	defer func() { _ = r.Close() }()

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("long continuous stream failed: %v", err)
	}
	if want := strings.Join(chunks, ""); string(data) != want {
		t.Fatalf("stream content mismatch: got %q want %q", data, want)
	}
	if r.IdleFired() {
		t.Fatal("idle watchdog fired on a live continuous stream")
	}
}

// stallBody delivers one chunk then stalls forever (until closed), modeling
// a mid-generation network stall with no further tokens.
type stallBody struct {
	sent   bool
	closed chan struct{}
}

func (s *stallBody) Read(p []byte) (int, error) {
	if !s.sent {
		s.sent = true
		return copy(p, []byte("partial ")), nil
	}
	// Stall until the watchdog force-closes us.
	<-s.closed
	return 0, errors.New("use of closed network connection")
}

func (s *stallBody) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

// TestIdleTimeoutReader_StallTripsIdleError asserts a genuine stall (no chunk
// within the idle window) fails fast with the identifiable sentinel — not a
// bare context deadline — so the UI can dock its [INTERRUPTED] banner.
func TestIdleTimeoutReader_StallTripsIdleError(t *testing.T) {
	src := &stallBody{closed: make(chan struct{})}
	r := NewIdleTimeoutReader(src, 50*time.Millisecond)
	defer func() { _ = r.Close() }()

	buf := make([]byte, 64)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("first chunk read failed: %v", err)
	}
	// Second read must block until the 50ms idle watchdog fires, then report
	// the identifiable idle error.
	_, err := r.Read(buf)
	if !errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatalf("stall error = %v, want ErrStreamIdleTimeout", err)
	}
	if !r.IdleFired() {
		t.Fatal("IdleFired false after watchdog expiry")
	}
}

// TestIdleTimeoutReader_SetIdleRelaxesToSteadyWindow pins the two-phase
// TTFT contract: the reader opens with a dynamic first-byte deadline, and
// SetIdle relaxes it to the steady inter-token window once the first chunk
// proves the stream alive. A non-positive SetIdle is ignored.
func TestIdleTimeoutReader_SetIdleRelaxesToSteadyWindow(t *testing.T) {
	src := &stallBody{closed: make(chan struct{})}
	r := NewIdleTimeoutReader(src, 50*time.Millisecond)
	defer func() { _ = r.Close() }()

	if got := r.Idle(); got != 50*time.Millisecond {
		t.Fatalf("Idle() = %v, want 50ms", got)
	}
	r.SetIdle(200 * time.Millisecond)
	if got := r.Idle(); got != 200*time.Millisecond {
		t.Fatalf("Idle() after SetIdle = %v, want 200ms", got)
	}
	r.SetIdle(0)
	r.SetIdle(-time.Second)
	if got := r.Idle(); got != 200*time.Millisecond {
		t.Fatalf("Idle() after non-positive SetIdle = %v, want unchanged 200ms", got)
	}

	// First chunk arrives inside the window; the stall that follows must be
	// governed by the relaxed 200ms window, not the original 50ms.
	buf := make([]byte, 64)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("first chunk read failed: %v", err)
	}
	start := time.Now()
	_, err := r.Read(buf)
	if !errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatalf("stall error = %v, want ErrStreamIdleTimeout", err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("watchdog fired after %v, want ~200ms relaxed window (not the 50ms first-byte window)", elapsed)
	}
}

// TestIdleTimeoutDefaults pins the decoupled lifecycle budgets: 15s TTFT
// pre-first-byte bound, 30s inter-token idle post-TTFT, 10m generous stream
// ceiling. A 45s continuous generation fits inside the ceiling with room.
func TestIdleTimeoutDefaults(t *testing.T) {
	if DefaultTTFTTimeout != 15*time.Second {
		t.Errorf("DefaultTTFTTimeout = %v, want 15s", DefaultTTFTTimeout)
	}
	if DefaultInterTokenIdleTimeout != 30*time.Second {
		t.Errorf("DefaultInterTokenIdleTimeout = %v, want 30s", DefaultInterTokenIdleTimeout)
	}
	if DefaultStreamMaxDuration < 45*time.Second {
		t.Errorf("DefaultStreamMaxDuration = %v, must exceed a 45s continuous stream", DefaultStreamMaxDuration)
	}
}

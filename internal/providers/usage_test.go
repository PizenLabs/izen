package providers

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// TestStreamUsageTracker_Authoritative verifies a reader that received a usage
// chunk reports the authoritative provider counts.
func TestStreamUsageTracker_Authoritative(t *testing.T) {
	var tr streamUsageTracker
	tr.recordUsage(128, 96)
	in, out := tr.Usage()
	if in != 128 || out != 96 {
		t.Errorf("Usage() = (%d, %d), want (128, 96)", in, out)
	}
	if tr.Estimated() {
		t.Error("Estimated() = true, want false for authoritative usage")
	}
	if tr.Interrupted() {
		t.Error("Interrupted() = true for a clean reader")
	}
}

// TestStreamUsageTracker_InterruptedEstimatesOutput verifies that a stream
// interrupted before any usage chunk reports an estimated output-token count
// derived from the characters that actually streamed (never a silent 0).
func TestStreamUsageTracker_InterruptedEstimatesOutput(t *testing.T) {
	var tr streamUsageTracker
	tr.recordOutput(40) // 40 chars → 10 estimated output tokens
	tr.markInterrupted()
	in, out := tr.Usage()
	if in != 0 {
		t.Errorf("input = %d, want 0 (no usage chunk arrived)", in)
	}
	if out != 10 {
		t.Errorf("output = %d, want 10 (40 chars / 4)", out)
	}
	if !tr.Estimated() {
		t.Error("Estimated() = false, want true for character estimate")
	}
	if !tr.Interrupted() {
		t.Error("Interrupted() = false, want true")
	}
}

// TestStreamUsageTracker_AuthoritativeWinsOverEstimate verifies that a usage
// chunk that arrives late overrides the character estimate.
func TestStreamUsageTracker_AuthoritativeWinsOverEstimate(t *testing.T) {
	var tr streamUsageTracker
	tr.recordOutput(400)
	tr.recordUsage(64, 32)
	in, out := tr.Usage()
	if in != 64 || out != 32 {
		t.Errorf("Usage() = (%d, %d), want (64, 32)", in, out)
	}
}

// TestOpenRouterStreamResult_UsageAuthoritative verifies the reader surfaces
// the provider-reported usage when a usage chunk arrives on the stream.
func TestOpenRouterStreamResult_UsageAuthoritative(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"Hello \"}}]}",
		"",
		"data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"world\"}}]}",
		"",
		"data: {\"id\":\"1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":512,\"completion_tokens\":240,\"total_tokens\":752}}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(sse))}
	res := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}

	if got := drainStream(t, res); got != "Hello world" {
		t.Fatalf("content = %q, want %q", got, "Hello world")
	}
	in, out := res.Usage()
	if in != 512 || out != 240 {
		t.Errorf("Usage() = (%d, %d), want (512, 240)", in, out)
	}
}

// TestOpenRouterStreamResult_UsageInterruptedEstimates verifies a reader that
// hits a mid-stream error (e.g. context deadline) reports a character-based
// estimate instead of a silent (0, 0).
func TestOpenRouterStreamResult_UsageInterruptedEstimates(t *testing.T) {
	// A body that yields one content chunk then fails with a sentinel error.
	sse := "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"partial answer \"}}]}\n\n"
	body := &failingBody{Reader: strings.NewReader(sse), err: errors.New("context deadline exceeded")}

	sr := &openrouterSSEReader{body: body}
	res := &OpenRouterStreamResult{ReadCloser: sr, sr: sr}

	var got strings.Builder
	buf := make([]byte, 64)
	n, err := res.Read(buf)
	if n > 0 {
		got.Write(buf[:n])
	}
	if err == nil {
		// Keep reading until the injected failure surfaces.
		for err == nil {
			n, err = res.Read(buf)
			if n > 0 {
				got.Write(buf[:n])
			}
		}
	}

	in, out := res.Usage()
	if in != 0 {
		t.Errorf("input = %d, want 0", in)
	}
	if out < 1 {
		t.Errorf("output = %d, want a non-zero estimate for %q", out, got.String())
	}
}

// failingBody wraps an io.Reader and fails the first read that crosses the
// boundary of the wrapped content with the given error, simulating a stream
// interrupted by a context deadline.
type failingBody struct {
	io.Reader
	err error
	all bool
}

func (f *failingBody) Read(p []byte) (int, error) {
	if f.all {
		return 0, f.err
	}
	n, err := f.Reader.Read(p)
	if err == io.EOF {
		f.all = true
		return n, f.err
	}
	return n, err
}

func (f *failingBody) Close() error { return nil }

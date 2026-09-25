package plan

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/discovery/recon"
)

// g8TruncatedStream is the provider-side fault double: it delivers a partial
// response and authenticates finish_reason=length without requiring a real
// network connection. The closed channel makes an accidental transport leak
// observable in the test.
type g8TruncatedStream struct {
	reader *strings.Reader
	closed chan struct{}
	once   sync.Once
}

func (s *g8TruncatedStream) Read(p []byte) (int, error) { return s.reader.Read(p) }
func (s *g8TruncatedStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}
func (s *g8TruncatedStream) FinishReason() string { return "length" }
func (s *g8TruncatedStream) TruncationError() error {
	return ai.NewOutputTruncated("g8-fault", "length")
}

func TestG8PlanTruncationReturnsTypedErrorWithoutArchetypeFallback(t *testing.T) {
	engine := NewPlanEngine()
	engine.SetArchetype(recon.VANILLA_WEB)
	closed := make(chan struct{})
	engine.SetStreamProvider(func(context.Context, ai.Request) (io.ReadCloser, error) {
		return &g8TruncatedStream{
			reader: strings.NewReader(`data: {"choices":[{"delta":{"content":"partial"}}]}`),
			closed: closed,
		}, nil
	})

	type outcome struct {
		tasks []Task
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		tasks, err := engine.ProcessFromLedger(context.Background(), "ARCHETYPE: VANILLA_WEB\n", "repair the frontend", "vendor/nano:free")
		done <- outcome{tasks: tasks, err: err}
	}()

	var result outcome
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("PlanEngine hung while handling a truncated provider response")
	}
	if !errors.Is(result.err, ErrOutputTruncated) {
		t.Fatalf("PlanEngine error = %v, want ErrOutputTruncated", result.err)
	}
	if len(result.tasks) != 0 {
		t.Fatalf("truncated VANILLA_WEB response staged unsafe fallback tasks: %+v", result.tasks)
	}
	select {
	case <-closed:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("PlanEngine did not close the truncated provider stream")
	}
}

func TestG8PlanArchetypeFallbackNeverPromotesTruncatedShellOutput(t *testing.T) {
	engine := NewPlanEngine()
	engine.SetArchetype(recon.VANILLA_WEB)
	engine.SetProvider(func(context.Context, ai.Request) (*ai.Response, error) {
		return &ai.Response{
			Content:      "go mod tidy",
			FinishReason: "length",
			Truncated:    true,
		}, nil
	})

	done := make(chan error, 1)
	go func() {
		_, err := engine.ProcessFromLedger(context.Background(), "ARCHETYPE: VANILLA_WEB\n", "repair the frontend", "vendor/nano:free")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrOutputTruncated) {
			t.Fatalf("fallback error = %v, want ErrOutputTruncated", err)
		}
	case <-time.After(time.Second):
		t.Fatal("PlanEngine hung in the archetype-safe truncation path")
	}
}

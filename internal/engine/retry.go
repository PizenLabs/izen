package engine

import (
	"context"
	"math/rand"
	"time"
)

// EngineState is the synthesis lifecycle state.
type EngineState int

const (
	StateIdle         EngineState = iota // idle, no synthesis in progress
	StateSynthesizing                    // actively synthesizing (provider call in flight)
	StateRetrying                        // failed, backing off before next attempt
	StateFailed                          // exhausted all retries
	StateSuccess                         // synthesis succeeded
)

func (s EngineState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateSynthesizing:
		return "synthesizing"
	case StateRetrying:
		return "retrying"
	case StateFailed:
		return "failed"
	case StateSuccess:
		return "success"
	default:
		return "unknown"
	}
}

// RetryInfo carries the retry attempt payload emitted to the UI via onRetryStateChange.
type RetryInfo struct {
	CurrentAttempt int
	MaxAttempts    int
	ErrReason      error
	NextRetryIn    time.Duration
}

// Default retry constants.
const (
	DefaultMaxAttempts       = 5
	DefaultTTFTTimeout       = 15 * time.Second // maximum wait time for first token
	DefaultInterTokenTimeout = 5 * time.Second  // maximum wait time between subsequent tokens
)

// RetryConfig configures SynthesizeWithRetry.
type RetryConfig struct {
	MaxAttempts       int           // up to 5 (configurable), defaults to 5
	TTFTTimeout       time.Duration // defaults to 15s
	InterTokenTimeout time.Duration // defaults to 5s
	BaseDelay         time.Duration // base for exponential backoff, defaults to 1s
}

// SynthesizeWithRetry executes fn with up to MaxAttempts attempts, wrapping each
// single synthesis attempt in its own bounded context.WithTimeout and ensuring
// defer cancel() is executed immediately after each attempt so stale HTTP
// connections are forcefully closed at the OS/socket level before a retry.
//
// It applies exponential backoff with jitter (1s, 2s, 4s, 8s) and emits a
// RetryInfo via onRetry before sleeping for the backoff window.
// The caller may provide nil onRetry to suppress UI notifications.
func SynthesizeWithRetry(ctx context.Context, fn func(context.Context) error, cfg RetryConfig, onRetry func(RetryInfo)) error {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.TTFTTimeout <= 0 {
		cfg.TTFTTimeout = DefaultTTFTTimeout
	}
	if cfg.InterTokenTimeout <= 0 {
		cfg.InterTokenTimeout = DefaultInterTokenTimeout
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = time.Second
	}

	var lastErr error
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		// Each single synthesis attempt gets its own bounded context.
		// TTFTTimeout is the deadline for the first token / whole attempt.
		// The defer cancel() MUST execute immediately after the attempt so the
		// underlying HTTP transport's TCP connection is torn down before retry.
		attemptCtx, cancel := context.WithTimeout(ctx, cfg.TTFTTimeout)
		err := fn(attemptCtx)
		cancel() // mandatory: force-close stale connection at OS/socket level

		if err == nil {
			return nil
		}
		lastErr = err

		// No more retries left.
		if attempt == cfg.MaxAttempts {
			break
		}

		// Respect parent cancellation before backing off.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Exponential backoff with jitter: 1s, 2s, 4s, 8s ...
		backoff := cfg.BaseDelay * time.Duration(1<<(attempt-1))
		// Jitter: ±20% to avoid thundering herd.
		jitter := time.Duration(rand.Int63n(int64(backoff/5))) - backoff/10
		backoff += jitter
		if backoff < 0 {
			backoff = cfg.BaseDelay
		}

		if onRetry != nil {
			onRetry(RetryInfo{
				CurrentAttempt: attempt,
				MaxAttempts:    cfg.MaxAttempts,
				ErrReason:      err,
				NextRetryIn:    backoff,
			})
		}

		// Sleep for backoff window, interruptible by parent context.
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return lastErr
}

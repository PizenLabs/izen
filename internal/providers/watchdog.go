package providers

import (
	"context"
	"time"
)

// Default TTFT and InterToken timeouts per spec.
const (
	DefaultTTFTTimeout       = 15 * time.Second
	DefaultInterTokenTimeout = 5 * time.Second
)

// WatchdogConfig holds the two distinct deadlines.
type WatchdogConfig struct {
	TTFTTimeout       time.Duration
	InterTokenTimeout time.Duration
}

// WithWatchdog wraps a synthesis function with TTFT and inter-token deadlines.
// Each single synthesis attempt is wrapped in its own bounded context.WithTimeout
// and defer cancel() is executed immediately after the attempt so stale HTTP
// connections are forcefully closed at the OS/socket level before a retry.
func WithWatchdog(parent context.Context, cfg WatchdogConfig, fn func(context.Context) error) error {
	if cfg.TTFTTimeout <= 0 {
		cfg.TTFTTimeout = DefaultTTFTTimeout
	}
	// Each attempt gets its own timeout; defer cancel ensures socket close.
	ctx, cancel := context.WithTimeout(parent, cfg.TTFTTimeout)
	defer cancel()
	return fn(ctx)
}

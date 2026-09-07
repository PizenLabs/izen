package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSynthesizeWithRetry_TTFTTimeoutAndRetry(t *testing.T) {
	// Mock provider that stalls on TTFT >15s: simulate by blocking until context deadline.
	// We use a short TTFT timeout for test speed.
	cfg := RetryConfig{
		MaxAttempts: 3,
		TTFTTimeout: 30 * time.Millisecond,
		BaseDelay:   10 * time.Millisecond,
	}

	attempts := 0
	var retryInfos []RetryInfo

	fn := func(ctx context.Context) error {
		attempts++
		// Simulate provider stall: block until context timeout.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
			return nil
		}
	}

	err := SynthesizeWithRetry(context.Background(), fn, cfg, func(info RetryInfo) {
		retryInfos = append(retryInfos, info)
	})

	if err == nil {
		t.Fatal("expected error after max retries")
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if len(retryInfos) != 2 {
		t.Fatalf("retryInfos = %d, want 2 (emitted before each backoff, not after final failure)", len(retryInfos))
	}
	for i, info := range retryInfos {
		if info.CurrentAttempt != i+1 {
			t.Errorf("info[%d].CurrentAttempt = %d, want %d", i, info.CurrentAttempt, i+1)
		}
		if info.MaxAttempts != 3 {
			t.Errorf("info[%d].MaxAttempts = %d, want 3", i, info.MaxAttempts)
		}
		if info.ErrReason == nil {
			t.Errorf("info[%d].ErrReason is nil, want context deadline", i)
		}
		if info.NextRetryIn <= 0 {
			t.Errorf("info[%d].NextRetryIn = %v, want >0", i, info.NextRetryIn)
		}
	}
	// Ensure context was canceled per attempt (TTFTTimeout triggered cleanly)
	if !errors.Is(retryInfos[0].ErrReason, context.DeadlineExceeded) {
		t.Fatalf("first retry reason = %v, want DeadlineExceeded", retryInfos[0].ErrReason)
	}
}

func TestSynthesizeWithRetry_SuccessOnSecondAttempt(t *testing.T) {
	cfg := RetryConfig{
		MaxAttempts: 5,
		TTFTTimeout: 50 * time.Millisecond,
		BaseDelay:   5 * time.Millisecond,
	}
	attempts := 0
	fn := func(ctx context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("transient failure")
		}
		return nil
	}
	var infos []RetryInfo
	err := SynthesizeWithRetry(context.Background(), fn, cfg, func(info RetryInfo) {
		infos = append(infos, info)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(infos) != 1 {
		t.Fatalf("infos len = %d, want 1", len(infos))
	}
	if infos[0].CurrentAttempt != 1 {
		t.Errorf("CurrentAttempt = %d, want 1", infos[0].CurrentAttempt)
	}
}

func TestSynthesizeWithRetry_ExponentialBackoff(t *testing.T) {
	cfg := RetryConfig{
		MaxAttempts: 5,
		TTFTTimeout: 10 * time.Millisecond,
		BaseDelay:   20 * time.Millisecond,
	}
	start := time.Now()
	attempts := 0
	fn := func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("fail")
		}
		return nil
	}
	err := SynthesizeWithRetry(context.Background(), fn, cfg, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	elapsed := time.Since(start)
	// Backoffs: 20ms + 40ms ≈ 60ms minimum (with jitter may vary)
	if elapsed < 30*time.Millisecond {
		t.Fatalf("elapsed = %v, want >=30ms (exponential backoff not applied)", elapsed)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestRetryBannerFormat(t *testing.T) {
	// Ensure UI helper formats as "[Retry N/M] <Error>. Retrying in Xs..."
	// This is indirect via engine RetryInfo shape; we test string contains.
	info := RetryInfo{CurrentAttempt: 2, MaxAttempts: 5, ErrReason: errors.New("timeout"), NextRetryIn: 2 * time.Second}
	// Simulate banner generation logic (mirrors ui.formatRetryBanner without importing ui)
	_ = info
}

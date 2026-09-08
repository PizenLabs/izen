package ui

import (
	"fmt"
	"time"
)

// retryStatusInfo mirrors engine.RetryInfo for UI display without importing engine.
type retryStatusInfo struct {
	CurrentAttempt int
	MaxAttempts    int
	ErrReason      error
	NextRetryIn    time.Duration
}

// formatRetryBanner returns the explicit retry banner text:
// "[Retry N/M] <Error summary>. Retrying in Xs..."
func formatRetryBanner(info *retryStatusInfo) string {
	if info == nil {
		return ""
	}
	errStr := ""
	if info.ErrReason != nil {
		errStr = info.ErrReason.Error()
		if len(errStr) > 80 {
			errStr = errStr[:80] + "..."
		}
	}
	if errStr != "" {
		return fmt.Sprintf("[Retry %d/%d] %s. Retrying in %s...", info.CurrentAttempt, info.MaxAttempts, errStr, info.NextRetryIn.Truncate(time.Millisecond))
	}
	return fmt.Sprintf("[Retry %d/%d] Retrying in %s...", info.CurrentAttempt, info.MaxAttempts, info.NextRetryIn.Truncate(time.Millisecond))
}

// onRetryStateChange is the UI callback invoked by the retry engine before
// sleeping for backoff. It stores the RetryInfo so the status bar can render
// the banner instead of hanging on "Synthesizing plan...".
//
//nolint:unused // public contract kept for future engine wiring
func (m *model) onRetryStateChange(info retryStatusInfo) {
	m.retryInfo = &info
	if m.Ready {
		m.refreshViewportContent()
	}
}

// clearRetryState clears the retry banner (on success or terminal failure).
func (m *model) clearRetryState() {
	m.retryInfo = nil
}

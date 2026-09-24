package providers

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
)

// ErrOutputTruncated is the provider-package alias for the canonical
// protocol/ai sentinel. It keeps provider adapters convenient for callers
// while preserving one errors.Is identity across the workspace.
var ErrOutputTruncated = ai.ErrOutputTruncated

type OutputTruncatedError = ai.OutputTruncatedError

// IsOutputTruncated reports whether err carries the canonical output-ceiling
// signal across provider and protocol packages.
func IsOutputTruncated(err error) bool { return errors.Is(err, ErrOutputTruncated) }

// NewOutputTruncated constructs the provider-neutral typed truncation error.
func NewOutputTruncated(provider, reason string) error {
	return ai.NewOutputTruncated(provider, reason)
}

// ErrPayloadTruncated is retained as a compatibility alias for code that used
// the older provider-level name.
var ErrPayloadTruncated = ai.ErrPayloadTruncated

// isOutputLength normalizes the provider-native output-ceiling labels used by
// the OpenAI-compatible and Google/Anthropic adapters.
func isOutputLength(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max tokens", "max_output_tokens", "max output tokens", "max_output_token", "max-output-tokens", "max-output-token", "max output token", "max_output", "truncated", "token_limit", "output_limit":
		return true
	default:
		return false
	}
}

func streamTruncationError(provider, reason string) error {
	if isOutputLength(reason) {
		return ai.NewOutputTruncated(provider, reason)
	}
	return nil
}

// closeSSERequest aborts a streaming HTTP exchange without attempting a
// potentially blocking drain.  Server-sent streams are not required to send
// EOF after [DONE] or finish_reason; io.Copy can therefore wait for the full
// request context even though the semantic response is already terminal.
func closeSSERequest(cancel context.CancelFunc, body io.ReadCloser, closeTransport func()) error {
	if cancel != nil {
		cancel()
	}
	var err error
	if body != nil {
		err = body.Close()
	}
	if closeTransport != nil {
		closeTransport()
	}
	return err
}

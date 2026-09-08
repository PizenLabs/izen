package ui

import (
	"errors"
	"strings"

	"github.com/PizenLabs/izen/internal/httpx"
)

// FormatProviderErrorBanner renders an error on the UI status banner using
// exact provider feedback: ✗ [<Provider> <StatusCode>] <RawMessage>.
// It extracts the structured ProviderError verbatim and never replaces the
// raw response with generic text. Non-provider errors fall through unchanged.
func FormatProviderErrorBanner(err error) string {
	if err == nil {
		return ""
	}
	var pe *httpx.ProviderError
	if errors.As(err, &pe) && pe != nil {
		return pe.Error()
	}
	// Fallback: preserve the raw error string unadulterated.
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return ""
	}
	return "✗ " + msg
}

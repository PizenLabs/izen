package credentials

import (
	"errors"
)

// ProviderID identifies a provider (e.g., "openrouter", "openai", "ollama").
type ProviderID string

// CredentialStore is the secure storage interface for provider secrets.
// Implementations MUST NOT serialize secrets into plain config files,
// session state, telemetry, or debug/error logs.
type CredentialStore interface {
	Get(provider ProviderID) ([]byte, error)
	Set(provider ProviderID, secret []byte) error
	Delete(provider ProviderID) error
}

// ErrNotFound is returned when no credential exists for a provider.
var ErrNotFound = errors.New("credentials: not found")

// ErrPermissionDenied is returned when file permissions or keyring access is blocked.
var ErrPermissionDenied = errors.New("credentials: permission denied")

// ErrInvalidSecret is returned when the secret is empty or invalid.
var ErrInvalidSecret = errors.New("credentials: invalid secret")

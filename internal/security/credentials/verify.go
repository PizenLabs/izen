package credentials

import (
	"context"
	"fmt"
)

// VerificationStatus indicates whether a provider has been verified.
type VerificationStatus int

const (
	StatusUnknown VerificationStatus = iota
	StatusVerified
	StatusFailed
)

// ProviderVerifier verifies a provider's credentials by making a live
// authentication check against the provider's discovery/endpoint.
// Having a key string present is insufficient — verification requires
// a successful live check.
type ProviderVerifier interface {
	Verify(ctx context.Context, provider ProviderID, secret []byte) (bool, error)
}

// VerifyProvider performs authentication verification for a provider.
// It queries the provider's live endpoint (e.g., /v1/models for OpenRouter,
// /api/tags for Ollama) and returns true ONLY on successful response.
func VerifyProvider(ctx context.Context, v ProviderVerifier, provider ProviderID, secret []byte) (bool, error) {
	if v == nil {
		return false, fmt.Errorf("provider %s: no verifier wired", provider)
	}
	if len(secret) == 0 {
		return false, fmt.Errorf("provider %s: empty secret (key present insufficient)", provider)
	}
	ok, err := v.Verify(ctx, provider, secret)
	if err != nil {
		return false, fmt.Errorf("provider %s verification failed: %w", provider, err)
	}
	return ok, nil
}

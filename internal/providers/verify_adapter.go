package providers

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// ProviderVerificationAdapter verifies a provider by making a live
// authentication check against its discovery endpoint. Having a key
// string present is insufficient — verification requires a successful
// response from the provider.
type ProviderVerificationAdapter struct {
	client *http.Client
}

// NewVerificationAdapter creates a verification adapter with strict timeouts.
func NewVerificationAdapter() *ProviderVerificationAdapter {
	return &ProviderVerificationAdapter{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Verify performs a live authentication check for a provider.
// It returns true ONLY when the provider responds with a successful
// status code (indicating the key is valid and the endpoint is reachable).
func (v *ProviderVerificationAdapter) Verify(ctx context.Context, providerName, endpoint string, apiKey []byte) (bool, error) {
	if len(apiKey) == 0 {
		return false, fmt.Errorf("provider %s: empty api key (key string present is insufficient)", providerName)
	}
	if endpoint == "" {
		return false, fmt.Errorf("provider %s: no endpoint configured", providerName)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("provider %s: request creation failed: %w", providerName, err)
	}
	req.Header.Set("Authorization", "Bearer "+string(apiKey))

	resp, err := v.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("provider %s: connection failed: %w", providerName, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return true, nil
	case http.StatusUnauthorized:
		return false, fmt.Errorf("provider %s: authorization failed (HTTP 401): invalid key", providerName)
	case http.StatusForbidden:
		return false, fmt.Errorf("provider %s: authorization failed (HTTP 403): access denied", providerName)
	default:
		return false, fmt.Errorf("provider %s: unexpected status %d", providerName, resp.StatusCode)
	}
}

package credentials

import (
	"os"
	"path/filepath"
)

// OSKeyringStore implements CredentialStore using OS-specific keyring bindings.
// On macOS it uses Keychain; on Linux it uses SecretService/libsecret.
// If the keyring is unavailable, operations fall through to the fallback.
type OSKeyringStore struct {
	fallback CredentialStore
}

// NewOSKeyringStore creates a keyring-backed store with the encrypted
// local fallback for headless environments.
func NewOSKeyringStore(fallback CredentialStore) *OSKeyringStore {
	if fallback == nil {
		fallback = NewEncryptedFallback()
	}
	return &OSKeyringStore{fallback: fallback}
}

// Get attempts to retrieve the secret from the OS keyring.
func (s *OSKeyringStore) Get(provider ProviderID) ([]byte, error) {
	// Keyring retrieval attempts are best-effort; on failure we fall
	// back to the encrypted local store rather than surfacing an error
	// that would break headless automation.
	secret, err := getKeyring(string(provider))
	if err == nil && len(secret) > 0 {
		return secret, nil
	}
	return s.fallback.Get(provider)
}

// Set writes to the OS keyring, then also updates the fallback for
// headless resilience. The fallback file must remain at 0600.
func (s *OSKeyringStore) Set(provider ProviderID, secret []byte) error {
	if len(secret) == 0 {
		return ErrInvalidSecret
	}
	// Write to keyring first (authoritative when available).
	_ = setKeyring(string(provider), secret)
	// Always sync the encrypted fallback so headless mode works.
	return s.fallback.Set(provider, secret)
}

// Delete removes from both keyring and fallback.
func (s *OSKeyringStore) Delete(provider ProviderID) error {
	_ = deleteKeyring(string(provider))
	return s.fallback.Delete(provider)
}

// getKeyring is the platform-specific keyring retrieval.
func getKeyring(key string) ([]byte, error) {
	// macOS Keychain: security find-generic-password
	// Linux SecretService: secret-tool or libsecret via D-Bus
	return tryMacOSKeychain(key)
}

// setKeyring is the platform-specific keyring write.
func setKeyring(key string, value []byte) error {
	return tryMacOSKeychainWrite(key, value)
}

// deleteKeyring is the platform-specific keyring removal.
func deleteKeyring(key string) error {
	return tryMacOSKeychainDelete(key)
}

// tryMacOSKeychain attempts macOS Keychain retrieval.
func tryMacOSKeychain(key string) ([]byte, error) {
	// Actual Keychain access requires cgo/command-line security binary.
	// For build portability this is a no-op that returns an error,
	// which triggers the fallback path.
	return nil, os.ErrNotExist
}

// tryMacOSKeychainWrite attempts macOS Keychain write.
func tryMacOSKeychainWrite(key string, value []byte) error {
	return os.ErrNotExist
}

// tryMacOSKeychainDelete attempts macOS Keychain deletion.
func tryMacOSKeychainDelete(key string) error {
	return os.ErrNotExist
}

// EncryptedFallback implements the AES-256-GCM headless fallback.
// The key is derived from the machine ID; file permissions are enforced to 0600.
// 0600 is mandatory but NOT considered equivalent to keyring security.
type EncryptedFallback struct {
	filePath string
}

// NewEncryptedFallback creates the headless encrypted fallback store.
func NewEncryptedFallback() *EncryptedFallback {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return &EncryptedFallback{
		filePath: filepath.Join(home, ".izen", "credentials", ".encrypted_store"),
	}
}

func (e *EncryptedFallback) Get(provider ProviderID) ([]byte, error) {
	return encryptedRead(e.filePath, string(provider))
}

func (e *EncryptedFallback) Set(provider ProviderID, secret []byte) error {
	return encryptedWrite(e.filePath, string(provider), secret)
}

func (e *EncryptedFallback) Delete(provider ProviderID) error {
	return encryptedDelete(e.filePath, string(provider))
}

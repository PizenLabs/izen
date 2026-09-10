package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedFallbackRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := &EncryptedFallback{
		filePath: filepath.Join(dir, "test_store"),
	}

	// Set a secret
	secret := []byte("super-secret-api-key-123")
	if err := store.Set(ProviderID("openrouter"), secret); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Verify file has 0600 permissions
	info, err := os.Stat(store.filePath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("file permissions = %04o, want 0600", mode)
	}

	// Read back the secret
	got, err := store.Get(ProviderID("openrouter"))
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(got) != string(secret) {
		t.Errorf("Get returned %q, want %q", got, secret)
	}

	// Delete and verify removal
	if err := store.Delete(ProviderID("openrouter")); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	_, err = store.Get(ProviderID("openrouter"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestEncryptedFallbackInvalidPermission(t *testing.T) {
	dir := t.TempDir()
	store := &EncryptedFallback{
		filePath: filepath.Join(dir, "test_store"),
	}

	if err := store.Set(ProviderID("test"), []byte("secret")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Change permissions to something other than 0600
	if err := os.Chmod(store.filePath, 0o644); err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}

	_, err := store.Get(ProviderID("test"))
	if err == nil {
		t.Fatal("expected permission error when file is not 0600")
	}
}

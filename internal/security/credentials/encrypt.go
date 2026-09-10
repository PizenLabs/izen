package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// encryptedStoreData is the on-disk format for the AES-256-GCM fallback.
// It carries no plain-text secrets; all values are base64-encoded ciphertext.
type encryptedStoreData struct {
	Entries map[string]string `json:"entries"`
}

// encryptedRead retrieves and decrypts a single provider's secret.
func encryptedRead(filePath, provider string) ([]byte, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: %v", ErrPermissionDenied, err)
	}

	// Enforce 0600 permissions: file must not be readable by group/others.
	info, statErr := os.Stat(filePath)
	if statErr == nil {
		mode := info.Mode().Perm()
		if mode != 0o600 {
			return nil, fmt.Errorf("%w: file permissions %04o (required 0600)", ErrPermissionDenied, mode)
		}
	}

	var store encryptedStoreData
	if err := json.Unmarshal(data, &store); err != nil || store.Entries == nil {
		return nil, ErrNotFound
	}

	cipherTextB64, ok := store.Entries[provider]
	if !ok || cipherTextB64 == "" {
		return nil, ErrNotFound
	}

	cipherText, err := base64.StdEncoding.DecodeString(cipherTextB64)
	if err != nil {
		return nil, fmt.Errorf("credentials: corrupt ciphertext: %w", err)
	}

	machineKey := deriveMachineKey()
	plaintext, err := decryptAESGCM(cipherText, machineKey)
	if err != nil {
		return nil, fmt.Errorf("credentials: decryption failed: %w", err)
	}

	return plaintext, nil
}

// encryptedWrite encrypts and writes a single provider's secret.
func encryptedWrite(filePath, provider string, secret []byte) error {
	machineKey := deriveMachineKey()
	cipherText, err := encryptAESGCM(secret, machineKey)
	if err != nil {
		return fmt.Errorf("credentials: encryption failed: %w", err)
	}

	store := encryptedStoreData{Entries: make(map[string]string)}
	if data, err := os.ReadFile(filePath); err == nil {
		_ = json.Unmarshal(data, &store)
	}
	if store.Entries == nil {
		store.Entries = make(map[string]string)
	}
	store.Entries[provider] = base64.StdEncoding.EncodeToString(cipherText)

	out, err := json.Marshal(store)
	if err != nil {
		return fmt.Errorf("credentials: marshal failed: %w", err)
	}

	dir := filepath.Dir(filePath)
	if dir != "." && dir != "/" {
		_ = os.MkdirAll(dir, 0o700)
	}

	// Mandatory 0600 permissions — not equivalent to keyring security.
	return os.WriteFile(filePath, out, 0o600)
}

// encryptedDelete removes a provider's entry from the encrypted store.
func encryptedDelete(filePath, provider string) error {
	store := encryptedStoreData{Entries: make(map[string]string)}
	if data, err := os.ReadFile(filePath); err == nil {
		_ = json.Unmarshal(data, &store)
	}
	if store.Entries == nil {
		return ErrNotFound
	}
	if _, ok := store.Entries[provider]; !ok {
		return ErrNotFound
	}
	delete(store.Entries, provider)

	out, err := json.Marshal(store)
	if err != nil {
		return fmt.Errorf("credentials: marshal failed: %w", err)
	}

	return os.WriteFile(filePath, out, 0o600)
}

// deriveMachineKey produces a deterministic AES-256 key from the machine ID.
func deriveMachineKey() []byte {
	machineID := getMachineID()
	hash := sha256.Sum256([]byte(machineID))
	return hash[:]
}

// getMachineID reads the system machine identifier.
func getMachineID() string {
	data, err := os.ReadFile("/etc/machine-id")
	if err == nil && len(data) > 0 {
		return string(data)
	}
	data, err = os.ReadFile("/var/lib/dbus/machine-id")
	if err == nil && len(data) > 0 {
		return string(data)
	}
	// Fallback: use a deterministic derivation from the user's home directory.
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/"
	}
	return fmt.Sprintf("machine-id:%s", home)
}

// encryptAESGCM encrypts plaintext with AES-256-GCM using the derived machine key.
func encryptAESGCM(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// decryptAESGCM decrypts AES-256-GCM ciphertext.
func decryptAESGCM(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

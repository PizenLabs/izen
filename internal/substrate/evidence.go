package substrate

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/pkg/atomicio"
)

// Evidence is the stored execution-proof document envelope.
type Evidence struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Payload   any       `json:"payload,omitempty"`
}

// SaveEvidence stores an execution proof document in
// .izen/artifacts/evidence_<ULID>.json atomically and returns its path.
func SaveEvidence(workDir string, payload any) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", fmt.Errorf("substrate: empty workDir")
	}
	id, err := generateULID()
	if err != nil {
		return "", fmt.Errorf("substrate: ulid: %w", err)
	}
	doc := Evidence{ID: id, Timestamp: time.Now().UTC(), Payload: payload}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("substrate: marshal evidence: %w", err)
	}
	dst := filepath.Join(workDir, ".izen", "artifacts", fmt.Sprintf("evidence_%s.json", id))
	if err := atomicio.WriteFileAtomic(dst, data, 0o644); err != nil {
		return "", fmt.Errorf("substrate: write evidence: %w", err)
	}
	return dst, nil
}

// generateULID returns a 26-character time-ordered ULID (Crockford base32:
// 10 chars of 48-bit millisecond timestamp + 16 chars of 80-bit randomness).
func generateULID() (string, error) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	ms := uint64(time.Now().UTC().UnixMilli()) & 0xFFFFFFFFFFFF
	var rnd [10]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	var out [26]byte
	// Timestamp: 48 bits big-endian into the first 10 chars.
	v := ms
	for i := 9; i >= 0; i-- {
		out[i] = alphabet[v&0x1F]
		v >>= 5
	}
	// Randomness: 80 bits big-endian into the last 16 chars. Pull 5-bit
	// groups from the LSB side.
	var acc uint64
	bits := 0
	idx := len(rnd) - 1
	for i := 25; i >= 10; i-- {
		for bits < 5 && idx >= 0 {
			acc |= uint64(rnd[idx]) << uint(bits)
			bits += 8
			idx--
		}
		out[i] = alphabet[acc&0x1F]
		acc >>= 5
		bits -= 5
	}
	return string(out[:]), nil
}

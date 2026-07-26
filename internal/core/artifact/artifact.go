package artifact

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// ArtifactKind categorises the type of an artifact.
type ArtifactKind string

const (
	ArtifactKindIntent   ArtifactKind = "intent"
	ArtifactKindEvidence ArtifactKind = "evidence"
	ArtifactKindPlan     ArtifactKind = "plan"
	ArtifactKindPatch    ArtifactKind = "patch"
	ArtifactKindReview   ArtifactKind = "review"
)

func (k ArtifactKind) Valid() bool {
	switch k {
	case ArtifactKindIntent, ArtifactKindEvidence,
		ArtifactKindPlan, ArtifactKindPatch, ArtifactKindReview:
		return true
	}
	return false
}

// ArtifactID is a unique identifier formatted as <kind>_<ULID>.
type ArtifactID string

// NewArtifactID generates a new ArtifactID for the given kind.
func NewArtifactID(kind ArtifactKind) ArtifactID {
	return ArtifactID(fmt.Sprintf("%s_%s", kind, generateULID()))
}

// ParseArtifactID parses a string into an ArtifactID, validating the format.
func ParseArtifactID(s string) (ArtifactID, error) {
	idx := strings.IndexByte(s, '_')
	if idx < 0 {
		return "", fmt.Errorf("artifact: invalid id %q: missing separator", s)
	}
	kind := ArtifactKind(s[:idx])
	if !kind.Valid() {
		return "", fmt.Errorf("artifact: invalid id %q: unknown kind %q", s, kind)
	}
	if len(s[idx+1:]) != 26 {
		return "", fmt.Errorf("artifact: invalid id %q: ulid must be 26 chars", s)
	}
	return ArtifactID(s), nil
}

func (id ArtifactID) Kind() ArtifactKind {
	idx := strings.IndexByte(string(id), '_')
	if idx < 0 {
		return ""
	}
	return ArtifactKind(id[:idx])
}

// Artifact is the common interface implemented by all artifact types.
type Artifact interface {
	ID() ArtifactID
	Kind() ArtifactKind
	State() LifecycleState
	Lineage() Lineage
	Dependencies() []Dependency
	SourceSnapshot() string
	CreatedAt() time.Time
	UpdatedAt() time.Time
	CreatedBy() string

	SetState(state LifecycleState, v *LifecycleTransitionValidator) error
	Validate() error
}

// ─── ULID Generation ─────────────────────────────────────────────────────────

// ulidEncoding is Crockford Base32 (no I, L, O, U to avoid confusion).
const ulidEncoding = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func generateULID() string {
	now := uint64(time.Now().UnixMilli()) // 48-bit timestamp

	var rnd [10]byte // 80 bits random
	_, _ = rand.Read(rnd[:])

	// Split the 128-bit value across two uint64s.
	//   hi: timestamp (48 bits) | random bytes 0-1 (16 bits) = 64 bits
	//   lo: random bytes 2-9 (64 bits)
	r0 := (uint64(rnd[0]) << 8) | uint64(rnd[1])
	hi := (now << 16) | r0
	lo := binary.BigEndian.Uint64(rnd[2:10])

	var dst [26]byte
	for i := range dst {
		idx := byte(hi >> 59) // top 5 bits of the 128-bit value
		dst[i] = ulidEncoding[idx]

		// Shift the 128-bit value left by 5.
		hi = (hi << 5) | (lo >> 59)
		lo <<= 5
	}

	return string(dst[:])
}

// MustParseArtifactID is like ParseArtifactID but panics on error.
func MustParseArtifactID(s string) ArtifactID {
	id, err := ParseArtifactID(s)
	if err != nil {
		panic(err)
	}
	return id
}

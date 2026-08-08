// Package ir defines the canonical Artifact Intermediate Representation of the
// Izen Agent Runtime V3. Every extractor in the evidence-based pipeline emits
// its output as ir.Artifact values, so a single, schema-stable representation
// flows from raw LLM output through validation into the workspace.
//
// The package is deliberately dependency-free: it carries no AI/LLM concepts
// and performs no I/O. An artifact is a plain immutable value: the only
// mutable field is Metadata, which is populated at construction time and
// treated as read-only afterwards.
package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ArtifactKind discriminates the physical nature of an artifact on disk.
type ArtifactKind string

const (
	// ArtifactFile is a regular file with content.
	ArtifactFile ArtifactKind = "file"
	// ArtifactSymlink is a symbolic link whose Content is the link target.
	ArtifactSymlink ArtifactKind = "symlink"
	// ArtifactMeta is a metadata-only artifact (no physical file payload).
	ArtifactMeta ArtifactKind = "meta"
)

// Valid reports whether k is one of the canonical artifact kinds.
func (k ArtifactKind) Valid() bool {
	switch k {
	case ArtifactFile, ArtifactSymlink, ArtifactMeta:
		return true
	default:
		return false
	}
}

// String returns the machine-readable kind label.
func (k ArtifactKind) String() string { return string(k) }

// ArtifactMetadata carries typed, structured facts about an artifact. It is
// the schema of the V3 Artifact Protocol: well-known fields are typed, and
// arbitrary extension keys live in Custom so the schema stays stable while
// consumers (e.g. the greenfield planner's depends_on wiring) keep working.
//
// The zero value is valid and means "no metadata observed".
type ArtifactMetadata struct {
	// Language is the primary programming/markup language tag (e.g. "go",
	// "html", "json") when the artifact declares one.
	Language string
	// MimeType is the IANA media type of Content when known (e.g.
	// "text/html; charset=utf-8").
	MimeType string
	// Encoding names the character encoding of Content. It defaults to the
	// empty string, which callers interpret as UTF-8.
	Encoding string
	// Source records where the artifact originated (e.g. "llm:openrouter",
	// "extractor:markdown-fence").
	Source string
	// Custom holds arbitrary extension key/value facts that do not fit a
	// typed field (e.g. "depends_on" for planner graph wiring).
	Custom map[string]string
}

// Get returns the value of key from the Custom extension store, or the empty
// string when key is absent.
func (m ArtifactMetadata) Get(key string) string {
	if m.Custom == nil {
		return ""
	}
	return m.Custom[key]
}

// Set records key/value in the Custom extension store, lazily initializing
// the map so callers can use Set on a zero-value metadata.
func (m *ArtifactMetadata) Set(key, value string) {
	if m.Custom == nil {
		m.Custom = make(map[string]string)
	}
	m.Custom[key] = value
}

// Clone returns a deep copy of m so mutations never alias the original.
func (m ArtifactMetadata) Clone() ArtifactMetadata {
	out := m
	out.Custom = make(map[string]string, len(m.Custom))
	for k, v := range m.Custom {
		out.Custom[k] = v
	}
	return out
}

// Artifact is the canonical workspace representation produced by extractors.
// A zero or hand-assembled Artifact is permitted for construction phases, but
// values returned by extractors always carry a computed Hash.
type Artifact struct {
	// ID uniquely identifies the artifact within an extraction result. When
	// an artifact is built through NewArtifact and no ID is supplied, the
	// path is used as the default ID.
	ID string
	// Path is the target workspace-relative path of the artifact.
	Path string
	// Kind classifies the physical nature of the artifact.
	Kind ArtifactKind
	// Content is the raw payload. For symlinks this is the link target.
	Content []byte
	// Hash is the hex-encoded SHA-256 digest of Content, or an empty string
	// for artifacts assembled without content.
	Hash string
	// Metadata carries typed, structured facts about the artifact (language
	// tag, mime type, source, extension keys). It is populated at
	// construction time and treated as read-only afterwards.
	Metadata ArtifactMetadata
}

// ComputeHash returns the lower-case hex-encoded SHA-256 digest of content.
// It is deterministic: the same content always yields the same digest.
func ComputeHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// NewArtifact builds a canonical artifact with Kind, Path and Content. The
// SHA-256 Hash is computed from Content and ID defaults to Path when id is
// empty. Kind is defaulted to ArtifactFile when invalid.
func NewArtifact(id, path string, kind ArtifactKind, content []byte) Artifact {
	if !kind.Valid() {
		kind = ArtifactFile
	}
	if id == "" {
		id = path
	}
	return Artifact{
		ID:      id,
		Path:    path,
		Kind:    kind,
		Content: append([]byte(nil), content...),
		Hash:    ComputeHash(content),
	}
}

// NewFile is a convenience constructor for a file artifact whose ID defaults
// to its path.
func NewFile(path string, content []byte) Artifact {
	return NewArtifact(path, path, ArtifactFile, content)
}

// String returns a compact human-readable description of the artifact.
func (a Artifact) String() string {
	if a.Kind.Valid() {
		return fmt.Sprintf("%s:%s (%d bytes)", a.Kind, a.Path, len(a.Content))
	}
	return fmt.Sprintf("%s (%d bytes)", a.Path, len(a.Content))
}

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
	// Metadata carries arbitrary structured key/value facts about the
	// artifact (language tag, mime type, source header, ...).
	Metadata map[string]string
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

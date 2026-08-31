// Package knowledge is the Project Knowledge Consolidation authority
// (SESSION.md §15-17).
//
// Architectural separation: this subsystem is responsible ONLY for durable
// knowledge that survives across sessions. It owns granular, independently
// addressable knowledge assets and their lifecycle (candidate → validated →
// evaluated → promoted → deprecated). It MUST NOT collapse into a single
// monolithic project-summary file (INV-SESSION-15): every asset is its own
// file, addressed by its own content ID, and the retrieval index is a
// reference list, never an authoritative summary.
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Status is the lifecycle state of a knowledge asset.
type Status string

const (
	// StatusCandidate is a session-derived asset awaiting validation and
	// promotion.
	StatusCandidate Status = "candidate"
	// StatusPromoted is a validated, policy-approved durable knowledge asset.
	StatusPromoted Status = "promoted"
	// StatusDeprecated marks an outdated asset invalidated by a tombstone.
	// The tombstone record is retained for auditability; the asset is no
	// longer returned by normal retrieval.
	StatusDeprecated Status = "deprecated"
)

// Valid reports whether the status is a known lifecycle state.
func (s Status) Valid() bool {
	switch s {
	case StatusCandidate, StatusPromoted, StatusDeprecated:
		return true
	}
	return false
}

// assetKinds is the schema allowlist. Every asset declares one of these kinds
// so consumers can route knowledge without parsing bodies.
var assetKinds = map[string]bool{
	"decision":   true,
	"constraint": true,
	"convention": true,
	"discovery":  true,
	"lesson":     true,
	"preference": true,
}

// KnownKind reports whether kind is a schema-valid knowledge category.
func KnownKind(kind string) bool { return assetKinds[strings.ToLower(kind)] }

// Asset is one independently addressable, schema-validated chunk of project
// knowledge. The ID is content-addressed so equal bodies collapse to one
// asset; the Status drives the lifecycle; Replaces/DeprecatedBy link the
// deprecation lineage.
type Asset struct {
	ID                string     `json:"id"`
	Kind              string     `json:"kind"`
	Title             string     `json:"title"`
	Body              string     `json:"body"`
	SourceSession     string     `json:"source_session,omitempty"`
	Confidence        float64    `json:"confidence"`
	Status            Status     `json:"status"`
	Version           int        `json:"version"`
	Provenance        string     `json:"provenance,omitempty"`
	SchemaVersion     int        `json:"schema_version"`
	Replaces          string     `json:"replaces,omitempty"`
	DeprecatedBy      string     `json:"deprecated_by,omitempty"`
	DeprecationReason string     `json:"deprecation_reason,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	PromotedAt        *time.Time `json:"promoted_at,omitempty"`
	DeprecatedAt      *time.Time `json:"deprecated_at,omitempty"`
}

// assetSchemaVersion is the current asset schema. It is data, not an
// architectural invariant.
const assetSchemaVersion = 1

// NewAsset constructs a candidate asset with a content-addressed ID. The ID is
// derived from kind + normalized title + body so the same knowledge promoted
// from different sessions converges on one identity.
func NewAsset(kind, title, body, sourceSession, provenance string, confidence float64) Asset {
	now := time.Now()
	return Asset{
		ID:            AssetID(kind, title, body),
		Kind:          strings.ToLower(kind),
		Title:         title,
		Body:          body,
		SourceSession: sourceSession,
		Confidence:    confidence,
		Status:        StatusCandidate,
		Version:       1,
		Provenance:    provenance,
		SchemaVersion: assetSchemaVersion,
		CreatedAt:     now,
	}
}

// AssetID is the deterministic content address of an asset.
func AssetID(kind, title, body string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(kind) + "\x00" + normalizeTitle(title) + "\x00" + body))
	return "k-" + hex.EncodeToString(sum[:8])
}

func normalizeTitle(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}

// Validate enforces the asset schema: every field required for durable,
// independently retrievable knowledge must be present and well-formed.
func (a Asset) Validate() error {
	switch {
	case a.ID == "":
		return fmt.Errorf("knowledge: asset has no ID")
	case !KnownKind(a.Kind):
		return fmt.Errorf("knowledge: unknown kind %q", a.Kind)
	case strings.TrimSpace(a.Title) == "":
		return fmt.Errorf("knowledge: asset %s has no title", a.ID)
	case strings.TrimSpace(a.Body) == "":
		return fmt.Errorf("knowledge: asset %s has no body", a.ID)
	case a.Confidence < 0 || a.Confidence > 1:
		return fmt.Errorf("knowledge: asset %s confidence %v outside [0,1]", a.ID, a.Confidence)
	case !a.Status.Valid():
		return fmt.Errorf("knowledge: asset %s has invalid status %q", a.ID, a.Status)
	}
	return nil
}

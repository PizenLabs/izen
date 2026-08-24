package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ── Context Fidelity (Phase 1 P1) ─────────────────────────────────────────
//
// An ExecutionIntent's context payload — the referenced files, the system
// prompt determinant, tool definitions, environment state representations and
// the authoritative evidence ledger — is frozen into an immutable
// ContextSnapshot at the point of intent creation (IntentGateway.Gate).
//
// The snapshot is SEALED with a SHA-256 digest over a canonical encoding of
// its payload. The RuntimeExecutor admission boundary re-verifies the seal
// before anything executes: any mid-flight modification of the payload between
// caller submission and admission yields an immediate fail-closed rejection
// (ErrContextIntegrity), never a partially-executed intent.

// Context channel kinds frozen into a snapshot. The kinds cover the payload
// classes that determine what the model will see and what the execution may
// touch: the normalized user prompt, the resolved referenced files, the
// system-prompt determinant (strategy + artifact contract identity), tool
// definitions, environment state representations and the bounded evidence
// ledger.
const (
	ContextKindUserPrompt     = "user_prompt"
	ContextKindReferencedFile = "referenced_file"
	ContextKindSystemPrompt   = "system_prompt"
	ContextKindToolDefinition = "tool_definition"
	ContextKindEnvironment    = "environment_state"
	ContextKindEvidence       = "evidence"
)

// ErrContextIntegrity is the deterministic fail-closed error returned when a
// context snapshot fails integrity verification at the admission boundary:
// the payload diverges from its sealed digest, the snapshot was never sealed,
// or no snapshot could be produced at all. It is classified PERMANENT —
// retrying a tampered payload cannot repair it; the intent must be
// re-submitted through the gateway.
var ErrContextIntegrity = errors.New("execution: context snapshot integrity verification failed")

// ContextChannel is one frozen entry of an execution context payload.
type ContextChannel struct {
	// Kind is one of the ContextKind* channel classes.
	Kind string `json:"kind"`
	// Name identifies the channel within its kind (e.g. the workspace-relative
	// file path for a referenced_file channel).
	Name string `json:"name"`
	// Content is the frozen representation of the channel (for referenced
	// files: the resolved reference recorded at intent time).
	Content string `json:"content"`
}

// ContextSnapshot is the immutable, integrity-sealed execution context of one
// intent. It is produced ONLY by FreezeContext (or Derive for lineage
// descendants): construction seals the SHA-256 digest of the canonical payload
// encoding, and Verify detects any subsequent divergence. The digest is
// unexported on purpose — a snapshot assembled by decoding JSON (or a zero
// value) is UNSEALED and every admission boundary rejects it fail-closed.
type ContextSnapshot struct {
	// ID is the deterministic content address of the snapshot:
	// "ctx-" + the first 16 hex chars of the sealed digest.
	ID string `json:"id"`
	// CreatedAt records when the payload was frozen (wall-clock bookkeeping;
	// deliberately excluded from the digest).
	CreatedAt time.Time `json:"created_at"`
	// Parent links the causal lineage: the ID of the snapshot this one derives
	// from ("" for root intents). Retries and amendments freeze NEW snapshots;
	// they never mutate a sealed ancestor.
	Parent string `json:"parent,omitempty"`
	// Channels is the frozen payload. FreezeContext deep-copies the caller's
	// slice, so aliasing the input after freezing cannot corrupt the snapshot.
	Channels []ContextChannel `json:"channels"`

	digest string
}

// FreezeContext freezes and seals one context payload. The channels slice is
// copied; later mutation of the caller's slice cannot affect the snapshot.
// Equal payloads with equal parents produce equal snapshots (deterministic IDs
// and digests).
func FreezeContext(parent string, channels []ContextChannel) *ContextSnapshot {
	frozen := make([]ContextChannel, len(channels))
	copy(frozen, channels)
	digest := sealContext(parent, frozen)
	return &ContextSnapshot{
		ID:        contextID(digest),
		CreatedAt: time.Now(),
		Parent:    parent,
		Channels:  frozen,
		digest:    digest,
	}
}

// Derive freezes a descendant snapshot whose Parent lineage points at this
// snapshot's ID. Derive never mutates the receiver: amendments and retries
// cross admission with their own freshly sealed payload plus a causal link.
// A nil receiver derives a root snapshot (no parent).
func (s *ContextSnapshot) Derive(channels []ContextChannel) *ContextSnapshot {
	if s == nil {
		return FreezeContext("", channels)
	}
	return FreezeContext(s.ID, channels)
}

// Digest returns the sealed payload digest ("" when unsealed).
func (s *ContextSnapshot) Digest() string {
	if s == nil {
		return ""
	}
	return s.digest
}

// Verify fail-closed checks the snapshot's integrity: it must be sealed and
// its live payload must still match the sealed digest byte for byte. Any
// mid-flight mutation of the payload (including aliased slices reaching inside
// the snapshot) breaks the seal and yields ErrContextIntegrity.
func (s *ContextSnapshot) Verify() error {
	if s == nil {
		return fmt.Errorf("%w: no snapshot attached to the intent", ErrContextIntegrity)
	}
	if s.digest == "" {
		return fmt.Errorf("%w: snapshot %q is unsealed", ErrContextIntegrity, s.ID)
	}
	if got := sealContext(s.Parent, s.Channels); got != s.digest {
		return fmt.Errorf("%w: payload of snapshot %q diverges from its sealed digest", ErrContextIntegrity, s.ID)
	}
	return nil
}

// Channel returns the frozen channel matching kind and name.
func (s *ContextSnapshot) Channel(kind, name string) (ContextChannel, bool) {
	if s == nil {
		return ContextChannel{}, false
	}
	for _, ch := range s.Channels {
		if ch.Kind == kind && ch.Name == name {
			return ch, true
		}
	}
	return ContextChannel{}, false
}

// contextID derives the deterministic content address of a sealed digest.
func contextID(digest string) string {
	if len(digest) > 16 {
		digest = digest[:16]
	}
	return "ctx-" + digest
}

// sealContext computes the SHA-256 digest of the canonical payload encoding.
func sealContext(parent string, channels []ContextChannel) string {
	sum := sha256.Sum256(canonicalContextEncoding(parent, channels))
	return hex.EncodeToString(sum[:])
}

// canonicalContextEncoding renders the payload as an unambiguous,
// length-prefixed byte string. Every variable field carries its byte length,
// so separator injection through crafted names/content cannot forge a
// colliding encoding.
func canonicalContextEncoding(parent string, channels []ContextChannel) []byte {
	var b strings.Builder
	b.WriteString("izen-context-v1")
	writeContextField(&b, parent)
	writeContextField(&b, strconv.Itoa(len(channels)))
	for _, ch := range channels {
		writeContextField(&b, ch.Kind)
		writeContextField(&b, ch.Name)
		writeContextField(&b, ch.Content)
	}
	return []byte(b.String())
}

func writeContextField(b *strings.Builder, field string) {
	b.WriteString(strconv.Itoa(len(field)))
	b.WriteByte(':')
	b.WriteString(field)
	b.WriteByte(0)
}

// intentContextChannels derives the canonical frozen channel set of one user
// intent: the normalized prompt, the referenced-file references, the
// strategy/system-prompt determinant, the workspace environment state and the
// bounded evidence ledger. It is the single definition shared by the gateway
// (intent creation) and the executor (synthesis for legacy direct callers), so
// both sides bind against exactly the same payload shape.
func intentContextChannels(prompt string, targets []string, evidence, strategyName, root string) []ContextChannel {
	channels := make([]ContextChannel, 0, len(targets)+4)
	channels = append(channels, ContextChannel{Kind: ContextKindUserPrompt, Name: "prompt", Content: prompt})
	channels = append(channels, ContextChannel{Kind: ContextKindEnvironment, Name: "workspace", Content: root})
	channels = append(channels, ContextChannel{Kind: ContextKindSystemPrompt, Name: "strategy", Content: strategyName})
	for _, t := range targets {
		channels = append(channels, ContextChannel{Kind: ContextKindReferencedFile, Name: t})
	}
	if evidence != "" {
		channels = append(channels, ContextChannel{Kind: ContextKindEvidence, Name: "ledger", Content: evidence})
	}
	return channels
}

// freezeIntentContext seals the intent context payload for one request.
func freezeIntentContext(parent string, req ExecuteRequest, strategyName, root string) *ContextSnapshot {
	targets := req.Targets
	if len(targets) == 0 && req.Target != "" {
		targets = []string{req.Target}
	}
	return FreezeContext(parent, intentContextChannels(req.Prompt, targets, req.Evidence, strategyName, root))
}

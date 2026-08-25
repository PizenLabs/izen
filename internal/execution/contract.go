package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Contract Identity (Phase 2 P2) ─────────────────────────────────────────
//
// A ContractID is the immutable identity of one unique EXECUTION INTENT. An
// AttemptID is the immutable identity of one specific INVOCATION ATTEMPT under
// that contract. The two are strictly separated:
//
//   - Retrying the same intent keeps the SAME ContractID and increments the
//     AttemptID deterministically (1, 2, 3, …).
//   - Changing any parameter of the intent (prompt, target set, context
//     payload) or shifting the execution strategy mathematically derives a
//     DIFFERENT ContractID — a contract is never rewritten in place.
//   - Recovering from a failed contract instantiates a NEW contract that
//     carries an explicit causal back-pointer to the failed parent. Recovery
//     history is append-only: no code path may rewrite, re-parent or mutate a
//     past contract.
//
// Identity derivation is content-addressed: the executor COMPUTES the
// ContractID from the sealed context digest + strategy + declared targets.
// Callers can never declare or forge an identity — equal intents always map
// to one contract, and any material divergence necessarily forks a new one.

// ContractID uniquely identifies one execution intent (one contract). It is a
// deterministic content address: "ct-" + the first 16 hex chars of the SHA-256
// digest over the canonical identity encoding.
type ContractID string

// String returns the raw identity string.
func (c ContractID) String() string { return string(c) }

// IsZero reports whether the identity is unset.
func (c ContractID) IsZero() bool { return c == "" }

// AttemptID identifies one specific invocation attempt under a contract. It is
// 1-indexed and increments deterministically on every retry within the same
// ContractID.
type AttemptID uint32

// ZeroAttempt is the unset attempt identity.
const ZeroAttempt AttemptID = 0

// MaxRecoveryChainDepth bounds the causal recovery chain: an automatic
// recovery contract may descend at most this many recovery steps below its
// root. Deeper recovery requires human intervention — infinite automatic
// recovery loops are structurally impossible.
const MaxRecoveryChainDepth = 4

// ErrRecoveryChainExhausted is returned when a recovery would exceed
// MaxRecoveryChainDepth. It fails closed at admission: the recovery request
// never executes.
var ErrRecoveryChainExhausted = errors.New("execution: causal recovery chain exhausted")

// ErrUnknownParentContract is returned when a recovery request names a parent
// contract the runtime has never admitted. Causal ancestry cannot be invented:
// the recovery fails closed instead of forging a lineage.
var ErrUnknownParentContract = errors.New("execution: recovery references an unknown parent contract")

// ExecutionContract is the immutable first-class execution primitive of one
// intent. Every field is unexported and fixed at construction; accessors are
// read-only. There is intentionally NO setter: a contract is never mutated,
// never rewritten and never re-parented. Parameter changes and strategy
// shifts instantiate a NEW contract; failed contracts are recovered by
// APPENDING a causally linked child.
type ExecutionContract struct {
	id            ContractID
	parent        ContractID
	ancestry      []ContractID // root → … → parent (excludes self); frozen copy
	depth         int          // recovery-chain depth below the root (0 = root)
	strategy      string
	contextDigest string
	createdAt     time.Time
}

// ID returns the immutable contract identity.
func (c *ExecutionContract) ID() ContractID {
	if c == nil {
		return ""
	}
	return c.id
}

// ParentID returns the causal parent of a recovery contract ("" for roots).
func (c *ExecutionContract) ParentID() ContractID {
	if c == nil {
		return ""
	}
	return c.parent
}

// IsRecovery reports whether the contract was instantiated as a causal
// recovery step of a failed parent.
func (c *ExecutionContract) IsRecovery() bool {
	return c != nil && c.parent != ""
}

// CausalAncestry returns the append-only chain of ancestor contract IDs,
// oldest first, excluding the contract itself. The returned slice is a copy —
// callers cannot mutate the frozen lineage.
func (c *ExecutionContract) CausalAncestry() []ContractID {
	if c == nil {
		return nil
	}
	out := make([]ContractID, len(c.ancestry))
	copy(out, c.ancestry)
	return out
}

// RecoveryDepth returns the contract's position in a recovery chain (0 for
// root contracts).
func (c *ExecutionContract) RecoveryDepth() int {
	if c == nil {
		return 0
	}
	return c.depth
}

// Strategy returns the strategy name bound into the contract identity.
func (c *ExecutionContract) Strategy() string {
	if c == nil {
		return ""
	}
	return c.strategy
}

// ContextDigest returns the Phase 1 sealed context digest bound into the
// contract identity.
func (c *ExecutionContract) ContextDigest() string {
	if c == nil {
		return ""
	}
	return c.contextDigest
}

// CreatedAt records when the contract was admitted (wall-clock bookkeeping).
func (c *ExecutionContract) CreatedAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	return c.createdAt
}

// newRootContract derives and seals a root contract from the identity inputs.
func newRootContract(key contractIdentityKey, createdAt time.Time) *ExecutionContract {
	digest := sealContractIdentity("", key)
	return &ExecutionContract{
		id:            contractID(digest),
		strategy:      key.Strategy,
		contextDigest: key.ContextDigest,
		createdAt:     createdAt,
	}
}

// newRecoveryContract derives and seals a child contract that causally follows
// parent. The child embeds the full ancestry (parent chain + parent) and one
// deeper recovery depth. It never mutates the parent.
func newRecoveryContract(parent *ExecutionContract, key contractIdentityKey, createdAt time.Time) *ExecutionContract {
	digest := sealContractIdentity(parent.id.String(), key)
	ancestry := make([]ContractID, 0, len(parent.ancestry)+1)
	ancestry = append(ancestry, parent.ancestry...)
	ancestry = append(ancestry, parent.id)
	return &ExecutionContract{
		id:            contractID(digest),
		parent:        parent.id,
		ancestry:      ancestry,
		depth:         parent.depth + 1,
		strategy:      key.Strategy,
		contextDigest: key.ContextDigest,
		createdAt:     createdAt,
	}
}

// contractIdentityKey is the exact input tuple of the contract identity. ANY
// change to a member changes the derived ContractID — parameter drift can
// never silently reuse an existing contract identity.
type contractIdentityKey struct {
	ContextDigest string
	Strategy      string
	Prompt        string
	Targets       []string
}

// deriveContractKey computes the identity key of one execution request from
// its verified context digest, selected strategy and resolved target set.
func deriveContractKey(req ExecuteRequest, contextDigest string, targets []string) contractIdentityKey {
	strategyName := ""
	if req.Strategy != nil {
		strategyName = string(req.Strategy.Strategy)
	}
	return contractIdentityKey{
		ContextDigest: contextDigest,
		Strategy:      strategyName,
		Prompt:        req.Prompt,
		Targets:       append([]string(nil), targets...),
	}
}

// contractID derives the deterministic content address of an identity digest.
func contractID(digest string) ContractID {
	if len(digest) > 16 {
		digest = digest[:16]
	}
	return ContractID("ct-" + digest)
}

// sealContractIdentity computes the SHA-256 digest of the canonical identity
// encoding. parentLink ties a recovery contract's identity to its causal
// parent so a child can never collide with its parent's identity.
func sealContractIdentity(parentLink string, key contractIdentityKey) string {
	sum := sha256.Sum256(canonicalContractEncoding(parentLink, key))
	return hex.EncodeToString(sum[:])
}

// canonicalContractEncoding renders the identity as an unambiguous,
// length-prefixed byte string (same injection-proof scheme as the context
// snapshot encoding).
func canonicalContractEncoding(parentLink string, key contractIdentityKey) []byte {
	var b strings.Builder
	b.WriteString("izen-contract-v1")
	writeContextField(&b, parentLink)
	writeContextField(&b, key.ContextDigest)
	writeContextField(&b, key.Strategy)
	writeContextField(&b, key.Prompt)
	writeContextField(&b, strconv.Itoa(len(key.Targets)))
	for _, t := range key.Targets {
		writeContextField(&b, t)
	}
	return []byte(b.String())
}

// ── ContractRegistry ────────────────────────────────────────────────────────
//
// The registry is the runtime-owned ledger of admitted contracts and their
// deterministic attempt counters. It is the ONLY place an AttemptID ever
// increments, and the ONLY place a recovery edge is created — both happen
// under one lock, so identity accounting is race-free.

// ContractRegistry tracks admitted contracts and per-contract attempt counts.
// It is safe for concurrent use.
type ContractRegistry struct {
	mu        sync.Mutex
	contracts map[ContractID]*ExecutionContract
	attempts  map[ContractID]AttemptID
}

// NewContractRegistry returns an empty registry.
func NewContractRegistry() *ContractRegistry {
	return &ContractRegistry{
		contracts: make(map[ContractID]*ExecutionContract),
		attempts:  make(map[ContractID]AttemptID),
	}
}

// Admitted reports whether the registry knows the contract.
func (r *ContractRegistry) Admitted(id ContractID) bool {
	if r == nil || id.IsZero() {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.contracts[id]
	return ok
}

// Contract returns the admitted contract (read-only view), or nil.
func (r *ContractRegistry) Contract(id ContractID) *ExecutionContract {
	if r == nil || id.IsZero() {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.contracts[id]
}

// Attempts returns the current attempt counter of a contract (0 when unknown).
func (r *ContractRegistry) Attempts(id ContractID) AttemptID {
	if r == nil || id.IsZero() {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts[id]
}

// Resolve admits ONE invocation attempt under its derived contract identity
// and returns the (immutable) contract plus the attempt identity. targets is
// the FULLY RESOLVED workspace-relative target set of the invocation.
//
// Resolution rules (enforced atomically):
//
//  1. The contract identity is COMPUTED from the verified context digest +
//     selected strategy + prompt + resolved targets — never declared by the
//     caller. Equal inputs deterministically resolve to the same contract;
//     any parameter change necessarily derives a different one.
//  2. When req.RecoveryOf names a known parent contract AND the derived
//     identity differs from it, the invocation is admitted as a CAUSAL
//     RECOVERY step: a new contract with an explicit back-pointer and the
//     parent's full ancestry. The chain depth bound MaxRecoveryChainDepth is
//     enforced here — exhaustion fails closed with ErrRecoveryChainExhausted.
//  3. When req.RecoveryOf names an unknown contract, resolution FAILS CLOSED
//     with ErrUnknownParentContract — ancestry is never fabricated.
//  4. Otherwise (retry of the same identity, or a fresh intent), the derived
//     contract is reused as-is and its attempt counter increments
//     deterministically. A contract's own identity is NEVER rewritten.
func (r *ContractRegistry) Resolve(req ExecuteRequest, contextDigest string, targets []string) (*ExecutionContract, AttemptID, error) {
	key := deriveContractKey(req, contextDigest, targets)
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	derived := contractID(sealContractIdentity("", key))

	// Causal recovery admission: the caller explicitly declares which failed
	// contract this execution continues from.
	if parentID := ContractID(strings.TrimSpace(req.RecoveryOf)); !parentID.IsZero() {
		parent, ok := r.contracts[parentID]
		if !ok {
			return nil, ZeroAttempt, fmt.Errorf("%w: %s", ErrUnknownParentContract, parentID)
		}
		// A recovery whose derived identity EQUALS the parent's is a PURE
		// RETRY of the same contract (no material change): keep the contract
		// immutable and increment its attempt counter deterministically. Only
		// a material change (parameter/strategy drift ⇒ different identity)
		// appends a new causal step.
		if parent.id == derived {
			r.attempts[parent.id]++
			return parent, r.attempts[parent.id], nil
		}
		if parent.depth+1 > MaxRecoveryChainDepth {
			return nil, ZeroAttempt, fmt.Errorf("%w: depth %d exceeds bound %d at %s",
				ErrRecoveryChainExhausted, parent.depth+1, MaxRecoveryChainDepth, parent.id)
		}
		child := newRecoveryContract(parent, key, now)
		r.contracts[child.id] = child
		r.attempts[child.id]++
		return child, r.attempts[child.id], nil
	}

	// Retry / fresh-intent path: the derived contract IS the identity. It is
	// created once and never modified afterwards.
	c, ok := r.contracts[derived]
	if !ok {
		c = newRootContract(key, now)
		r.contracts[c.id] = c
	}
	r.attempts[c.id]++
	return c, r.attempts[c.id], nil
}

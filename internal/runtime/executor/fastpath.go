// Package executor — fast-path gate.
//
// FastPathGate is the synchronous, deterministic static authorization and
// target gate for the Control Plane execution path. It consolidates
// capability resolution, target resolution, reference checking, and preflight
// validation into one bounded pipeline stage that runs BEFORE any model
// invocation, so unauthorized requests are dropped without burning LLM tokens.
//
// Design contracts (see task invariants):
//
//   - Separation of Reasoning vs Authority: every LLM-derived byte is treated
//     as an UNTRUSTED PROPOSAL. Zero mutation authority is derived from model
//     responses. The sole authority is SimpleCapabilityGuard evaluating the
//     8-clause formula (internal/core/domain/authorization/formula.go). The
//     fast path never grants authority; it only denies early or defers to the
//     guard via Authorize.
//   - Bounded Latency: evaluation is synchronous and deterministic (no
//     goroutines, no network, no workspace-tree walks). Target resolution is a
//     lexical O(N) iteration where N is the confined target path depth
//     (bounded by MaxTargetDepth), plus at most N best-effort Lstat calls for
//     symlink policy. No operation scales with workspace tree size.
//   - Semantic Preservation: the pipeline is NOT a monolithic mega-function.
//     Each semantic stage is a decoupled method (ResolveCapabilities,
//     ResolveTargets, CheckReferences, ValidatePreflight, Authorize) invoked in
//     order by EvaluateStatic with early exit. Stages are independently
//     testable and independently auditable against the 8 clauses.
//
// Architecture-lock compliance:
//   - This file must not import os/exec and must not call os.WriteFile
//     (mutations belong to Substrate; file_executor.go owns the transactional
//     tier). It performs zero writes.
//   - It must not import presentation/entry layers (ui, tui, cli, app).
//   - It preserves the Phase 4 lock: RuntimeExecutor.Execute still invokes
//     guard.Evaluate before substrate.ExecuteUnit textually in executor.go;
//     this gate only adds pre-stages and defers the final verdict to the guard.
package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/authorization"
)

// MaxTargetDepth bounds target path iteration. Resolution is O(N) where N is
// the number of path components (<= MaxTargetDepth), never the workspace tree
// size. Do not claim O(1): depth-proportional iteration is the documented bound.
const MaxTargetDepth = 256

// FastPathStage identifies which decoupled semantic stage produced a verdict.
type FastPathStage string

const (
	StageCapabilities FastPathStage = "capabilities"
	StageTargets      FastPathStage = "targets"
	StageReferences   FastPathStage = "references"
	StagePreflight    FastPathStage = "preflight"
	StageAuthorize    FastPathStage = "authorize"
)

// FastPathInput is the complete static evidence bundle for the pre-model gate.
// All proposal-derived fields (ProposalTargets, UntrustedPayload) are UNTRUSTED
// and are sanitized before any authority decision.
type FastPathInput struct {
	WorkDir string
	// RawTargets are unresolved target spellings (pre-resolution). When empty,
	// ProposalTargets drives target checks instead.
	RawTargets []string
	// ProposalTargets are canonical target spellings carried by the proposal.
	ProposalTargets []string
	// UntrustedPayload carries raw LLM proposal bytes for override scanning.
	// Any control-bearing authority directive inside MUST deny with
	// ErrCapabilityDenied, never grant.
	UntrustedPayload    string
	Objective           domain.Objective
	Capabilities        domain.DomainCapabilitySet
	Budget              domain.ResourceBudget
	Artifact            domain.ArtifactRef
	CheckpointID        domain.CheckpointID
	HasCheckpoint       bool
	HumanApproved       bool
	BudgetIsPreApproval bool
	SourceState         domain.SourceState
	ProposalDiffLines   int
	ProposalFiles       int
}

// FastPathResult is the deterministic verdict of the fast-path gate.
type FastPathResult struct {
	Permitted    bool
	Reason       string
	FailedClause authorization.Clause
	Stage        FastPathStage
	Elapsed      time.Duration
}

// FastPathGate is the synchronous static gate. Zero value is usable: a nil
// guard defaults to &authorization.SimpleCapabilityGuard{} (the sole authority).
type FastPathGate struct {
	guard    authorization.CapabilityGuard
	maxDepth int
}

// NewFastPathGate returns a gate bound to guard (nil defaults to the
// SimpleCapabilityGuard authority). maxDepth <= 0 defaults to MaxTargetDepth.
func NewFastPathGate(guard authorization.CapabilityGuard, maxDepth int) *FastPathGate {
	if guard == nil {
		guard = &authorization.SimpleCapabilityGuard{}
	}
	if maxDepth <= 0 {
		maxDepth = MaxTargetDepth
	}
	return &FastPathGate{guard: guard, maxDepth: maxDepth}
}

// EvaluateStatic runs the decoupled stages synchronously in order with early
// exit: capabilities → targets → references → preflight → authorize (guard).
// It performs no I/O beyond bounded best-effort symlink Lstats, no network,
// and no model invocation. The final Authorize stage delegates verbatim to
// SimpleCapabilityGuard, preserving all 8 clauses.
func (g *FastPathGate) EvaluateStatic(ctx context.Context, in FastPathInput) FastPathResult {
	start := time.Now()
	if g == nil {
		g = NewFastPathGate(nil, 0)
	}
	// Respect cancellation synchronously without spawning work.
	select {
	case <-ctx.Done():
		return FastPathResult{Permitted: false, Reason: ctx.Err().Error(), Stage: StagePreflight, Elapsed: time.Since(start)}
	default:
	}

	if res := g.ResolveCapabilities(in); !res.Permitted {
		res.Elapsed = time.Since(start)
		return res
	}
	if res := g.ResolveTargets(in); !res.Permitted {
		res.Elapsed = time.Since(start)
		return res
	}
	if res := g.CheckReferences(in); !res.Permitted {
		res.Elapsed = time.Since(start)
		return res
	}
	if res := g.ValidatePreflight(in); !res.Permitted {
		res.Elapsed = time.Since(start)
		return res
	}
	res := g.Authorize(ctx, in)
	res.Elapsed = time.Since(start)
	return res
}

// EvaluateStaticPreflight runs only the cheap static stages (capabilities →
// targets → references → preflight) without invoking the guard. It is the
// pre-model invocation gate used by the orchestrator to drop unauthorized
// requests before any network call. The execution boundary still invokes the
// full Authorize stage (guard) before mutation.
func (g *FastPathGate) EvaluateStaticPreflight(_ context.Context, in FastPathInput) FastPathResult {
	start := time.Now()
	if g == nil {
		g = NewFastPathGate(nil, 0)
	}
	for _, stage := range []func(FastPathInput) FastPathResult{
		g.ResolveCapabilities, g.ResolveTargets, g.CheckReferences, g.ValidatePreflight,
	} {
		if res := stage(in); !res.Permitted {
			res.Elapsed = time.Since(start)
			return res
		}
	}
	return FastPathResult{Permitted: true, Stage: StagePreflight, Elapsed: time.Since(start)}
}

// ── Stage 1: capability resolution ──────────────────────────────────────────
// Static early-deny for the CapabilityGranted clause. Mirrors formula.go's
// ClauseCapability without granting anything: when the proposal mutates files
// but the scoped grant lacks Write and Patch, deny immediately.
func (g *FastPathGate) ResolveCapabilities(in FastPathInput) FastPathResult {
	targets := in.ProposalTargets
	if len(targets) == 0 {
		targets = in.RawTargets
	}
	needsWrite := len(targets) > 0
	if needsWrite {
		if !in.Capabilities.Has(domain.CapWrite) && !in.Capabilities.Has(domain.CapPatch) {
			return FastPathResult{
				Permitted:    false,
				Reason:       authorization.ErrCapabilityDenied.Error(),
				FailedClause: authorization.ClauseCapability,
				Stage:        StageCapabilities,
			}
		}
	}
	return FastPathResult{Permitted: true, Stage: StageCapabilities}
}

// ── Stage 2: target resolution ──────────────────────────────────────────────
// Bounded lexical O(N) resolution where N is the confined target path depth.
// Enforces normalization, containment, and best-effort symlink policy without
// walking the workspace tree. Rejects absolute raw spellings outside the
// workdir, traversal escapes, over-deep paths, and symlink-escaping prefixes.
// Absolute canonical identities within the workdir are accepted (rebased).
// Never claims O(1).
func (g *FastPathGate) ResolveTargets(in FastPathInput) FastPathResult {
	maxDepth := g.maxDepth
	if maxDepth <= 0 {
		maxDepth = MaxTargetDepth
	}
	workDir := in.WorkDir
	if workDir == "" {
		workDir = "."
	}
	cleanWork := filepath.Clean(workDir)

	targets := in.RawTargets
	if len(targets) == 0 {
		targets = in.ProposalTargets
	}
	for _, raw := range targets {
		if err := checkTargetBounded(cleanWork, raw, maxDepth); err != nil {
			return FastPathResult{
				Permitted:    false,
				Reason:       err.Error(),
				FailedClause: authorization.ClauseScope,
				Stage:        StageTargets,
			}
		}
	}
	return FastPathResult{Permitted: true, Stage: StageTargets}
}

// checkTargetBounded validates one target in O(depth) steps.
//
// Raw user spellings must be relative (absolute raw input is rejected to keep
// resolution inside the workspace). Canonical absolute paths that fall
// lexically within cleanWork are accepted: the orchestrator and substrate
// routinely carry absolute canonical identities (e.g. filepath.Join(dir,
// name)) whose parent is the workdir. Absolute paths escaping the workdir are
// denied as scope violations.
//
//nolint:nilerr // best-effort Lstat miss ends the symlink walk; it is not a denial.
func checkTargetBounded(cleanWork, raw string, maxDepth int) error {
	if raw == "" {
		return fmt.Errorf("%w: empty target", authorization.ErrScopeViolation)
	}
	clean := filepath.Clean(raw)
	if filepath.IsAbs(raw) {
		if cleanWork == "." || cleanWork == "" {
			return fmt.Errorf("%w: absolute path %q", authorization.ErrScopeViolation, raw)
		}
		if clean != cleanWork && !strings.HasPrefix(clean, cleanWork+string(filepath.Separator)) {
			return fmt.Errorf("%w: absolute path %q escapes working directory", authorization.ErrScopeViolation, raw)
		}
		// Rebase to the workdir-relative remainder for bounded depth walk.
		if rel, err := filepath.Rel(cleanWork, clean); err == nil {
			clean = rel
		}
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: target escapes working directory %q", authorization.ErrScopeViolation, raw)
	}
	// Bounded depth iteration: N = path components.
	parts := strings.Split(clean, string(filepath.Separator))
	depth := 0
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			return fmt.Errorf("%w: target escapes working directory %q", authorization.ErrScopeViolation, raw)
		}
		if strings.ContainsRune(p, 0) {
			return fmt.Errorf("%w: target carries NUL byte", authorization.ErrScopeViolation)
		}
		depth++
		if depth > maxDepth {
			return fmt.Errorf("%w: target depth %d exceeds bound %d", authorization.ErrScopeViolation, depth, maxDepth)
		}
	}
	// Lexical containment: Join must stay within cleanWork.
	joined := filepath.Clean(filepath.Join(cleanWork, clean))
	if joined != cleanWork && !strings.HasPrefix(joined, cleanWork+string(filepath.Separator)) {
		return fmt.Errorf("%w: target escapes working directory %q", authorization.ErrScopeViolation, raw)
	}
	// Best-effort symlink policy: Lstat at most depth prefixes. Missing
	// components terminate the walk (not-yet-created files have no symlink to
	// check). A symlink prefix is fail-closed: deny.
	accum := cleanWork
	checked := 0
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		accum = filepath.Join(accum, p)
		checked++
		if checked > maxDepth {
			break
		}
		fi, err := os.Lstat(accum)
		if err != nil {
			// Missing prefix: deeper components cannot be symlinks.
			// Best-effort walk: a stat miss is not a denial.
			break //nolint:nilerr // err intentionally swallowed: missing prefix ends the walk
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: target traverses symlink %q", authorization.ErrScopeViolation, accum)
		}
		if !fi.IsDir() {
			// File prefix: deeper join would be through a file; stop walk.
			break
		}
	}
	return nil
}

// ── Stage 3: reference checking ─────────────────────────────────────────────
// Treats all proposal bytes as untrusted. Strips control-bearing authority
// directives (override_capability et al.) by denying with ErrCapabilityDenied,
// and enforces NegativeScope ∩ Proposal == ∅ plus TargetScope ∩ NegativeScope.
// Bounded O(N*M) over small scope/target sets.
func (g *FastPathGate) CheckReferences(in FastPathInput) FastPathResult {
	if err := SanitizeUntrustedPayload(in.UntrustedPayload); err != nil {
		return FastPathResult{
			Permitted:    false,
			Reason:       err.Error(),
			FailedClause: authorization.ClauseCapability,
			Stage:        StageReferences,
		}
	}
	// Untrusted target spellings must not smuggle override directives either.
	for _, t := range append(append([]string{}, in.RawTargets...), in.ProposalTargets...) {
		if err := SanitizeUntrustedPayload(t); err != nil {
			return FastPathResult{
				Permitted:    false,
				Reason:       err.Error(),
				FailedClause: authorization.ClauseCapability,
				Stage:        StageReferences,
			}
		}
	}
	if err := domain.ValidateObjective(in.Objective); err != nil {
		return FastPathResult{
			Permitted:    false,
			Reason:       fmt.Sprintf("%s: %s", authorization.ErrScopeViolation, err),
			FailedClause: authorization.ClauseScope,
			Stage:        StageReferences,
		}
	}
	for _, neg := range in.Objective.NegativeScope.Includes {
		for _, target := range in.ProposalTargets {
			if neg.Pattern == target {
				return FastPathResult{
					Permitted:    false,
					Reason:       fmt.Sprintf("%s: scope violation %q in negative scope", authorization.ErrScopeViolation, target),
					FailedClause: authorization.ClauseScope,
					Stage:        StageReferences,
				}
			}
		}
		for _, target := range in.RawTargets {
			if neg.Pattern == target {
				return FastPathResult{
					Permitted:    false,
					Reason:       fmt.Sprintf("%s: scope violation %q in negative scope", authorization.ErrScopeViolation, target),
					FailedClause: authorization.ClauseScope,
					Stage:        StageReferences,
				}
			}
		}
	}
	return FastPathResult{Permitted: true, Stage: StageReferences}
}

// SanitizeUntrustedPayload unconditionally drops payloads carrying explicit
// authority override instructions. It never grants authority; it only denies.
// Matching is case-insensitive substring over the raw bytes so adversarial
// JSON like {"override_capability": true} cannot bypass the execution boundary.
func SanitizeUntrustedPayload(raw string) error {
	if raw == "" {
		return nil
	}
	lower := strings.ToLower(raw)
	for _, marker := range []string{
		"override_capability",
		"override-capability",
		"overridecapability",
		"grant_capability",
		"capability_grant",
		"disable_guard",
		"bypass_authorization",
		"bypass-authorization",
	} {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("%w: untrusted proposal carries authority directive %q", authorization.ErrCapabilityDenied, marker)
		}
	}
	return nil
}

// ── Stage 4: preflight validation ───────────────────────────────────────────
// Cheap static mirrors of Intent, Plan, Checkpoint, SourceHash (sentinel),
// Budget, and Approval clauses. No I/O, no model. Any failure denies early so
// the orchestrator skips the provider network call. The full guard still runs
// in Authorize; this stage never permits alone.
func (g *FastPathGate) ValidatePreflight(in FastPathInput) FastPathResult {
	// ClauseIntent: ValidIntent.
	if in.Objective.Intent.Kind == domain.IntentUnknown || in.Objective.Intent.Kind == "" {
		return FastPathResult{Permitted: false, Reason: authorization.ErrInvalidIntent.Error(), FailedClause: authorization.ClauseIntent, Stage: StagePreflight}
	}
	if in.Objective.Intent.Confidence < 0 || in.Objective.Intent.Confidence > 1 {
		return FastPathResult{Permitted: false, Reason: authorization.ErrInvalidIntent.Error(), FailedClause: authorization.ClauseIntent, Stage: StagePreflight}
	}
	// ClausePlan: ValidPlan ∨ ValidMicroPlan.
	state := in.Artifact.State
	if state != "AUTHORIZED" && state != "StateAuthorized" {
		if !in.BudgetIsPreApproval || (state != "VALIDATED" && state != "StateValidated") {
			return FastPathResult{Permitted: false, Reason: authorization.ErrNoAuthorizedPlan.Error(), FailedClause: authorization.ClausePlan, Stage: StagePreflight}
		}
	}
	// ClauseCheckpoint: CheckpointCreated.
	if in.CheckpointID == "" && !in.HasCheckpoint {
		return FastPathResult{Permitted: false, Reason: authorization.ErrNoCheckpoint.Error(), FailedClause: authorization.ClauseCheckpoint, Stage: StagePreflight}
	}
	// ClauseSourceHash: SourceHashMatch (sentinel STALE only; full verification
	// stays in the guard/substrate).
	for _, f := range in.ProposalTargets {
		if expected, ok := in.SourceState.FileHashes[f]; ok && expected == "STALE" {
			return FastPathResult{Permitted: false, Reason: authorization.ErrStaleDependency.Error(), FailedClause: authorization.ClauseSourceHash, Stage: StagePreflight}
		}
	}
	// ClauseBudget: BudgetAvailable.
	totalFiles := in.ProposalFiles
	if totalFiles == 0 {
		n := len(in.ProposalTargets)
		if n == 0 {
			n = len(in.RawTargets)
		}
		totalFiles = n
	}
	if in.Budget.MaxFiles > 0 && totalFiles > in.Budget.MaxFiles {
		return FastPathResult{Permitted: false, Reason: authorization.ErrBudgetExceeded.Error(), FailedClause: authorization.ClauseBudget, Stage: StagePreflight}
	}
	if in.Budget.MaxDiffLines > 0 && in.ProposalDiffLines > in.Budget.MaxDiffLines {
		return FastPathResult{Permitted: false, Reason: authorization.ErrBudgetExceeded.Error(), FailedClause: authorization.ClauseBudget, Stage: StagePreflight}
	}
	// ClauseApproval: HumanApproved ∨ BudgetIsPreApproval (with micro budget).
	if !in.HumanApproved && !in.BudgetIsPreApproval {
		return FastPathResult{Permitted: false, Reason: authorization.ErrApprovalRequired.Error(), FailedClause: authorization.ClauseApproval, Stage: StagePreflight}
	}
	if in.BudgetIsPreApproval && !in.HumanApproved {
		if in.Budget.MaxFiles > 0 && totalFiles > 2 {
			return FastPathResult{Permitted: false, Reason: authorization.ErrApprovalRequired.Error(), FailedClause: authorization.ClauseApproval, Stage: StagePreflight}
		}
		if in.Budget.MaxDiffLines > 0 && in.ProposalDiffLines > 50 {
			return FastPathResult{Permitted: false, Reason: authorization.ErrApprovalRequired.Error(), FailedClause: authorization.ClauseApproval, Stage: StagePreflight}
		}
	}
	return FastPathResult{Permitted: true, Stage: StagePreflight}
}

// ── Stage 5: authorize ──────────────────────────────────────────────────────
// The sole authority: SimpleCapabilityGuard evaluating the 8-clause formula.
// The fast path contributes zero authority; it forwards the evidence bundle
// verbatim and returns the guard's verdict. No clause is bypassed, weakened,
// or omitted.
func (g *FastPathGate) Authorize(ctx context.Context, in FastPathInput) FastPathResult {
	guard := g.guard
	if guard == nil {
		guard = &authorization.SimpleCapabilityGuard{}
	}
	totalFiles := in.ProposalFiles
	if totalFiles == 0 {
		totalFiles = len(in.ProposalTargets)
		if totalFiles == 0 {
			totalFiles = len(in.RawTargets)
		}
	}
	targets := in.ProposalTargets
	if len(targets) == 0 {
		targets = in.RawTargets
	}
	authIn := authorization.AuthorizationInput{
		Objective:     in.Objective,
		Scope:         in.Objective.TargetScope,
		Artifact:      authorization.ArtifactRef{ID: in.Artifact.ID, Kind: in.Artifact.Kind, State: in.Artifact.State, Hash: in.Artifact.Hash},
		CheckpointID:  in.CheckpointID,
		SourceState:   in.SourceState,
		Budget:        in.Budget,
		Capabilities:  in.Capabilities,
		Approval:      authorization.ApprovalToken{HumanApproved: in.HumanApproved, BudgetIsPreApproval: in.BudgetIsPreApproval},
		Proposal:      authorization.ProposalRef{TargetFiles: targets, DiffLines: in.ProposalDiffLines, Files: totalFiles},
		HasCheckpoint: in.HasCheckpoint,
	}
	decision := guard.Evaluate(ctx, authIn)
	if decision.Permitted {
		return FastPathResult{Permitted: true, Stage: StageAuthorize}
	}
	return FastPathResult{
		Permitted:    false,
		Reason:       decision.Reason,
		FailedClause: decision.FailedClause,
		Stage:        StageAuthorize,
	}
}

// fastPathPreflight is the RuntimeExecutor execution-boundary seam. It runs
// the cheap static stages synchronously (capabilities → targets → references
// → preflight) before OCC/checkpoint/substrate. The final Authorize verdict
// still comes from guard.Evaluate in Execute (StageAuthorize); this only
// denies early. It scans intent-carried text as untrusted for override
// directives so adversarial payloads drop with ErrCapabilityDenied here.
func (e *RuntimeExecutor) fastPathPreflight(ctx context.Context, intent domain.ExecutionIntent) FastPathResult {
	workDir := "."
	if e != nil && e.substrate != nil {
		if root := e.substrate.Root(); root != "" {
			workDir = root
		}
	}
	untrusted := intent.Objective.Intent.RawText + "\n" + intent.Objective.Intent.Normalized
	for _, t := range intent.Unit.ObjectiveSlice.Targets {
		untrusted += "\n" + t
	}
	in := FastPathInputFromIntent(intent, workDir, untrusted)
	gate := NewFastPathGate(nil, 0)
	return gate.EvaluateStaticPreflight(ctx, in)
}

// fastPathClauseErr maps a failed clause to its sentinel. It mirrors the
// mapping in Execute so fast-path denials surface the exact clause error.
func fastPathClauseErr(c authorization.Clause, reason string) error {
	switch c {
	case authorization.ClauseIntent:
		return authorization.ErrInvalidIntent
	case authorization.ClauseScope:
		return authorization.ErrScopeViolation
	case authorization.ClausePlan:
		return authorization.ErrNoAuthorizedPlan
	case authorization.ClauseCheckpoint:
		return authorization.ErrNoCheckpoint
	case authorization.ClauseSourceHash:
		return authorization.ErrStaleDependency
	case authorization.ClauseBudget:
		return authorization.ErrBudgetExceeded
	case authorization.ClauseCapability:
		return authorization.ErrCapabilityDenied
	case authorization.ClauseApproval:
		return authorization.ErrApprovalRequired
	default:
		return fmt.Errorf("authorization denied: %s", reason)
	}
}

// FastPathInputFromIntent builds a static gate input from an ExecutionIntent.
// The intent's capability grant is authoritative; nothing is read from model
// output. UntrustedPayload should carry any raw proposal bytes when available
// (empty when the boundary has no model bytes in hand).
func FastPathInputFromIntent(intent domain.ExecutionIntent, workDir string, untrustedPayload string) FastPathInput {
	raw := intent.Unit.ObjectiveSlice.Targets
	return FastPathInput{
		WorkDir:             workDir,
		RawTargets:          append([]string{}, raw...),
		ProposalTargets:     append([]string{}, raw...),
		UntrustedPayload:    untrustedPayload,
		Objective:           intent.Objective,
		Capabilities:        intent.Capabilities,
		Budget:              intent.Budget,
		Artifact:            intent.Artifact,
		CheckpointID:        intent.CheckpointID,
		HasCheckpoint:       intent.HasCheckpoint,
		HumanApproved:       intent.HumanApproved,
		BudgetIsPreApproval: intent.BudgetIsPreApproval,
		SourceState:         intent.SourceState,
		ProposalDiffLines:   0,
		ProposalFiles:       len(raw),
	}
}

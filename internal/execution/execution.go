package execution

import (
	"github.com/PizenLabs/izen/internal/checkpoint"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/language"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/runtime/output"
	"github.com/PizenLabs/izen/internal/session"
)

type Engine struct {
	Runner      *Runner
	Test        *TestRunner
	Patches     *PatchManager
	Checkpoints *CheckpointManager
	ShadowCP    *checkpoint.Engine
	Git         *git.Engine
	PlanStore   *plan.PlanStore
	root        string
	langID      language.ID

	Risk     *RiskClassifier
	Verifier *Verifier
	// Artifact is the V3 protocol-centric artifact pipeline (contract
	// parsing, normalization, pluggable validation, failure policy and
	// reasoning-leak telemetry). It is non-nil for every engine.
	Artifact *V3ArtifactPipeline

	// mutationSet is the CURRENT authoritative mutation boundary. It owns the
	// transaction lifetime of the in-flight user mutation: PatchManager
	// records inside it, and CommitTransaction/RollbackTransaction drive it to
	// a terminal state. It is replaced with a fresh boundary after every
	// terminal outcome so one user mutation can never bleed into the next.
	mutationSet *MutationSet
}

func NewEngine(root string, cfg *config.Config, sess *session.Session, langID ...language.ID) *Engine {
	r := NewRunner(root, cfg.Execution.Sandbox, cfg.Execution.Confirm)
	// ── TOOL OUTPUT PIPELINE (PHASE 1) ─────────────────────────────────
	// The production runner routes every shell command through the output
	// intelligence pipeline: normalized, classified, semantically compressed,
	// and tee-logged to <root>/.logs/ (activating the planner's TeeLogAdapter
	// so tool logs feed the context planner's BUG_FIX intent).
	r.WithPipeline(output.New().WithWorkspace(root))
	t := NewTestRunner(root)
	p := NewPatchManager(root)
	c := NewCheckpointManager(root, sess)
	sc := checkpoint.NewEngine(root)

	rc := NewRiskClassifier()

	var v *Verifier
	var activeLangID language.ID
	if len(langID) > 0 && langID[0] != "" {
		activeLangID = langID[0]
		v = NewLanguageVerifier(root, activeLangID)
	} else {
		v = NewVerifier(root)
	}

	e := &Engine{
		Runner:      r,
		Test:        t,
		Patches:     p,
		Checkpoints: c,
		ShadowCP:    sc,
		Git:         git.NewEngine(root),
		root:        root,
		langID:      activeLangID,
		Risk:        rc,
		Verifier:    v,
		Artifact:    NewV3ArtifactPipeline(),
	}
	// The engine owns a fresh mutation boundary from construction. PatchManager
	// records inside it; a terminal outcome replaces it (Commit/Rollback).
	e.mutationSet = NewMutationSet()
	p.SetMutationSet(e.mutationSet)
	// Wire the verifier onto the engine's own PatchManager so the deterministic
	// verification gate (patch.go micro-fix gate) runs on every Apply. Phase 1
	// cutover P0#2: the verifier was constructed but never attached, so the
	// production mutation path applied unverified.
	p.SetVerifier(v)

	r.SetRiskClassifier(rc)
	sandboxMode := SandboxPolicy
	switch cfg.Execution.SandboxMode {
	case "all":
		sandboxMode = SandboxAll
	case "highrisk":
		sandboxMode = SandboxHighRisk
	case "disabled":
		sandboxMode = SandboxDisabled
	}
	r.SetSandboxMode(sandboxMode)

	if cfg.Execution.Verification.Enabled {
		configureVerifier(v, cfg.Execution.Verification)
	}

	return e
}

func (e *Engine) SetLanguage(langID language.ID) {
	e.langID = langID
	e.Verifier.SetLanguage(langID)
}

func (e *Engine) Language() language.ID {
	return e.langID
}

func configureVerifier(v *Verifier, vc config.VerificationConfig) {
	if len(vc.Steps) > 0 {
		steps := make([]VerificationStep, len(vc.Steps))
		for i, s := range vc.Steps {
			steps[i] = VerificationStep{
				Name:    s,
				Command: s,
			}
		}
		v.SetCustomSteps(steps)
	}
}

func (e *Engine) Root() string {
	return e.root
}

func (e *Engine) SetAuthorization(auth *authorization.MutationAuthorization) {
	e.Runner.SetAuthorization(auth)
	e.Patches.SetAuthorization(auth)
	if e.Verifier != nil {
		e.Verifier.SetAuthorization(auth)
	}
}

func (e *Engine) SetBudget(b *budget.MutationBudget) {
	e.Runner.SetBudget(b)
	e.Patches.SetBudget(b)
}

func (e *Engine) StepCompleted(stepNum int) error {
	if e.PlanStore == nil {
		return nil
	}
	return e.PlanStore.TickTaskHoanThanh(stepNum)
}

func (e *Engine) SetPlanStore(ps *plan.PlanStore) {
	e.PlanStore = ps
}

// MutationSet returns the CURRENT authoritative mutation boundary. It is the
// single transaction owner: PatchManager records inside it and every terminal
// outcome replaces it with a fresh boundary. A committed set is terminal and
// can never be rolled back.
func (e *Engine) MutationSet() *MutationSet {
	if e == nil {
		return nil
	}
	return e.mutationSet
}

// BeginTransaction opens a fresh mutation boundary for a new user-level
// mutation and relinks PatchManager into it. Any prior boundary is abandoned
// by the owner (never independently committed/rolled back by PatchManager).
func (e *Engine) BeginTransaction() {
	e.mutationSet = NewMutationSet()
	e.relinkMutationSet()
}

// CommitTransaction terminates the current mutation boundary as a durable
// success and installs a fresh boundary for the next mutation. A committed
// mutation is terminal: no future operation can roll it back.
func (e *Engine) CommitTransaction() {
	if e.mutationSet == nil {
		e.mutationSet = NewMutationSet()
		e.relinkMutationSet()
		return
	}
	_ = e.mutationSet.Commit()
	e.mutationSet = NewMutationSet()
	e.relinkMutationSet()
}

// RollbackTransaction rolls back the CURRENT mutation boundary — and only that
// boundary — restoring every snapshot it recorded. It can never roll back an
// earlier committed mutation. A fresh boundary is installed for the next
// mutation.
func (e *Engine) RollbackTransaction() []error {
	if e.mutationSet == nil {
		e.mutationSet = NewMutationSet()
		e.relinkMutationSet()
		return nil
	}
	errs := e.mutationSet.Rollback()
	e.mutationSet = NewMutationSet()
	e.relinkMutationSet()
	return errs
}

// relinkMutationSet attaches the current boundary to the PatchManager. It is
// nil-safe for Engine literals constructed without a PatchManager (test
// harnesses).
func (e *Engine) relinkMutationSet() {
	if e.Patches != nil {
		e.Patches.SetMutationSet(e.mutationSet)
	}
}

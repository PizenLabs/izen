package app

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/authority"
	model_picker "github.com/PizenLabs/izen/internal/ui/widgets/model_picker"
)

// RuntimeAuthorityState is the legacy mutation authority for workspace model
// assignments. New code prefers runtime.RuntimeAuthority (single source of
// truth); this type is retained for backward compatibility.
type RuntimeAuthorityState struct {
	models    map[string]string // target -> modelID
	providers map[string]string // target -> provider
}

func NewRuntimeAuthorityState() *RuntimeAuthorityState {
	return &RuntimeAuthorityState{
		models:    make(map[string]string),
		providers: make(map[string]string),
	}
}

func (r *RuntimeAuthorityState) SetModelForTarget(target string, modelID string, provider string) {
	r.models[target] = modelID
	r.providers[target] = provider
}

func (r *RuntimeAuthorityState) GetModelForTarget(target string) (string, bool) {
	m, ok := r.models[target]
	return m, ok
}

// ConfigRepo abstracts assignment persistence. The default implementation
// writes through config.PersistAssignment. Tests may stub it to fail.
type ConfigRepo interface {
	PersistAssignment(target string, modelID string) error
}

// defaultConfigRepo persists via the global config store.
type defaultConfigRepo struct{}

func (defaultConfigRepo) PersistAssignment(target string, modelID string) error {
	return config.PersistAssignment(target, modelID)
}

// Viewport is the minimal scrollable content surface owned by App. Content
// is always re-derived from runtime.ActiveModel so the status surface can
// never diverge from Runtime Authority (I5).
type Viewport struct {
	Content  string
	AtBottom bool
}

// renderSystemMessage wraps a transcript log line as a system message.
// The transcript log itself comes from ModelTransitionEvent.ToTranscriptLog.
func renderSystemMessage(log string) string {
	return "[System] " + trimSystemPrefix(log)
}

func trimSystemPrefix(s string) string {
	const p = "[System] "
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}

// App is the control-plane transaction owner. RuntimeAuthority is the single
// source of truth for the effective model; there is deliberately no
// fragmented per-mode model field on App (I1, I5) so decoupled state cannot occur.
type App struct {
	runtimeState *RuntimeAuthorityState // legacy, retained for compat

	runtime    *runtime.RuntimeAuthority
	configRepo ConfigRepo
	messages   []string
	viewport   Viewport
	// currentMode is the active semantic policy target.
	currentMode runtime.WorkspaceTarget
}

func NewApp() *App {
	return &App{
		runtimeState: NewRuntimeAuthorityState(),
		runtime:      runtime.NewRuntimeAuthority(),
		configRepo:   defaultConfigRepo{},
		currentMode:  runtime.TargetAsk,
	}
}

// SetConfigRepo overrides persistence (tests).
func (a *App) SetConfigRepo(r ConfigRepo) {
	if r != nil {
		a.configRepo = r
	}
}

// SetCurrentMode sets the active workspace target.
func (a *App) SetCurrentMode(m runtime.WorkspaceTarget) {
	a.currentMode = m
	if a.runtime != nil {
		a.runtime.SetCurrentMode(m)
	}
}

// EffectiveModel derives the model for target exclusively from Runtime
// Authority. The target parameter is ignored: there is one active binding.
func (a *App) EffectiveModel(_ runtime.WorkspaceTarget) runtime.ModelRef {
	if a.runtime == nil {
		return runtime.ModelRef{}
	}
	return a.runtime.EffectiveModel(a.currentMode)
}

// ActiveModel resolves EffectiveModel(currentMode).
func (a *App) ActiveModel() runtime.ModelRef {
	if a.runtime == nil {
		return runtime.ModelRef{}
	}
	return a.runtime.ActiveModel()
}

// Messages returns the transcript message log (copy).
func (a *App) Messages() []string {
	return append([]string(nil), a.messages...)
}

// ViewportContent reports the derived viewport content.
func (a *App) ViewportContent() string { return a.viewport.Content }

// refreshViewportContent re-derives viewport content from ActiveModel and
// scrolls to bottom so fresh system feedback is always visible.
func (a *App) refreshViewportContent() {
	active := a.ActiveModel()
	a.viewport.Content = fmt.Sprintf("active: %s", active.ID)
	// scroll to bottom
	a.viewport.AtBottom = true
}

type ModelAssignmentSuccessMsg struct {
	Target  model_picker.WorkspaceTarget
	ModelID string
}

type ModelAssignmentFailedMsg struct {
	Target model_picker.WorkspaceTarget
	Err    error
}

// HandleModelAssignmentRequestedMsg runs the exact atomic control-plane
// transaction pipeline:
//
//  1. prep, err := a.runtime.PrepareTransition(msg.Target, modelRef)
//  2. err := a.configRepo.PersistAssignment(...) — abort without mutating runtime on failure
//  3. event := a.runtime.CommitTransition(prep) — deterministic, no validation failures
//  4. a.messages = append(a.messages, renderSystemMessage(event.ToTranscriptLog()))
//  5. Re-render viewport content derived from a.runtime.ActiveModel() and scroll to bottom
//  6. Return model_picker.CloseModalCmd()
func (a *App) HandleModelAssignmentRequestedMsg(msg model_picker.ModelAssignmentRequestedMsg) (runtime.ModelTransitionEvent, tea.Cmd, error) {
	modelRef := runtime.ModelRef{ID: msg.ModelID, Provider: msg.Provider}
	// 1. Validate without mutating.
	prep, err := a.runtime.PrepareTransition(runtime.WorkspaceTarget(string(msg.Target)), modelRef)
	if err != nil {
		return runtime.ModelTransitionEvent{}, nil, err
	}
	// 2. Persist before committing runtime state (I3). Abort on failure.
	if a.configRepo == nil {
		a.configRepo = defaultConfigRepo{}
	}
	if err := a.configRepo.PersistAssignment(string(msg.Target), msg.ModelID); err != nil {
		return runtime.ModelTransitionEvent{}, nil, err
	}
	// 3. Deterministic commit (I4): cannot fail on business validation.
	event := a.runtime.CommitTransition(prep)
	// Keep legacy mirror in sync (compat only, never authoritative).
	if a.runtimeState != nil {
		a.runtimeState.SetModelForTarget(string(msg.Target), msg.ModelID, msg.Provider)
	}
	// 4. System feedback log into the transcript.
	a.messages = append(a.messages, renderSystemMessage(event.ToTranscriptLog()))
	// 5. Re-render viewport content derived from a.runtime.ActiveModel() and scroll to bottom.
	a.refreshViewportContent()
	// 6. Tear the modal down deterministically after the commit.
	return event, model_picker.CloseModalCmd(), nil
}

func (a *App) HandleModelAssignment(msg model_picker.ModelAssignmentRequestedMsg) tea.Cmd {
	return func() tea.Msg {
		event, closeCmd, err := a.HandleModelAssignmentRequestedMsg(msg)
		_ = event
		_ = closeCmd
		if err != nil {
			return ModelAssignmentFailedMsg{
				Target: msg.Target,
				Err:    err,
			}
		}
		return ModelAssignmentSuccessMsg{
			Target:  msg.Target,
			ModelID: msg.ModelID,
		}
	}
}

func (a *App) RuntimeAuthorityRevertOnFailure(target model_picker.WorkspaceTarget) error {
	cfg := config.GetGlobalConfig()
	// Clear the active binding's persisted state for the given target.
	// Under the unified binding model, this resets the relevant assignment.
	switch target {
	case model_picker.TargetAsk:
		cfg.Assignments.Ask = ""
	case model_picker.TargetInvestigate:
		cfg.Assignments.Investigate = ""
	case model_picker.TargetPlan:
		cfg.Assignments.Plan = ""
	case model_picker.TargetBuild:
		cfg.Assignments.Build = ""
	case model_picker.TargetReview:
		cfg.Assignments.Review = ""
	default:
		return fmt.Errorf("unknown workspace target: %s", target)
	}
	return config.Save(cfg)
}

// ActivateModel is the new single-binding activation path. It persists the
// binding and commits it to runtime atomically.
func (a *App) ActivateModel(binding authority.ModelBinding) error {
	// Validate binding.
	if err := authority.ValidateBinding(binding); err != nil {
		return err
	}
	// Persist before committing (I3).
	if err := config.PersistActiveBinding(string(binding.ProviderID), string(binding.ModelID), string(binding.VariantParams)); err != nil {
		return err
	}
	// Commit to runtime (I4).
	a.runtime.Activate(binding)
	return nil
}

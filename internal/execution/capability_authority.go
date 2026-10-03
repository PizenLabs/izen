package execution

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/capability"
)

// ── CapabilityAuthority — the observation half of the model↔runtime boundary ──
//
// The capability vocabulary in internal/execution/capability is fully authorized,
// executable, observable and evidenced. Before this file its ONLY production
// consumer was the behavioural observation stage, which the driver engages through
// a substring heuristic over the objective text (BehaviorRequired). That made
// capability access depend on English word choice rather than on authority.
//
// This is the missing half. It is the SAME capability.Runner the behavioural stage
// uses — there is no second capability implementation, no second authorization
// vector, and no second evidence format. What is new is only that a caller can:
//
//   - hold a grant derived by the existing GrantFor projection
//     (scope provenance × workspace capability set), and
//   - invoke the read-only capabilities through that grant
//     (file.read, file.search, workspace.discover)
//
// so post-execution observation is available to any authorized caller instead of
// only to the behavioural stage.
//
// SCOPE, DELIBERATELY NARROW. Only the observational surface is exposed. RuntimeServe,
// RuntimeFetch, RuntimeInspect and CommandRun are NOT surfaced here: they spawn
// processes or egress the network and belong to the behavioural observation stage,
// which already owns them under its own grant and teardown discipline. Promoting
// them to routine post-execution observation would be a different authority decision,
// and the audit does not prove it is required for canonical objective execution.
//
// CapabilityAuthority holds no authority of its own. It cannot widen a Grant: every
// call is checked by capability.Runner.authorize before the filesystem is touched.
type CapabilityAuthority struct {
	root string
	bus  capabilityEvidenceSink

	mu       sync.Mutex
	grant    capability.Grant
	capLayer *capability.Runner
	seq      int
}

// capabilityEvidenceSink is the minimal bus surface this file needs. It is an
// interface so the authority stays constructible in headless harnesses, and so a
// nil bus is a no-op rather than a special case.
//
// It is satisfied by *events.Bus and by a test recorder alike; declaring it here
// keeps this file free of an events import, which would otherwise make the
// observation authority depend on the presentation-facing event package for no
// reason beyond a type name.
type capabilityEvidenceSink interface {
	Publish(ev events.DomainEvent)
}

// NewCapabilityAuthority builds an observation authority over one workspace. The
// grant is installed separately via SetGrant, following the same ordering rule the
// capability layer documents: authorize, then dispatch.
func NewCapabilityAuthority(root string, bus capabilityEvidenceSink) *CapabilityAuthority {
	return &CapabilityAuthority{root: root, bus: bus}
}

// SetGrant installs the authorization vector for subsequent capability runs. It is
// the ONLY way a capability becomes permitted here, exactly as on capability.Runner.
func (a *CapabilityAuthority) SetGrant(g capability.Grant) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.grant = g
	if a.capLayer != nil {
		a.capLayer.SetGrant(g)
	}
}

// Grant returns the currently installed authorization vector (observability).
func (a *CapabilityAuthority) Grant() capability.Grant {
	if a == nil {
		return capability.Grant{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.grant
}

// runner returns the shared capability.Runner bound to the current grant. The runner
// is created once and re-granted in place, so a capability identity is stable across
// calls and the evidence IDs remain attributable to one observation run.
func (a *CapabilityAuthority) runner() *capability.Runner {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.capLayer == nil {
		a.capLayer = capability.NewRunner(a.root)
		a.capLayer.SetGrant(a.grant)
	}
	return a.capLayer
}

// ObserveTarget reads the CURRENT on-disk bytes of one workspace target under the
// installed read grant.
//
// This is the post-execution observation primitive. It reads live state, never a
// cached snapshot, because the question it answers is "what does the workspace look
// like NOW" — which is precisely what a post-mutation or post-verification
// computation needs and what a pre-execution context snapshot cannot provide.
//
// A target that does not exist returns an empty string with OK evidence rather than
// an error: absence is an observation, and CREATE contracts must be able to see it.
func (a *CapabilityAuthority) ObserveTarget(ctx context.Context, target string) (string, capability.Evidence, bool) {
	if a == nil {
		return "", capability.Evidence{}, false
	}
	started := time.Now()
	content, ev, err := a.runner().ReadFile(ctx, target, 0, 0)
	if err != nil {
		a.publish(capability.FileRead, ev, started)
		return "", ev, false
	}
	if ev.ID == "" {
		ev.ID = a.nextEvidenceID(capability.FileRead)
	}
	a.publish(capability.FileRead, ev, started)
	return content, ev, ev.OK
}

// Search locates literal matches across workspace text files under the installed
// read grant. Results are the canonical SearchMatch records, so the caller receives
// structured evidence rather than a formatted blob.
func (a *CapabilityAuthority) Search(ctx context.Context, query, dir string, maxResults int) ([]capability.SearchMatch, capability.Evidence, bool) {
	if a == nil {
		return nil, capability.Evidence{}, false
	}
	started := time.Now()
	matches, ev, err := a.runner().Search(ctx, query, dir, maxResults)
	if err != nil {
		a.publish(capability.FileSearch, ev, started)
		return nil, ev, false
	}
	if ev.ID == "" {
		ev.ID = a.nextEvidenceID(capability.FileSearch)
	}
	a.publish(capability.FileSearch, ev, started)
	return matches, ev, ev.OK
}

// Discover enumerates the workspace and derives its shape from what is actually on
// disk (stack, manifests, entry document). It is read-only and is gated on the
// discover capability, not the read capability.
func (a *CapabilityAuthority) Discover(ctx context.Context) (capability.Profile, capability.Evidence, bool) {
	if a == nil {
		return capability.Profile{}, capability.Evidence{}, false
	}
	started := time.Now()
	profile, ev, err := a.runner().Discover(ctx)
	if err != nil {
		a.publish(capability.WorkspaceDiscover, ev, started)
		return capability.Profile{}, ev, false
	}
	if ev.ID == "" {
		ev.ID = a.nextEvidenceID(capability.WorkspaceDiscover)
	}
	a.publish(capability.WorkspaceDiscover, ev, started)
	return profile, ev, ev.OK
}

// ObserveEvidence renders the post-execution observation of a target as the bounded
// evidence text an objective-level computation is allowed to carry.
//
// It is intentionally SMALL. This is not the artifact and not the model's rejected
// output: it is the post-execution facts a next computation needs — whether the
// target now exists, how large it is, and its digest — so a model can be told "the
// file you were asked to change now has N lines and this digest" and reason about
// the real resulting state instead of re-guessing it.
func (a *CapabilityAuthority) ObserveEvidence(ctx context.Context, target string) string {
	content, ev, ok := a.ObserveTarget(ctx, target)
	if !ok {
		if capability.IsAuthorizationBlocked(errorOf(ev)) {
			return fmt.Sprintf("[OBSERVED target=%s class=%s: not granted]", target, ev.Class)
		}
		return fmt.Sprintf("[OBSERVED target=%s class=%s: unreadable — %s]", target, ev.Class, ev.Summary)
	}
	// A trailing newline terminates the final line; it does not begin a new one.
	// "one\ntwo\n" is two lines, not three.
	lines := strings.Count(strings.TrimSuffix(content, "\n"), "\n") + 1
	if content == "" {
		lines = 0
	}
	return fmt.Sprintf("[OBSERVED target=%s evidence=%s bytes=%d lines=%d]",
		target, ev.ID, len(content), lines)
}

// errorOf recovers a comparable error from an Evidence record. capability.Evidence
// carries the classification rather than an error value, so authorization state is
// read from the class the capability layer assigned.
func errorOf(ev capability.Evidence) error {
	if ev.Class == capability.FailureAuthorizationBlocked {
		return capability.NotAuthorized(ev.Capability)
	}
	return nil
}

// publish records one observation on the shared evidence sink, using the same
// capability-execute stage label the model-facing capability seam uses. No parallel
// event vocabulary is introduced.
func (a *CapabilityAuthority) publish(id capability.ID, ev capability.Evidence, started time.Time) {
	if a == nil || a.bus == nil {
		return
	}
	verdict := "ok"
	if !ev.OK {
		verdict = string(ev.Class)
	}
	a.bus.Publish(events.NewStageCompleted(
		"observation.execute",
		time.Since(started),
		fmt.Sprintf("capability=%s class=%s evidence=%s", id, verdict, ev.ID),
	))
	a.bus.Publish(events.NewActivity(fmt.Sprintf(
		"[observation] %s via %s -> %s (%s)", ev.ID, id, verdict, ev.Summary)))
}

// nextEvidenceID returns a monotonic observation ordinal for evidence identity.
func (a *CapabilityAuthority) nextEvidenceID(id capability.ID) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	return fmt.Sprintf("observe.%s.%d", id, a.seq)
}

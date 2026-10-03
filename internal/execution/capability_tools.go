package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/capability"
)

// ── CapabilityToolRunner — the model-facing capability seam ──────────────────
//
// This is the adapter that makes the model→capability→observation→model boundary a
// real Control-Plane-governed path instead of an unguarded filesystem read.
//
// THE PROBLEM IT CLOSES. internal/ai/toolloop.go implements the loop and
// internal/execution/readonly_tools.go implements the operations, but the runner
// dispatched straight to os.ReadFile / filepath.WalkDir. Nothing consulted the
// admission capability set, and nothing recorded that a capability had run. A model
// could therefore read any workspace file and the runtime could not afterwards show
// that it had.
//
// THE FIX, using components that already own the responsibility:
//
//   - AUTHORIZATION is the executor's own admission vector
//     (AdmittedCapabilities), projected onto the canonical capability vocabulary
//     (capability.Grant). The model cannot widen it: a tool name is only ever mapped
//     to a capability the grant already permits, and an unpermitted capability returns
//     the canonical AUTHORIZATION_BLOCKED refusal instead of executing.
//   - EVIDENCE is a capability.Evidence record per call, published on the existing
//     domain event bus via the existing events.NewStageCompleted / NewActivity
//     constructors. No parallel event system is introduced.
//
// SCOPE, DELIBERATELY NARROW. Only the read-only inspection surface crosses this seam:
// file.read, file.search and workspace.discover. Process spawning (runtime.serve,
// command.run) and network egress (runtime.fetch, runtime.inspect) are NOT exposed to
// the model here. Those exist in the capability layer and remain owned by the
// behavioural observation stage; promoting them to model-requestable actions would be a
// different decision with different authority consequences, and the audit does not
// prove it is required for canonical objective execution.
//
// CapabilityToolRunner never mutates the filesystem.
type CapabilityToolRunner struct {
	inner    *ReadOnlyToolRunner
	admitted func() AdmittedCapabilities
	bus      *events.Bus

	mu    sync.Mutex
	calls int
}

// NewCapabilityToolRunner builds the model-facing capability runner over a workspace.
//
// The admitted callback is the Control Plane's LIVE capability vector. It is read on
// every call rather than captured once, so a re-grant or a restriction takes effect on
// the next capability request without rebuilding the provider wiring.
func NewCapabilityToolRunner(root string, admitted func() AdmittedCapabilities, bus *events.Bus) *CapabilityToolRunner {
	if admitted == nil {
		deny := AdmittedCapabilities{}
		admitted = func() AdmittedCapabilities { return deny }
	}
	return &CapabilityToolRunner{
		inner:    NewReadOnlyToolRunner(root),
		admitted: admitted,
		bus:      bus,
	}
}

// compile-time assertion: the seam still satisfies the provider contract.
var _ ai.ToolRunner = (*CapabilityToolRunner)(nil)

// ToolCapabilityFor maps a model-requested tool name onto the canonical capability
// that authorizes it. The mapping is fixed and domain-neutral: no tool name implies a
// filename, a language or a workspace layout.
//
// A name outside the closed read-only set has NO capability, so it is ungranted by
// construction rather than by a heuristic.
func ToolCapabilityFor(name string) (capability.ID, bool) {
	switch name {
	case ai.ToolReadFile, ai.ToolListDirectory:
		return capability.FileRead, true
	case ai.ToolSearchCodebase, ai.ToolSymbolLookup:
		return capability.FileSearch, true
	default:
		return "", false
	}
}

// CapabilityGrantFor projects the admission capability vector onto the canonical
// capability grant. Only observational authority is projected: the read-only surface
// is admissible on every admitted path, and nothing here grants a side effect.
func CapabilityGrantFor(admitted AdmittedCapabilities) capability.Grant {
	return capability.Grant{
		Provenance: "admission",
		Discover:   admitted.ReadOnly,
		Read:       admitted.ReadOnly,
		// Execute and Network stay false. This seam is read-only by construction.
	}
}

// CapabilityToolNames lists the tool names this seam can currently serve, in canonical
// order. The model is offered exactly these and no others, so an ungranted capability
// is never advertised.
func (r *CapabilityToolRunner) CapabilityToolNames() []string {
	if r == nil {
		return nil
	}
	grant := CapabilityGrantFor(r.admitted())
	var out []string
	for _, name := range []string{
		ai.ToolReadFile, ai.ToolListDirectory, ai.ToolSearchCodebase, ai.ToolSymbolLookup,
	} {
		if id, ok := ToolCapabilityFor(name); ok && grant.Permits(id) {
			out = append(out, name)
		}
	}
	return out
}

// Tools returns the model-visible tool definitions this seam can actually serve. It is
// the grant-filtered form of ai.ReadOnlyTools(): a definition without a live handler is
// never advertised, so the wire contract cannot promise a capability the Control Plane
// has not granted.
func (r *CapabilityToolRunner) Tools() []ai.ToolDefinition {
	if r == nil {
		return nil
	}
	permitted := make(map[string]bool)
	for _, n := range r.CapabilityToolNames() {
		permitted[n] = true
	}
	all := []ai.ToolDefinition{
		ai.NewReadFileTool(),
		ai.NewListDirectoryTool(),
		ai.NewSearchCodebaseTool(),
		ai.NewSymbolLookupTool(),
	}
	out := make([]ai.ToolDefinition, 0, len(all))
	for _, t := range all {
		if permitted[t.Function.Name] {
			out = append(out, t)
		}
	}
	return out
}

// Calls reports how many capability executions this runner has served. It is a
// deterministic counter for tests and observability.
func (r *CapabilityToolRunner) Calls() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// Run dispatches one model-requested capability.
//
// Order is fixed and is the whole point of this seam:
//
//  1. resolve the tool name to a canonical capability (unknown ⇒ refuse)
//  2. authorize it against the Control Plane's admission vector (ungranted ⇒ refuse)
//  3. execute the real operation against the live workspace
//  4. record capability.Evidence and publish it on the domain event bus
//
// A refusal in step 1 or 2 returns a bounded textual tool result rather than an error,
// so the model can read WHY it may not proceed and continue. It never falls through to
// execution.
func (r *CapabilityToolRunner) Run(ctx context.Context, call ai.ToolCall) (string, error) {
	if r == nil {
		return "", fmt.Errorf("capability tools: nil runner")
	}
	name := call.Function.Name
	started := time.Now()

	id, known := ToolCapabilityFor(name)
	if !known {
		// Closed vocabulary: a name outside it is refused, never guessed at.
		ev := capability.Refuse("", fmt.Errorf("capability: %q is not in the read-only capability vocabulary", name))
		r.publish(id, name, ev, started)
		return fmt.Sprintf("error: capability %q is not available in this runtime", name), nil
	}

	grant := CapabilityGrantFor(r.admitted())
	if !grant.Permits(id) {
		ev := capability.Refuse(id, capability.NotAuthorized(id))
		r.publish(id, name, ev, started)
		// The class travels with the message so a reader of the trace can tell
		// "the Control Plane said no" apart from "it was attempted and failed" —
		// which is exactly what the capability taxonomy exists to prevent.
		return fmt.Sprintf("error: %s [%s]", ev.Summary, ev.Class), nil
	}

	out, err := r.execute(ctx, call)

	ev := capability.Evidence{
		ID:         fmt.Sprintf("tool.%s.%d", id, r.nextCall()),
		Capability: id,
		OK:         err == nil,
		Fields:     map[string]string{"tool": name, "request_id": call.ID},
	}
	if err != nil {
		ev.Class = capability.FailureCapabilityFailed
		ev.Summary = err.Error()
	} else {
		ev.Summary = fmt.Sprintf("%s returned %d bytes", name, len(out))
	}
	r.publish(id, name, ev, started)

	if err != nil {
		// Surface as a bounded tool result so the model can recover, matching the
		// loop's convention for every other tool failure.
		//
		// The nil error is INTENTIONAL and is not a swallowed failure: the
		// execution failure has already been classified and published as
		// capability.Evidence above (FailureCapabilityFailed), and a tool
		// result is the channel the model reads to recover. Propagating the
		// error instead would abort the whole tool loop on one bad call, which
		// is exactly what runReadOnlyTool in the loop already avoids doing.
		return fmt.Sprintf("error: %s [%s]", ev.Summary, ev.Class), nil //nolint:nilerr // classified into Evidence above
	}
	return out, nil
}

// execute performs the real operation, delegating to the existing traversal-guarded
// read-only implementation. No filesystem logic is duplicated here.
func (r *CapabilityToolRunner) execute(_ context.Context, call ai.ToolCall) (string, error) {
	switch call.Function.Name {
	case ai.ToolReadFile:
		var p ai.ReadFileParams
		if err := json.Unmarshal([]byte(call.Function.Arguments), &p); err != nil {
			return "", fmt.Errorf("%s: parse arguments: %w", ai.ToolReadFile, err)
		}
		return r.inner.readFile(call.Function.Arguments)
	case ai.ToolListDirectory:
		return r.inner.listDirectory(call.Function.Arguments)
	case ai.ToolSearchCodebase:
		return r.inner.searchCodebase(call.Function.Arguments)
	case ai.ToolSymbolLookup:
		return r.inner.symbolLookup(call.Function.Arguments)
	default:
		return "", fmt.Errorf("capability: unsupported tool %q", call.Function.Name)
	}
}

// nextCall returns a monotonic per-runner call ordinal for evidence identity.
func (r *CapabilityToolRunner) nextCall() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.calls
}

// publish records one capability execution on the EXISTING domain event model.
//
// events.NewStageCompleted and events.NewActivity are the canonical constructors; this
// adds no new event type and no parallel bus. A nil bus is a no-op so the seam stays
// usable in headless harnesses.
func (r *CapabilityToolRunner) publish(id capability.ID, name string, ev capability.Evidence, started time.Time) {
	if r == nil || r.bus == nil {
		return
	}
	verdict := "ok"
	if !ev.OK {
		verdict = string(ev.Class)
	}
	r.bus.Publish(events.NewStageCompleted(
		"capability.execute",
		time.Since(started),
		fmt.Sprintf("capability=%s tool=%s class=%s evidence=%s", id, name, verdict, ev.ID),
	))
	r.bus.Publish(events.NewActivity(fmt.Sprintf(
		"[capability] %s via %s -> %s (%s)", ev.ID, name, verdict, ev.Summary)))
}

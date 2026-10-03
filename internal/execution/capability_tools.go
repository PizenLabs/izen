package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
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

	// root is the workspace root this seam is bound to. It is retained so the
	// TARGET IDENTITY authority can answer existence/directory questions without
	// the capability operation itself performing a second, divergent lookup.
	root string

	mu    sync.Mutex
	calls int
	// failures is the per-lifecycle record of capability failures, keyed by the
	// deterministic failure key (capability + target). It is what makes a
	// REPEATED identical request detectable rather than merely observable, and
	// it is the evidence NON_PROGRESSING_EXECUTION is derived from.
	failures map[string]*failureRecord
	// scope resolves the authoritative resolved scope for a request. It is
	// injected by the executor that owns target resolution; nil means the seam
	// reports the request's own status without claiming scope membership.
	scope func() TargetScopeEvidence
}

// failureRecord counts identical capability failures within one lifecycle.
type failureRecord struct {
	class  FailureClass
	target string
	tool   string
	reason string
	seen   int
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
		root:     root,
		failures: make(map[string]*failureRecord),
	}
}

// WithTargetScope binds the authoritative resolved-scope evidence this seam judges
// capability requests against.
//
// This is what makes target identity a RUNTIME FACT at the capability boundary
// instead of an inference from a failed read. A request for `style.css` against a
// scope of `[index.html, script.js, styles.css]` is classified NOT_FOUND (or
// TARGET_IDENTITY_MISMATCH) and the scope is returned unchanged — before any
// filesystem call is made, and with no possibility of substitution.
//
// Passing nil disables scope binding: requests are then classified on their own
// evidence alone. That is the historical behaviour, kept for callers that own no
// scope.
func (r *CapabilityToolRunner) WithTargetScope(scope func() TargetScopeEvidence) {
	if r == nil {
		return
	}
	r.scope = scope
}

// ResetFailures clears the per-lifecycle failure record. A new objective
// lifecycle starts with no history: failure evidence is objective-scoped, exactly
// like the objective contract, and carrying it forward would let one objective's
// dead ends constrain another's.
func (r *CapabilityToolRunner) ResetFailures() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = make(map[string]*failureRecord)
}

// CapabilityFailures returns a snapshot of the recorded capability failures,
// ordered deterministically by failure key. It is the evidence surface a trace
// and a test read; it never authorizes anything.
func (r *CapabilityToolRunner) CapabilityFailures() []ExecutionFailure {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]string, 0, len(r.failures))
	for k := range r.failures {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]ExecutionFailure, 0, len(keys))
	for _, k := range keys {
		rec := r.failures[k]
		out = append(out, ExecutionFailure{
			Class:    rec.class,
			Target:   rec.target,
			Evidence: rec.reason,
			Count:    rec.seen,
		})
	}
	return out
}

// recordFailure notes a capability failure and reports whether it is a REPEAT of
// an identical, already-failed request within this lifecycle.
//
// A repeat is only a repeat when nothing material changed in between: the same
// tool, the same target, the same class. That is what stops
// `read_file(style.css)` from being executed a third time after the runtime has
// already observed that `style.css` does not exist.
func (r *CapabilityToolRunner) recordFailure(key, tool, target string, class FailureClass, reason string) (count int, repeated bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failures == nil {
		r.failures = make(map[string]*failureRecord)
	}
	rec, ok := r.failures[key]
	if !ok {
		r.failures[key] = &failureRecord{class: class, target: target, tool: tool, reason: reason, seen: 1}
		return 1, false
	}
	rec.seen++
	// A failure whose class changed is a genuinely different failure: the record
	// is replaced rather than counted, because class change IS the new evidence
	// that makes a continuation meaningful.
	if rec.class != class {
		rec.class, rec.reason = class, reason
		return rec.seen, false
	}
	return rec.seen, true
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

	// ── TARGET IDENTITY (hard invariant) ──────────────────────────────────
	// Every file-addressed capability request is resolved against the
	// AUTHORITATIVE evidence BEFORE the operation runs. The requested name is
	// preserved verbatim; nothing is matched approximately, and a refusal leaves
	// the scope unchanged and performs no filesystem access.
	//
	// This is the check whose absence produced the live failure: `read_file
	// style.css` against a resolved scope of `[index.html, script.js,
	// styles.css]` failed as a generic capability error and was retried.
	//
	// READABILITY is the bar here, not mutation authority. This seam is read-only
	// by construction, so an out-of-scope file the workspace actually holds is
	// legitimate context. What is refused is a target that does not exist —
	// which is precisely the request that was being retried.
	if target := r.requestedTarget(call); target != "" {
		verdict := r.resolveRequest(target)
		if !verdict.Readable() {
			return r.refuseTarget(name, id, target, verdict, started), nil
		}
	}

	out, err := r.execute(ctx, call)

	// ── STRUCTURED FAILURE CLASSIFICATION ────────────────────────────────
	// The class is derived from the error's IDENTITY, not from a fixed default.
	// A missing file, an unauthorized call and a broken capability are three
	// different facts with three different recovery policies; collapsing them
	// into CAPABILITY_FAILED is what made a nonexistent target look like a
	// transport blip worth an identical retry.
	fail := classifyCapabilityError(err)
	ev := capability.Evidence{
		ID:         fmt.Sprintf("tool.%s.%d", id, r.nextCall()),
		Capability: id,
		OK:         err == nil,
		Fields:     map[string]string{"tool": name, "request_id": call.ID},
	}
	if err != nil {
		ev.Class = capabilityClassFor(fail.Class)
		ev.Summary = err.Error()
		ev.Fields["failure_class"] = fail.Class.String()
		if fail.Target != "" {
			ev.Fields["target"] = fail.Target
		}
		// Record the failure in the per-lifecycle ledger. A REPEAT of an
		// identical, already-observed failure is classified
		// NON_PROGRESSING_EXECUTION: the runtime now knows the answer, so
		// executing the same request again cannot change it.
		count, repeated := r.recordFailure(failureKey(name, fail.Target), name, fail.Target, fail.Class, fail.Evidence)
		ev.Fields["failure_count"] = fmt.Sprintf("%d", count)
		if repeated && count >= 2 {
			ev.Fields["failure_class"] = FailureNonProgressing.String()
			ev.Class = capability.FailureNoProgress
			ev.Summary = fmt.Sprintf(
				"error: NON_PROGRESSING_EXECUTION [%s] was already observed %d time(s) in this objective with no new evidence; "+
					"replan against the workspace instead of repeating it [%s]",
				fail.Target, count, fail.Evidence)
		}
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
		// capability.Evidence above, and a tool result is the channel the model
		// reads to recover. Propagating the error instead would abort the whole
		// tool loop on one bad call, which is exactly what runReadOnlyTool in
		// the loop already avoids doing.
		return fmt.Sprintf("error: %s [%s]", ev.Summary, ev.Class), nil //nolint:nilerr // classified into Evidence above
	}
	return out, nil
}

// requestedTarget extracts the file-addressed target of a tool call, verbatim.
//
// It returns "" for a tool that names no single file (a directory listing or a
// workspace-wide search) — those are not target-identity requests and are judged
// by their own evidence.
func (r *CapabilityToolRunner) requestedTarget(call ai.ToolCall) string {
	switch call.Function.Name {
	case ai.ToolReadFile:
		var p ai.ReadFileParams
		if err := json.Unmarshal([]byte(call.Function.Arguments), &p); err != nil {
			return ""
		}
		return strings.TrimSpace(p.Path)
	default:
		return ""
	}
}

// resolveRequest classifies one requested target against the authoritative
// evidence this seam is bound to.
func (r *CapabilityToolRunner) resolveRequest(target string) TargetRequest {
	ev := TargetScopeEvidence{}
	if r.scope != nil {
		ev = r.scope()
	}
	// Existence and directory-ness come from the workspace this runner is
	// already bound to. They are OBSERVATION, never authority: they decide
	// NOT_FOUND vs OBSERVED, and only an exact scope membership authorizes.
	if ev.Exists == nil {
		ev.Exists = r.exists
	}
	if ev.IsDir == nil {
		ev.IsDir = r.isDir
	}
	return ResolveTargetRequest(target, ev)
}

// exists reports whether a workspace-relative path is present.
func (r *CapabilityToolRunner) exists(rel string) bool {
	abs, err := r.inner.resolveWithinRoot(rel)
	if err != nil {
		return false
	}
	_, err = os.Stat(abs)
	return err == nil
}

// isDir reports whether a workspace-relative path is a directory.
func (r *CapabilityToolRunner) isDir(rel string) bool {
	abs, err := r.inner.resolveWithinRoot(rel)
	if err != nil {
		return false
	}
	info, err := os.Stat(abs)
	return err == nil && info.IsDir()
}

// refuseTarget records and renders a target-identity refusal.
//
// It performs NO filesystem access, substitutes NOTHING, and mutates NOTHING.
// The refusal text names the request verbatim and the unchanged resolved scope,
// so a human reading the trace can act on it — and the model can read WHY it
// may not proceed and choose a different target itself.
func (r *CapabilityToolRunner) refuseTarget(name string, id capability.ID, target string, verdict TargetRequest, started time.Time) string {
	structured := NewTargetRefusal(verdict, nil)
	class := FailureTargetNotFound
	if structured != nil {
		if typed, ok := structured.(interface{ Class() FailureClass }); ok {
			class = typed.Class()
		}
	}
	key := failureKey(name, target)
	count, repeated := r.recordFailure(key, name, target, class, verdict.Reason)

	ev := capability.Evidence{
		ID:         fmt.Sprintf("tool.%s.%d", id, r.nextCall()),
		Capability: id,
		OK:         false,
		Class:      capabilityClassFor(class),
		Summary:    verdict.Reason,
		Fields: map[string]string{
			"tool":          name,
			"target":        target,
			"target_status": verdict.Status.String(),
			"requested":     verdict.Requested,
			"failure_class": class.String(),
			"failure_count": fmt.Sprintf("%d", count),
			"scope":         strings.Join(verdict.Scope, ","),
			"substituted":   "false",
		},
	}
	// A repeated identical request is reclassified as NON_PROGRESSING. This is
	// the typed form of "the runtime already knows the answer": no amount of
	// re-issuing `style.css` will make it exist, and the scope is unchanged.
	if repeated && count >= 2 {
		ev.Fields["failure_class"] = FailureNonProgressing.String()
		ev.Class = capability.FailureNoProgress
		ev.Summary = fmt.Sprintf(
			"%s was already observed %d time(s) in this objective; the request is unchanged and so is the evidence", verdict.Reason, count)
	}
	r.publish(id, name, ev, started)
	return fmt.Sprintf("error: %s [%s] — resolved scope is [%s] and is unchanged; use a target from that scope",
		ev.Summary, ev.Fields["failure_class"], strings.Join(verdict.Scope, ","))
}

// failureKey is the deterministic identity of a failed capability request. Two
// requests with the same key are the same request, which is what makes a repeat
// detectable without any fuzzy comparison.
func failureKey(tool, target string) string { return tool + "\x00" + target }

// classifyCapabilityError derives the structured failure record from an
// execution error's IDENTITY.
//
// It checks typed sentinels and the filesystem error type rather than matching a
// message, so a reworded message cannot change a recovery decision. An
// unrecognised error degrades to CAPABILITY_FAILURE — the honest default,
// because "we do not know which kind of failure this was" is itself the fact.
func classifyCapabilityError(err error) ExecutionFailure {
	if err == nil {
		return ExecutionFailure{}
	}
	out := ExecutionFailure{Evidence: err.Error()}
	switch {
	case errors.Is(err, ErrTargetNotFound):
		out.Class = FailureTargetNotFound
	case errors.Is(err, ErrTargetIdentityMismatch):
		out.Class = FailureTargetIdentityMismatch
	case errors.Is(err, ErrNonProgressingExecution):
		out.Class = FailureNonProgressing
	case capability.IsAuthorizationBlocked(err):
		out.Class = FailureAuthorizationRequired
	case errors.Is(err, os.ErrNotExist):
		// The filesystem said the path is not there. That is a target-identity
		// fact, not a broken capability — and it is precisely the case that used
		// to be retried as if it were a transport blip.
		out.Class = FailureTargetNotFound
	case errors.Is(err, os.ErrPermission):
		out.Class = FailureAuthorizationRequired
	default:
		out.Class = FailureCapabilityFailure
	}
	// Preserve the target the failure concerns when the error carries one.
	var tf *TargetNotFoundError
	if errors.As(err, &tf) {
		out.Target = tf.Request.Requested
		if out.Class == FailureNone {
			out.Class = tf.Class()
		}
	}
	return out
}

// capabilityClassFor projects a structured failure class onto the capability
// taxonomy's existing members. It introduces no new capability member: a caller
// reading a capability log sees the vocabulary it already knows, and the finer
// distinction is carried in the `failure_class` field.
func capabilityClassFor(c FailureClass) capability.FailureClass {
	switch c {
	case FailureAuthorizationRequired, FailureTargetAmbiguous:
		return capability.FailureAuthorizationBlocked
	case FailureTargetNotFound, FailureTargetIdentityMismatch:
		return capability.FailureTargetUncertain
	case FailureNonProgressing:
		return capability.FailureNoProgress
	case FailureOutputExhausted:
		return capability.FailureContextInsufficient
	default:
		return capability.FailureCapabilityFailed
	}
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

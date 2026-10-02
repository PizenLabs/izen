package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/core/domain"
	domaincap "github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/domain/ports"
	"github.com/PizenLabs/izen/internal/execution/capability"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── Behavioral runtime observation (the missing execution half) ─────────────
//
// IZEN could already READ a workspace, MUTATE it, and VERIFY a mutation with a
// language toolchain. What it could not do was ask the question the user's
// objective actually poses: does the thing RUN, and does it behave correctly?
//
// The gap was structural rather than cosmetic:
//
//   - Nothing ever started a long-running process. Every exec site in the tree
//     ran to completion under cmd.Run(), so no runtime could be probed while it
//     was alive.
//   - No reachable URL could be discovered, because nothing served one.
//   - No HTTP observation existed outside provider-catalog probing, so a
//     runtime's actual response was never evidence.
//   - Consequently every defect that only manifests at runtime was invisible,
//     and "the model returned text" was the only terminal signal available.
//
// This file closes that gap WITHOUT adding a new authority. BehavioralRuntime
// observes; it never writes to the workspace. Every mutation it could need goes
// through the caller's existing mutation path, under the caller's own
// authorization. The one side effect it does own is a listener it binds itself
// and releases itself — which is precisely what makes the URL discoverable
// rather than guessed.

// behavioralDefaultCommandTimeout bounds one command execution performed by the
// behavioral runtime. The behavioral runtime exists to observe; a command that
// never returns would turn observation into a hang.
const behavioralDefaultCommandTimeout = 60 * time.Second

// BehavioralConfig configures a behavioral runtime.
type BehavioralConfig struct {
	// Root is the workspace root. Required.
	Root string
	// ServeDir is the workspace-relative directory to serve. Empty serves the
	// workspace root.
	ServeDir string
	// CommandDir is the workspace-relative directory commands run in. Empty
	// runs them at the workspace root.
	CommandDir string
	// CommandTimeout bounds a single authorized command. Zero uses
	// behavioralDefaultCommandTimeout.
	CommandTimeout time.Duration
}

// BehavioralRuntime observes a workspace's real runtime behaviour.
//
// It owns at most ONE listener at a time and releases it on Stop. Concurrency:
// Stop is idempotent and mutex-guarded, because a leaked listener would outlive
// the run that created it.
type BehavioralRuntime struct {
	root string

	mu         sync.Mutex
	served     *capability.ServerHandle
	baseURL    string
	entryPath  string
	serveDir   string
	commandDir string
	shell      ports.ShellPort
	timeout    time.Duration
	closed     bool
}

// NewBehavioralRuntime builds a runtime over one workspace.
//
// The shell port is OPTIONAL. A runtime with no shell port still discovers,
// serves, fetches and inspects, and reports command execution as
// CAPABILITY_MISSING rather than silently skipping it — a missing capability is
// reported, never faked.
func NewBehavioralRuntime(cfg BehavioralConfig) *BehavioralRuntime {
	timeout := cfg.CommandTimeout
	if timeout <= 0 {
		timeout = behavioralDefaultCommandTimeout
	}
	root := cfg.Root
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &BehavioralRuntime{
		root:       root,
		serveDir:   cfg.ServeDir,
		commandDir: cfg.CommandDir,
		timeout:    timeout,
	}
}

// SetShellPort installs the authorized shell port. It MUST be the same port the
// rest of the runtime executes through, so a behavioral command passes the same
// authorization and sandbox gates as every other command in IZEN.
func (b *BehavioralRuntime) SetShellPort(p ports.ShellPort) {
	if b == nil || p == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.shell = p
}

// Root returns the absolute workspace root this runtime observes.
func (b *BehavioralRuntime) Root() string {
	if b == nil {
		return ""
	}
	return b.root
}

// URL returns the URL of the currently served runtime, or "" when nothing is
// being served. It is DISCOVERED from the bound listener, never constructed from
// an assumed port.
func (b *BehavioralRuntime) URL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.baseURL
}

// EntryPath returns the served entry document path, or "" when nothing is
// served.
func (b *BehavioralRuntime) EntryPath() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.entryPath
}

// Served reports whether a runtime is currently being served.
func (b *BehavioralRuntime) Served() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.served != nil
}

// serveDir returns the configured serve directory.
func (b *BehavioralRuntime) configuredServeDir() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.serveDir
}

// entryRelPath converts a served document name into the workspace-relative path
// the mutation authority acts on.
//
// The served root may be a subdirectory of the workspace, so a served name is
// relative to the serve root and must be re-anchored to the workspace before it
// can be used as a mutation target.
func entryRelPath(entry, serveDir string) string {
	serveDir = strings.Trim(strings.TrimSpace(serveDir), "/")
	if serveDir == "" || serveDir == "." {
		return entry
	}
	return path.Join(serveDir, entry)
}

// runner builds the capability substrate runner bound to this workspace under
// the given grant, with the authorized shell port injected as the command seam.
func (b *BehavioralRuntime) runner(grant capability.Grant) *capability.Runner {
	b.mu.Lock()
	shell := b.shell
	dir := b.commandDir
	timeout := b.timeout
	b.mu.Unlock()

	r := capability.NewRunner(b.root, capability.WithCommandRunner(shellCommandRunner{
		port:    shell,
		dir:     dir,
		root:    b.root,
		timeout: timeout,
	}))
	r.SetGrant(grant)
	return r
}

// shellCommandRunner adapts the authorized shell port onto the capability
// substrate's command seam. Every command the behavioral runtime issues is
// therefore authorized exactly like every other command in IZEN — the
// behavioral runtime never spawns a process itself.
type shellCommandRunner struct {
	port    ports.ShellPort
	dir     string
	root    string
	timeout time.Duration
}

// RunCommand implements capability.CommandRunner.
func (s shellCommandRunner) RunCommand(ctx context.Context, command string) (capability.CommandResult, error) {
	if s.port == nil {
		return capability.CommandResult{}, errors.New("no authorized shell port is bound")
	}
	runCtx := ctx
	if s.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	dir := s.dir
	if strings.TrimSpace(dir) == "" {
		dir = "."
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.root, filepath.FromSlash(dir))
	}
	out, err := s.port.ExecuteIn(runCtx, dir, command)
	if err != nil {
		return capability.CommandResult{}, err
	}
	return capability.CommandResult{
		Command:  command,
		Stdout:   out.Stdout,
		Stderr:   out.Stderr,
		ExitCode: out.ExitCode,
	}, nil
}

// Discovery is one bounded workspace-discovery observation.
type Discovery struct {
	// Profile is the structured evidence the scan produced.
	Profile capability.Profile
	// Evidence is the evidence record for the scan itself.
	Evidence capability.Evidence
	// Block is the truthful stop when discovery could not run.
	Block *capability.Block
}

// Discover enumerates the workspace and derives its shape from what is on disk.
//
// Discovery is EVIDENCE. Nothing in the returned profile authorizes a mutation:
// the only path from a profile to a target is the existing target resolver plus
// the existing authorization gate.
func (b *BehavioralRuntime) Discover(ctx context.Context, grant capability.Grant) Discovery {
	r := b.runner(grant)
	profile, ev, err := r.Discover(ctx)
	if err != nil {
		return Discovery{
			Evidence: ev,
			Block:    b.classify(err, ev, capability.WorkspaceDiscover),
		}
	}
	return Discovery{Profile: profile, Evidence: ev}
}

// Observation is one complete behavioral observation pass over a workspace's
// real runtime, together with the evidence it rests on.
type Observation struct {
	// Result is the capability substrate's structured observation.
	Result capability.Observation
	// BaseURL is the runtime that was observed ("" when nothing was served).
	BaseURL string
	// EntryPath is the served document that was inspected.
	EntryPath string
	// Serve is the evidence proving a runtime was started and became reachable.
	Serve capability.Evidence
	// Stop is the evidence proving the runtime was released.
	Stop capability.Evidence
	// Block is the truthful stop when observation could not run at all.
	Block *capability.Block
	// Proof retains every evidence record the pass produced, in deterministic
	// order, so a PROVEN claim can always be walked back to raw observations.
	Proof []capability.Evidence
}

// Verified reports whether the observed requirements all held.
//
// A PASS requires a positive observation. NOT_APPLICABLE and BLOCKED are both
// non-verified: "I did not look" and "I could not look" are not success.
func (o Observation) Verified() bool {
	return o.Block == nil && o.Result.Verdict == capability.VerdictPass && len(o.Result.Defects) == 0
}

// Defects returns the evidence-backed requirement violations.
func (o Observation) Defects() []capability.Defect {
	if o.Result.Defects == nil {
		return nil
	}
	return o.Result.Defects
}

// DefectLine renders the bounded defect summary of the observation.
func (o Observation) DefectLine() string { return o.Result.DefectLine() }

// DefectCount reports how many observable requirements are unmet.
func (o Observation) DefectCount() int { return len(o.Result.Defects) }

// EvidenceLine renders the bounded evidence log for a prompt or a reason.
func (o Observation) EvidenceLine() string {
	if len(o.Proof) == 0 {
		return "no evidence collected"
	}
	parts := make([]string, 0, len(o.Proof))
	for _, ev := range o.Proof {
		parts = append(parts, ev.String())
	}
	return strings.Join(parts, " | ")
}

// Observe performs the real behavioral observation sequence over one workspace:
//
//	DISCOVER → SERVE → READINESS → FETCH → INSPECT → PROBE SUBRESOURCES → STOP
//
// Each step's evidence is retained. If the runtime cannot be served or cannot
// be reached, the observation is BLOCKED with the class that says exactly which
// step failed — never a PASS, and never a defect list fabricated from a failed
// probe.
func (b *BehavioralRuntime) Observe(ctx context.Context, grant capability.Grant) Observation {
	out := Observation{}
	r := b.runner(grant)

	profile, discoverEv, err := r.Discover(ctx)
	out.Proof = append(out.Proof, discoverEv)
	if err != nil {
		out.Block = b.classify(err, discoverEv, capability.WorkspaceDiscover)
		return out
	}

	serveRoot := strings.TrimSpace(b.serveDir)
	var handle *capability.ServerHandle
	var serveEv capability.Evidence
	if serveRoot == "" {
		handle, serveEv, err = r.Serve(ctx, profile)
	} else {
		handle, serveEv, err = r.ServeDir(ctx, serveRoot)
	}
	out.Proof = append(out.Proof, serveEv)
	if err != nil {
		out.Block = b.classify(err, serveEv, capability.RuntimeServe)
		return out
	}

	b.mu.Lock()
	previous := b.served
	b.served = handle
	b.baseURL = handle.BaseURL
	b.entryPath = "/" + handle.Entry
	b.mu.Unlock()
	if previous != nil {
		// Starting a second runtime without releasing the first would leave an
		// untracked listener. Release the old one explicitly rather than
		// orphaning it, under the caller's context so a cancelled pass unwinds.
		_ = previous.ShutdownContext(ctx)
	}

	out.BaseURL = handle.BaseURL
	out.EntryPath = "/" + handle.Entry
	out.Serve = serveEv

	obs, inspectErr := r.Inspect(ctx, handle.BaseURL, "/"+handle.Entry)
	out.Result = obs
	out.Proof = append(out.Proof, obs.Evidence...)
	// Translate the observation's URL-path attribution into a WORKSPACE-RELATIVE
	// one. A repair target is a path the mutation authority can act on; "/index.html"
	// is a URL, and treating it as a path is how a repair ends up writing to a
	// file named "/index.html" or nowhere at all.
	if obs.Block == nil && inspectErr == nil && len(obs.Defects) > 0 {
		rel := entryRelPath(handle.Entry, b.configuredServeDir())
		for i := range obs.Defects {
			if obs.Defects[i].Entry != "" {
				obs.Defects[i].Entry = rel
			}
		}
	}
	if inspectErr != nil {
		// A failed inspection is still an observation failure with evidence; the
		// block names the observation step, not a defect the runtime invented.
		if obs.Block != nil {
			out.Block = obs.Block
		} else {
			evidenceIDs := []string{serveEv.ID}
			for _, ev := range obs.Evidence {
				if ev.ID != "" {
					evidenceIDs = append(evidenceIDs, ev.ID)
				}
			}
			out.Block = &capability.Block{
				Class:      capability.FailureObservationFailed,
				Capability: capability.RuntimeInspect,
				Reason:     inspectErr.Error(),
				Evidence:   evidenceIDs,
			}
		}
	}

	// The runtime is released whether or not the inspection succeeded. A held
	// listener would keep the workspace's port bound for the rest of the run and
	// make the NEXT observation a lie.
	stopEv, stopErr := r.Stop(ctx)
	out.Proof = append(out.Proof, stopEv)
	if stopEv.OK {
		out.Stop = stopEv
	}
	b.mu.Lock()
	b.served = nil
	b.baseURL = ""
	b.entryPath = ""
	b.mu.Unlock()
	if stopErr != nil && out.Block == nil {
		out.Block = b.classify(stopErr, stopEv, capability.RuntimeServe)
	}
	return out
}

// RunCommand executes one authorized command through the runtime's shell port
// and reports its real stdout, stderr and exit code.
func (b *BehavioralRuntime) RunCommand(ctx context.Context, grant capability.Grant, command string) (capability.CommandResult, capability.Evidence, error) {
	return b.runner(grant).Command(ctx, capability.CommandRequest{Command: command})
}

// ReadFile reads one bounded region of one workspace file under the read grant.
func (b *BehavioralRuntime) ReadFile(ctx context.Context, grant capability.Grant, target string, startLine, endLine int) (string, capability.Evidence, error) {
	return b.runner(grant).ReadFile(ctx, target, startLine, endLine)
}

// Search locates literal references across workspace text files under the read
// grant.
func (b *BehavioralRuntime) Search(ctx context.Context, grant capability.Grant, query, dir string, maxResults int) ([]capability.SearchMatch, capability.Evidence, error) {
	return b.runner(grant).Search(ctx, query, dir, maxResults)
}

// Stop releases the tracked listener. It is idempotent, so a teardown path can
// call it unconditionally without leaking a socket on the second call.
//
// Stop is for TEARDOWN, where the caller has no context. A caller mid-pass must
// use StopContext so a cancelled pass unwinds promptly.
func (b *BehavioralRuntime) Stop() error {
	return b.StopContext(context.Background())
}

// StopContext releases the tracked listener under the caller's context.
func (b *BehavioralRuntime) StopContext(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	handle := b.served
	b.served = nil
	b.baseURL = ""
	b.entryPath = ""
	b.closed = true
	b.mu.Unlock()
	if handle == nil {
		return nil
	}
	return handle.ShutdownContext(ctx)
}

// Close releases every resource the runtime owns. It is Stop plus a marker that
// the runtime may no longer be reused.
func (b *BehavioralRuntime) Close() error {
	if b == nil {
		return nil
	}
	err := b.Stop()
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return err
}

// classify converts a capability error plus its evidence into a truthful Block.
//
// The classification is derived from the evidence the capability recorded, not
// from the error text: a class that a capability already determined is trusted,
// and anything unrecognized degrades to CAPABILITY_FAILED rather than to
// success or to a generic "failed".
func (b *BehavioralRuntime) classify(err error, ev capability.Evidence, id capability.ID) *capability.Block {
	if err == nil {
		return nil
	}
	class := capability.FailureCapabilityFailed
	switch {
	case capability.IsAuthorizationBlocked(err):
		class = capability.FailureAuthorizationBlocked
	case capability.IsCapabilityMissing(err):
		class = capability.FailureCapabilityMissing
	case ev.Class != "" && ev.Class.Valid():
		class = ev.Class
	}
	block := &capability.Block{
		Class:      class,
		Reason:     err.Error(),
		Capability: id,
	}
	if ev.ID != "" {
		block.Evidence = []string{ev.ID}
	}
	if cands := ev.Field("candidates"); cands != "" {
		block.Candidates = splitNonEmpty(cands)
	}
	return block
}

func splitNonEmpty(in string) []string {
	parts := strings.Split(in, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// ScopeProvenanceLabel renders a scope provenance for evidence. It names the
// directive that authorized a run so a reader can see WHICH authority was in
// force, not merely that some authority was.
func ScopeProvenanceLabel(p domain.ScopeProvenance) string {
	switch p {
	case domain.ScopeDynamic:
		return "$prompt"
	case domain.ScopeDeclared:
		return "$hot"
	default:
		return "read_only"
	}
}

// GrantFor derives the capability authorization vector from the scope
// provenance and workspace capability set IZEN ALREADY holds.
//
// It is a PROJECTION of existing authority, never a second authority:
//
//   - the workspace capability set is the ceiling. A capability the set has not
//     granted is never granted here, whatever the provenance says;
//   - $prompt (ScopeDynamic) permits the workspace's own toolchain and the
//     workspace's own runtime, because observing the thing you were asked to
//     leave working is inside the authority the user already granted;
//   - $hot (ScopeDeclared) permits exactly what its declared set grants, with no
//     dynamic widening;
//   - anything else is read-only.
//
// $prompt therefore does NOT become arbitrary shell access or arbitrary egress:
// CommandRun still requires CapabilityExecute, and network observation is
// restricted to what the behavioral runtime itself serves.
func GrantFor(provenance domain.ScopeProvenance, caps *domaincap.CapabilitySet) capability.Grant {
	grant := capability.Grant{Provenance: ScopeProvenanceLabel(provenance)}
	// Discovery is always permitted: it is read-only, bounded, and is the input
	// every other decision needs. It grants nothing by itself.
	grant.Discover = true
	if caps == nil {
		return grant
	}
	readable := caps.CanRead()
	executable := caps.Has(domaincap.CapabilityExecute)
	switch provenance {
	case domain.ScopeDynamic:
		grant.Read = readable
		grant.Execute = executable
		// Network observation for $prompt is WORKSPACE-SCOPED, and it is gated on
		// execute authority for a concrete reason: the runtime probes what the
		// behavioral runtime itself served. A scope that may not start a process
		// cannot reach one, so granting probe without it would produce a
		// capability that can only ever report OBSERVATION_FAILED.
		grant.Network = executable
	case domain.ScopeDeclared:
		grant.Read = readable
		grant.Execute = executable
		grant.Network = executable
	default:
		grant.Read = readable
	}
	return grant
}

// ensureRootExists reports whether the behavioral root is a real directory. It
// keeps a misconfigured runtime from reporting an empty workspace.
func ensureRootExists(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("behavioral runtime root %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("behavioral runtime root %q is not a directory", root)
	}
	return nil
}

// ShellPort returns the authorized shell port the executor runs commands
// through, or nil when none is bound.
//
// It exists so a component that needs to execute commands on the workspace — the
// behavioral runtime — joins the EXISTING authorized path instead of creating a
// second one. Returning nil is a truthful "no authority", not a fallback.
func (x *RuntimeExecutor) ShellPort() ports.ShellPort {
	if x == nil || x.shellPort == nil {
		return nil
	}
	return x.shellPort
}

// SetShellPort installs the authorized shell port used by behavioral commands.
func (x *RuntimeExecutor) SetShellPort(p ports.ShellPort) {
	if x == nil {
		return
	}
	x.shellPort = p
}

// AuthorizeMutationTarget runs the executor's OWN admission check for one
// workspace mutation, and it runs it through the same AdmissionGateway the
// normal execution path uses.
//
// Delegating to admission rather than re-implementing a scope check is what
// keeps the behavioral stage inside the authority model: it cannot authorize a
// write the rest of the runtime would refuse, and it cannot refuse one the rest
// of the runtime would allow. A refusal comes back as an error carrying the
// gate's own reason.
func (x *RuntimeExecutor) AuthorizeMutationTarget(target string) error {
	if x == nil {
		return errors.New("execution: no executor is bound to authorize a mutation")
	}
	if strings.TrimSpace(target) == "" {
		return errors.New("execution: mutation authorization requires a target")
	}
	req := ExecuteRequest{
		Mode:            "behavioral_repair",
		Prompt:          "behavioral repair of " + target,
		Target:          target,
		Targets:         []string{target},
		Intent:          "mutation",
		ScopeProvenance: domain.ScopeDynamic,
	}
	decision, err := x.admission.Admit(req, x.root, strategy.ExecutionStrategyProfile{
		Strategy:       strategy.TargetedMutation,
		ModelRequired:  false,
		StrategyReason: "behavioral repair authorized against observed runtime evidence",
		Artifact:       strategy.ArtifactContract{Kind: "full_file"},
	})
	if err != nil {
		return fmt.Errorf("execution: admission refused a repair to %s: %w", target, err)
	}
	if !decision.Allowed {
		return fmt.Errorf("execution: admission refused a repair to %s: %s", target, decision.Reason)
	}
	return nil
}

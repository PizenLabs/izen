package execution

// ── EVIDENCE-BOUND TARGET RESOLUTION (Phase 16.1) ──────────────────────────
//
// This file owns ONE question and refuses every other: "is the path the caller
// stated a FILE?" It answers with os.Stat and nothing else.
//
//	I11 (Directory Is Not A File): "." or any directory path classifies as
//	UNBOUND_DIRECTORY and MUST NEVER be auto-promoted to a specific file. The
//	resolver has no heuristic that could promote it, because it performs no
//	content inspection and no workspace search at all.
//
// Scanning lives in discovery.go and produces EVIDENCE. Resolution lives here
// and produces a TARGET. The two never collapse: a discovery candidate is not a
// target, and discovery cannot authorize a mutation (I13).
//
// The pipeline used to treat "no target" as a soft condition: the strategy
// selector filled it in, a fallback filled it in, and when nothing did, the
// model was dispatched anyway and whatever it returned was written somewhere.
// Every one of those paths is a target the runtime INVENTED.
//
// So this file is a boundary, not a heuristic: a mutation objective is
// dispatched ONLY against a target the runtime can PROVE, where proof is an
// explicit path the caller stated, a prompt-named path that stat resolves, or a
// single unambiguous candidate the human selects. Anything else stops.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ── I11: the closed path-classification vocabulary ─────────────────────────

// TargetState is the deterministic classification of a target statement. It is
// a CLOSED six-value vocabulary; a call site that needs a seventh meaning must
// add it here, deliberately, rather than invent a label locally.
type TargetState string

const (
	// TargetStateUnboundDirectory: the statement names a directory (including
	// "."). A directory is not a file and is NEVER promoted to one (I11).
	TargetStateUnboundDirectory TargetState = "UNBOUND_DIRECTORY"
	// TargetStateUnboundPath: the statement is empty, malformed, escapes the
	// workspace, or is otherwise not a usable path at all.
	TargetStateUnboundPath TargetState = "UNBOUND_PATH"
	// TargetStateResolvedFile: the statement proves exactly one regular file.
	TargetStateResolvedFile TargetState = "RESOLVED_FILE"
	// TargetStateResolvedSet: the statement proves more than one regular file
	// (an explicit multi-target objective).
	TargetStateResolvedSet TargetState = "RESOLVED_SET"
	// TargetStateAmbiguous: no single target can be proven; the human must
	// disambiguate. This is a question, not a decision.
	TargetStateAmbiguous TargetState = "AMBIGUOUS"
	// TargetStateNotFound: the statement named a path that does not exist and is
	// not a well-known creation target.
	TargetStateNotFound TargetState = "NOT_FOUND"
)

// AllTargetStates returns the closed vocabulary. It exists so a test can assert
// the vocabulary is exactly six values and no call site invented a seventh.
func AllTargetStates() []TargetState {
	return []TargetState{
		TargetStateUnboundDirectory,
		TargetStateUnboundPath,
		TargetStateResolvedFile,
		TargetStateResolvedSet,
		TargetStateAmbiguous,
		TargetStateNotFound,
	}
}

// IsBound reports whether the state proves an actionable target.
func (s TargetState) IsBound() bool {
	return s == TargetStateResolvedFile || s == TargetStateResolvedSet
}

// String returns the canonical label.
func (s TargetState) String() string { return string(s) }

// TargetBindingPhase is the deterministic state a target-binding decision lands
// in. It is the single vocabulary the pipeline, the evidence record and the UI
// use to describe target progress, so no call site re-derives its own label.
type TargetBindingPhase string

const (
	// PhaseTargetBindingResolved: the target is proven and the mutation may be
	// dispatched against it.
	PhaseTargetBindingResolved TargetBindingPhase = "RESOLVED"
	// PhaseAwaitingDisambiguation: the workspace offers more than one plausible
	// target, or the caller named a directory. The pipeline HALTS here.
	PhaseAwaitingDisambiguation TargetBindingPhase = "AWAITING_DISAMBIGUATION"
	// PhaseUnsubstantiated: no target evidence exists. The objective cannot be
	// dispatched, and an artifact that has no proven target can never be bound
	// to disk.
	PhaseUnsubstantiated TargetBindingPhase = "UNSUBSTANTIATED"
)

// TargetBindingStatus is the three-valued outcome of a resolution attempt. It is
// intentionally NOT a bool: "did we find it" and "is it safe to proceed" are
// different questions, and collapsing them is how a single candidate from a
// 40,000-file tree became indistinguishable from a single candidate from a
// 3-file tree.
type TargetBindingStatus string

const (
	// BindingResolved: exactly one target is proven.
	BindingResolved TargetBindingStatus = "RESOLVED"
	// BindingUnresolved: no target could be proven. Workspace discovery ran and
	// found nothing (or found only files the prompt never referenced).
	BindingUnresolved TargetBindingStatus = "UNRESOLVED"
	// BindingAmbiguous: more than one target is plausible. The pipeline halts.
	BindingAmbiguous TargetBindingStatus = "AMBIGUOUS"
)

// TargetBinding is a PROVEN target: a path plus the SHA-256 of the bytes the
// binding was made against.
//
// The digest is not decoration. It is what makes a later artifact binding
// detectable as stale — if the file changed under the binding, the digest no
// longer matches and the mutation is refused rather than applied to a buffer
// the model never saw.
type TargetBinding struct {
	// Path is the workspace-relative, slash-separated target path.
	Path string
	// SourceSHA256 is the hex SHA-256 of the target's current bytes. Empty only
	// when the target does not exist yet (a creation bound to a template path).
	SourceSHA256 string
	// Explicit reports whether the path was stated rather than discovered.
	Explicit bool
	// Exists reports whether the target was present on disk at bind time.
	Exists bool
}

// String renders the binding for evidence records.
func (b TargetBinding) String() string {
	return fmt.Sprintf("%s (sha256=%s, explicit=%t, exists=%t)", b.Path, shortDigest(b.SourceSHA256), b.Explicit, b.Exists)
}

// TargetBindingResult is the complete, evidence-carrying outcome of one
// resolution attempt. It is what the execution proof records and what the UI
// projects, so a halted run can still explain itself.
type TargetBindingResult struct {
	// Status is the three-valued resolution verdict (compatibility surface).
	Status TargetBindingStatus `json:"status"`
	// Phase is the deterministic state the pipeline transitions to.
	Phase TargetBindingPhase `json:"phase"`
	// State is the Phase 16.1 six-value path classification. It is the
	// authoritative answer to "what IS this target statement" (I11); Status and
	// Phase are derived projections kept for the existing evidence surfaces.
	State TargetState `json:"state"`
	// Binding is non-nil only when exactly one target is proven.
	Binding *TargetBinding `json:"binding,omitempty"`
	// Paths is the proven target set (one entry for RESOLVED_FILE, N for
	// RESOLVED_SET). It is populated only when State.IsBound().
	Paths []string `json:"paths,omitempty"`
	// Evidence is the bounded discovery record. Present whenever discovery ran.
	Evidence *WorkspaceEvidence `json:"evidence,omitempty"`
	// Profile is the full structured discovery output. Like Evidence it is
	// CONTEXT, never authority (I13): it is never read to populate Paths.
	Profile *WorkspaceProfile `json:"profile,omitempty"`
	// Candidates is the disambiguation set the human is offered.
	Candidates []string `json:"candidates,omitempty"`
	// DiscoveryPerformed reports whether WorkspaceDiscovery actually ran. A
	// resolver that never scanned cannot claim to have looked.
	DiscoveryPerformed bool `json:"discovery_performed"`
	// Reason explains the verdict in one sentence, in the vocabulary of the
	// runtime rather than the vocabulary of a particular prompt.
	Reason string `json:"reason"`
}

// Dispatchable reports whether the pipeline may proceed to a provider dispatch
// for a mutation objective. Only a PROVEN binding is dispatchable.
func (r TargetBindingResult) Dispatchable() bool {
	if !r.State.IsBound() {
		return false
	}
	if r.Binding != nil && strings.TrimSpace(r.Binding.Path) != "" {
		return true
	}
	return len(provenPaths(r.Paths)) > 0
}

// provenPaths filters blanks from a proven path set.
func provenPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// TargetResolver resolves a mutation objective to exactly one proven target, or
// to a deterministic refusal. It never ranks candidates and never guesses.
//
// It performs NO workspace scanning and NO candidate searching (Phase 16.1):
// classification is pure os.Stat, and discovery is delegated to
// WorkspaceDiscovery.
type TargetResolver struct {
	root     string
	maxDepth int
	// stat and readFile are injected so the resolution logic is testable
	// without a filesystem, and so a test can assert that a mis-bound artifact
	// leaves the disk untouched.
	stat     func(string) (fs.FileInfo, error)
	readFile func(string) ([]byte, error)
}

// NewTargetResolver returns a resolver rooted at root.
func NewTargetResolver(root string) *TargetResolver {
	return &TargetResolver{
		root:     root,
		maxDepth: ResolverMaxDepth,
		stat:     os.Stat,
		readFile: os.ReadFile,
	}
}

// WithMaxDepth overrides the depth bound used when this resolver delegates to
// workspace discovery. A non-positive value is ignored: the bound is a safety
// property, and a caller cannot remove it.
func (r *TargetResolver) WithMaxDepth(d int) *TargetResolver {
	if r == nil {
		return nil
	}
	if d > 0 && d < r.maxDepth {
		r.maxDepth = d
	}
	return r
}

// Root returns the workspace root the resolver is bound to.
func (r *TargetResolver) Root() string {
	if r == nil {
		return ""
	}
	return r.root
}

// Classify is PURE path-level classification (I11). It uses os.Stat only: it
// never lists a directory, never searches for a candidate, and never inspects a
// file's content. A directory — including "." — is therefore structurally
// incapable of being promoted to a file.
func (r *TargetResolver) Classify(p string) TargetState {
	if r == nil {
		return TargetStateUnboundPath
	}
	raw := strings.TrimSpace(p)
	raw = strings.TrimPrefix(strings.Trim(raw, "`\"'"), "@")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return TargetStateUnboundPath
	}
	// A directory statement is a directory, full stop. "." is checked before
	// workspaceRelative (which rejects it) so the caller is told WHY.
	if path.Clean(filepath.ToSlash(raw)) == "." {
		return TargetStateUnboundDirectory
	}
	rel, ok := workspaceRelative(raw, r.root)
	if !ok {
		return TargetStateUnboundPath
	}
	if strings.TrimSpace(r.root) == "" || r.stat == nil {
		return TargetStateUnboundPath
	}
	abs := filepath.Join(r.root, filepath.FromSlash(rel))
	info, err := r.stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return TargetStateNotFound
		}
		return TargetStateUnboundPath
	}
	if info == nil {
		return TargetStateUnboundPath
	}
	if info.IsDir() {
		return TargetStateUnboundDirectory
	}
	if !info.Mode().IsRegular() {
		return TargetStateUnboundPath
	}
	return TargetStateResolvedFile
}

// ClassifyAll classifies a stated target set. A directory anywhere in the set
// dominates (I11); otherwise one file is RESOLVED_FILE and several are
// RESOLVED_SET.
func (r *TargetResolver) ClassifyAll(paths []string) TargetState {
	if r == nil || len(paths) == 0 {
		return TargetStateUnboundPath
	}
	files, dir, missing := 0, false, false
	for _, p := range paths {
		switch r.Classify(p) {
		case TargetStateResolvedFile:
			files++
		case TargetStateUnboundDirectory:
			dir = true
		case TargetStateNotFound:
			missing = true
		}
	}
	switch {
	case dir:
		return TargetStateUnboundDirectory
	case files > 1:
		return TargetStateResolvedSet
	case files == 1:
		return TargetStateResolvedFile
	case missing:
		return TargetStateNotFound
	default:
		return TargetStateUnboundPath
	}
}

// Resolve produces the evidence-bound target verdict for a mutation objective.
//
// explicit is the caller-declared target set (an @file scope, a resolved
// strategy target). When it carries a usable path, that path is AUTHORITATIVE:
// the resolver never overrides a stated target with a discovered one, because a
// stated target is stronger evidence than a scan result.
//
// prompt is used ONLY to locate a path the prompt literally names. It is never
// used to manufacture a path, and a prompt that names no file resolves to a
// disambiguation request over DISCOVERY EVIDENCE — never to a chosen candidate.
func (r *TargetResolver) Resolve(ctx context.Context, prompt string, explicit []string) TargetBindingResult {
	if r == nil || strings.TrimSpace(r.root) == "" {
		return TargetBindingResult{
			Status: BindingUnresolved,
			Phase:  PhaseUnsubstantiated,
			State:  TargetStateUnboundPath,
			Reason: "no workspace root is bound to the resolver; target evidence is impossible",
		}
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return TargetBindingResult{
				Status: BindingUnresolved,
				Phase:  PhaseUnsubstantiated,
				State:  TargetStateUnboundPath,
				Reason: "target resolution cancelled: " + err.Error(),
			}
		}
	}

	// ── 1. Explicit targets are authoritative ───────────────────────────
	cleaned := cleanExplicit(explicit)
	if len(cleaned) > 0 {
		return r.resolveExplicit(cleaned)
	}

	// ── 2. Paths the prompt literally names ─────────────────────────────
	if tokens := promptFileTokens(prompt); len(tokens) > 0 {
		if res, decided := r.resolveTokens(tokens); decided {
			return res
		}
	}

	// ── 3. Discovery evidence only ──────────────────────────────────────
	return r.disambiguateFromDiscovery()
}

// Discover delegates the bounded scan to WorkspaceDiscovery and projects the
// compact evidence record. The resolver holds NO scan logic of its own; this
// method exists only so an execution can surface its discovery evidence.
func (r *TargetResolver) Discover() WorkspaceEvidence {
	if r == nil {
		return WorkspaceEvidence{}
	}
	return r.discovery().Discover().Evidence()
}

// discovery returns the isolated discovery pass bound to this resolver's root
// and depth.
func (r *TargetResolver) discovery() *WorkspaceDiscovery {
	if r == nil {
		return nil
	}
	return NewWorkspaceDiscovery(r.root).WithMaxDepth(r.maxDepth)
}

// resolveExplicit classifies a caller-stated target set. A stated path that is
// a directory is refused as a directory (I11); a stated path that does not
// exist and is not a well-known creation template is a deterministic failure.
func (r *TargetResolver) resolveExplicit(stated []string) TargetBindingResult {
	var usable []string
	directory := false
	unbound := false
	for _, raw := range stated {
		rel, ok := normalizeOne(raw, r.root)
		if !ok {
			if r.Classify(raw) == TargetStateUnboundDirectory {
				directory = true
			} else {
				unbound = true
			}
			continue
		}
		switch r.Classify(rel) {
		case TargetStateResolvedFile:
			usable = append(usable, rel)
		case TargetStateUnboundDirectory:
			directory = true
		case TargetStateNotFound:
			if IsTemplateTarget(rel) {
				usable = append(usable, rel)
			}
		default:
			unbound = true
		}
	}
	if directory {
		return r.directoryVerdict("the stated target is a directory; a directory is not a file and will not be promoted to one (I11)")
	}
	if len(usable) == 0 {
		state := TargetStateNotFound
		reason := "explicit target(s) were stated but none exists; the runtime will not invent a file"
		if unbound {
			state = TargetStateUnboundPath
			reason = "explicit target(s) were stated but none is a usable workspace-relative file path"
		}
		return TargetBindingResult{
			Status: BindingUnresolved,
			Phase:  PhaseUnsubstantiated,
			State:  state,
			Reason: reason,
		}
	}
	state := TargetStateResolvedFile
	if len(usable) > 1 {
		state = TargetStateResolvedSet
	}
	bindings := make([]*TargetBinding, 0, len(usable))
	for _, p := range usable {
		bindings = append(bindings, r.bind(p, true))
	}
	if len(bindings) == 0 {
		return TargetBindingResult{
			Status: BindingUnresolved,
			Phase:  PhaseUnsubstantiated,
			State:  TargetStateUnboundPath,
			Reason: "explicit target(s) were stated but none is a readable file inside the workspace root",
		}
	}
	return TargetBindingResult{
		Status:  BindingResolved,
		Phase:   PhaseTargetBindingResolved,
		State:   state,
		Binding: bindings[0],
		Paths:   usable,
		Reason:  "target resolved from the explicit target set",
	}
}

// resolveTokens classifies paths the prompt literally named. decided is false
// when the tokens resolved to nothing on disk, in which case the caller falls
// through to discovery (the token may be a bare basename of a deeper file).
func (r *TargetResolver) resolveTokens(tokens []string) (TargetBindingResult, bool) {
	var existing []string
	directory := false
	for _, tok := range tokens {
		switch r.Classify(tok) {
		case TargetStateResolvedFile:
			existing = append(existing, path.Clean(filepath.ToSlash(tok)))
		case TargetStateUnboundDirectory:
			directory = true
		}
	}
	if directory {
		return r.directoryVerdict("the prompt names a directory; a directory is not a file and will not be promoted to one (I11)"), true
	}
	if len(existing) == 0 {
		return TargetBindingResult{}, false
	}
	// A BARE basename that also exists deeper in the tree is ambiguous: the
	// statement named a filename, not a location, and choosing the shallower hit
	// is a guess dressed as a heuristic. This is the one place resolution
	// consults discovery, and it consults it to REFUSE, never to choose.
	profile := r.discovery().Discover()
	if collisions := basenameCollisions(existing, tokens, profile.CandidatePaths()); len(collisions) > 0 {
		ev := profile.Evidence()
		return TargetBindingResult{
			Status:             BindingAmbiguous,
			Phase:              PhaseAwaitingDisambiguation,
			State:              TargetStateAmbiguous,
			Evidence:           &ev,
			Profile:            &profile,
			Candidates:         collisions,
			Reason:             fmt.Sprintf("%d workspace files match the bare filename named in the prompt; the human must name the file", len(collisions)),
			DiscoveryPerformed: true,
		}, true
	}
	state := TargetStateResolvedFile
	if len(existing) > 1 {
		state = TargetStateResolvedSet
	}
	bindings := make([]*TargetBinding, 0, len(existing))
	for _, p := range existing {
		bindings = append(bindings, r.bind(p, false))
	}
	if len(bindings) == 0 {
		return TargetBindingResult{}, false
	}
	return TargetBindingResult{
		Status:  BindingResolved,
		Phase:   PhaseTargetBindingResolved,
		State:   state,
		Binding: bindings[0],
		Paths:   existing,
		Reason:  "target resolved from a path the prompt named",
	}, true
}

// basenameCollisions returns the candidate set when a token that names a BARE
// basename (no path separator) matches more than one discovered file. A token
// that states a path is unambiguous and never collides.
func basenameCollisions(existing, tokens, candidates []string) []string {
	bare := map[string]bool{}
	for _, tok := range tokens {
		cleaned := path.Clean(filepath.ToSlash(strings.TrimSpace(tok)))
		if strings.Contains(cleaned, "/") {
			continue
		}
		bare[strings.ToLower(path.Base(cleaned))] = true
	}
	if len(bare) == 0 {
		return nil
	}
	var matches []string
	seen := map[string]bool{}
	for _, tok := range tokens {
		cleaned := path.Clean(filepath.ToSlash(strings.TrimSpace(tok)))
		if strings.Contains(cleaned, "/") {
			continue
		}
		for _, c := range candidates {
			if strings.EqualFold(path.Base(c), path.Base(cleaned)) {
				if !seen[c] {
					seen[c] = true
					matches = append(matches, c)
				}
			}
		}
	}
	if len(matches) <= 1 {
		return nil
	}
	return matches
}

// disambiguateFromDiscovery runs the bounded scan and returns an ambiguity
// request over its candidate EVIDENCE. It never selects a candidate: a scan
// result is a fact about the repository, not a statement about the objective
// (I13).
func (r *TargetResolver) disambiguateFromDiscovery() TargetBindingResult {
	profile := r.discovery().Discover()
	ev := profile.Evidence()
	res := TargetBindingResult{
		Evidence:           &ev,
		Profile:            &profile,
		DiscoveryPerformed: true,
	}
	if len(profile.Candidates) == 0 {
		res.Status = BindingUnresolved
		res.Phase = PhaseUnsubstantiated
		res.State = TargetStateNotFound
		res.Reason = "workspace discovery found no candidate file within the bounded scan; the objective has no proven target"
		return res
	}
	res.Status = BindingAmbiguous
	res.Phase = PhaseAwaitingDisambiguation
	res.State = TargetStateAmbiguous
	res.Candidates = profile.CandidatePaths()
	res.Reason = fmt.Sprintf(
		"the objective names no file; bounded workspace discovery returned %d candidate file(s) and the human must name the one intended",
		len(profile.Candidates))
	return res
}

// directoryVerdict builds the deterministic refusal for a directory statement.
// It carries the discovery evidence so a human can see what the directory
// contains without the runtime ever promoting one of those files to a target.
func (r *TargetResolver) directoryVerdict(reason string) TargetBindingResult {
	profile := r.discovery().Discover()
	ev := profile.Evidence()
	return TargetBindingResult{
		Status:             BindingAmbiguous,
		Phase:              PhaseAwaitingDisambiguation,
		State:              TargetStateUnboundDirectory,
		Evidence:           &ev,
		Profile:            &profile,
		Candidates:         profile.CandidatePaths(),
		DiscoveryPerformed: true,
		Reason:             reason,
	}
}

// bind produces the proven TargetBinding for a workspace-relative path, or nil
// when the path is not a readable regular file inside the root.
func (r *TargetResolver) bind(rel string, explicit bool) *TargetBinding {
	rel = path.Clean(strings.TrimSpace(filepath.ToSlash(rel)))
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") || rel == ".." {
		return nil
	}
	full := filepath.Join(r.root, filepath.FromSlash(rel))
	abs, err := filepath.Abs(full)
	if err != nil {
		return nil
	}
	rootAbs, err := filepath.Abs(filepath.Clean(r.root))
	if err != nil {
		return nil
	}
	// Containment check. A binding that can name a file outside the workspace
	// root is an arbitrary-write primitive, not a target.
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(os.PathSeparator)) {
		return nil
	}
	if r.stat == nil {
		return nil
	}
	info, statErr := r.stat(abs)
	if statErr != nil || info == nil || info.IsDir() {
		if statErr != nil && !os.IsNotExist(statErr) {
			return nil
		}
		// The file does not exist. A creation binding is legitimate ONLY for a
		// well-known template target, and it carries no source digest because
		// there are no source bytes to bind against.
		if !explicit || !IsTemplateTarget(rel) {
			return nil
		}
		return &TargetBinding{Path: rel, Explicit: true, Exists: false}
	}
	digest := ""
	if r.readFile != nil {
		if data, readErr := r.readFile(abs); readErr == nil {
			digest = sourceSHA256(data)
		}
	}
	return &TargetBinding{Path: rel, SourceSHA256: digest, Explicit: explicit, Exists: true}
}

// promptFileTokens extracts the FILES a prompt names, and nothing else.
//
// A token qualifies only when it is explicitly scoped ("@path"), quoted, or
// carries a file extension. Free words are excluded on purpose: "redesign the
// portfolio page" names a page, not a file, and treating "portfolio" as a
// filename is precisely the content-type heuristic this resolver exists to
// forbid.
func promptFileTokens(prompt string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(tok string) {
		tok = strings.Trim(tok, "`\"'(),;:!?[]{}<>")
		tok = strings.TrimPrefix(tok, "@")
		tok = strings.TrimSpace(tok)
		if tok == "" || tok == "." || tok == "/" {
			return
		}
		if filepath.Ext(tok) == "" && !IsTemplateTarget(path.Base(tok)) {
			return
		}
		norm := path.Clean(filepath.ToSlash(tok))
		if _, dup := seen[norm]; dup {
			return
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	for _, field := range strings.Fields(prompt) {
		trimmed := strings.Trim(field, "`\"'(),;:!?[]{}")
		if strings.HasPrefix(trimmed, "@") {
			add(trimmed)
			continue
		}
		if len(trimmed) >= 2 && strings.ContainsAny(trimmed, "\"'`") {
			add(strings.Trim(trimmed, "\"'`"))
			continue
		}
		if filepath.Ext(trimmed) != "" && !strings.ContainsAny(trimmed, "<>") {
			add(trimmed)
		}
	}
	return out
}

// normalizeOne validates one caller-stated target and returns a usable
// workspace-relative path, or reports that it is not usable.
func normalizeOne(raw, root string) (string, bool) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", false
	}
	p = strings.Trim(p, "`\"'")
	p = strings.TrimPrefix(p, "@")
	p = filepath.ToSlash(strings.TrimSpace(p))
	return workspaceRelative(p, root)
}

// workspaceRelative converts a stated path into a contained workspace-relative
// path, or reports that it is not usable.
func workspaceRelative(p, root string) (string, bool) {
	if p == "" {
		return "", false
	}
	cleaned := path.Clean(p)
	if strings.HasPrefix(cleaned, "/") {
		// An absolute path is only usable when it is inside the root, in which
		// case it becomes relative. An absolute path OUTSIDE the root is refused.
		if root == "" {
			return "", false
		}
		rel, err := filepath.Rel(filepath.Clean(root), filepath.FromSlash(cleaned))
		if err != nil {
			return "", false
		}
		rel = filepath.ToSlash(rel)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return "", false
		}
		return path.Clean(rel), true
	}
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

// cleanExplicit reports whether the caller stated any target at all, ignoring
// normalization.
func cleanExplicit(explicit []string) []string {
	var out []string
	for _, raw := range explicit {
		if strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "@")) != "" {
			out = append(out, raw)
		}
	}
	return out
}

// sourceSHA256 returns the hex SHA-256 of buf. It is the single digest
// primitive every binding in this package uses, so two digests can never be
// computed two different ways.
func sourceSHA256(buf []byte) string {
	return ArtifactSHA256(buf)
}

package capability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxProbeBody bounds how much of a response body one observation retains.
const maxProbeBody = 64 * 1024

// maxSubresources bounds how many local subresources one Inspect resolves and
// probes. Beyond the bound the observation is reported as truncated rather than
// silently clipped.
const maxSubresources = 64

// ServerHandle is a live, IZEN-owned runtime process with a real listening
// socket. It is the handle an observation can stop, so a serve/stop cycle never
// guesses at a process it did not start.
type ServerHandle struct {
	// Root is the directory served.
	Root string `json:"root"`
	// Entry is the document served for a directory request.
	Entry string `json:"entry"`
	// BaseURL is the real, bound base URL including the ephemeral port.
	BaseURL string `json:"base_url"`
	// Kind names the runtime shape ("static_document").
	Kind string `json:"kind"`
	// StartedAt is when the listener was bound.
	StartedAt time.Time `json:"started_at"`

	listener net.Listener
	server   *http.Server
	stopOnce sync.Once
	stopErr  error
}

// Address returns the host:port the handle actually bound.
func (h *ServerHandle) Address() string {
	if h == nil || h.listener == nil {
		return ""
	}
	return h.listener.Addr().String()
}

// Close terminates the served runtime with a fresh bounded context. It is
// idempotent, and it terminates the exact listener this handle owns.
//
// Close is for TEARDOWN, where no caller context exists — a run finishing, a
// test finishing, a defer unwinding. A caller that HAS a context must use
// ShutdownContext so the stop is cancellable by that context.
func (h *ServerHandle) Close() error {
	return h.ShutdownContext(context.Background())
}

// ShutdownContext terminates the served runtime under the caller's context.
//
// The two entry points exist because they answer different questions. Close
// answers "release this", and must work with no context at all. ShutdownContext
// answers "release this and stop waiting if I am cancelled", which is what a
// live runtime needs: a stop that ignores cancellation would let a cancelled
// stage block for the full shutdown window before it can unwind.
func (h *ServerHandle) ShutdownContext(ctx context.Context) error {
	if h == nil || h.server == nil {
		return nil
	}
	h.stopOnce.Do(func() {
		shutdownCtx := ctx
		cancel := func() {}
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			// Bound the graceful window only when the caller supplied none, so a
			// caller's own deadline always wins.
			shutdownCtx, cancel = context.WithTimeout(ctx, 2*time.Second)
		}
		defer cancel()
		if err := h.server.Shutdown(shutdownCtx); err != nil {
			// A graceful shutdown that times out still closes the listener; the
			// remaining error is reported but never hides that the stop ran.
			h.stopErr = h.server.Close()
		}
	})
	return h.stopErr
}

// Serve starts the workspace's discovered runnable target and waits until it
// ACTUALLY answers a request.
//
// Readiness is measured, not assumed: after binding, the capability issues a
// real HTTP request against the bound address and retries until it gets a real
// response or the readiness bound expires. A runtime that never answers is
// OBSERVATION_FAILED — the capability does not report a URL it never reached.
//
// The served target is chosen from workspace evidence (Profile.Entry), never
// from a filename convention: when the profile carries no defensible entry, the
// capability reports TARGET_UNCERTAIN with the candidates it had, instead of
// serving an assumed default document.
func (r *Runner) Serve(ctx context.Context, profile Profile) (*ServerHandle, Evidence, error) {
	ev := Evidence{ID: "runtime.serve", Capability: RuntimeServe, Fields: map[string]string{}}
	if err := r.authorize(RuntimeServe); err != nil {
		return nil, Refuse(RuntimeServe, err), err
	}
	if profile.Entry == nil {
		candidates := make([]string, 0, len(profile.EntryCandidates))
		for _, c := range profile.EntryCandidates {
			candidates = append(candidates, c.Path)
		}
		reason := "workspace discovery produced no defensible entry document"
		if len(candidates) > 0 {
			reason = "workspace discovery produced " + strconv.Itoa(len(candidates)) +
				" equally-ranked entry documents and no evidence separates them"
		}
		ev.OK, ev.Class, ev.Summary = false, FailureTargetUncertain, reason
		ev.Fields["candidates"] = strings.Join(candidates, ",")
		return nil, ev, errors.New("runtime.serve: " + reason)
	}

	root, err := r.resolveWithinRoot(path.Dir(profile.Entry.Path))
	if err != nil {
		ev.OK, ev.Class, ev.Summary = false, FailureCapabilityFailed, err.Error()
		return nil, ev, err
	}
	entryName := path.Base(profile.Entry.Path)

	handler := newStaticHandler(root, entryName, r.root)
	// A context-aware listen so the bind itself is cancellable: a runtime that
	// cannot bind must report OBSERVATION_FAILED promptly rather than after the
	// readiness bound.
	lc := net.ListenConfig{}
	ln, lnErr := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if lnErr != nil {
		reason := "cannot bind a loopback listener for the workspace runtime: " + lnErr.Error()
		ev.OK, ev.Class, ev.Summary = false, FailureCapabilityFailed, reason
		return nil, ev, errors.New(reason)
	}
	handle := &ServerHandle{
		Root:      root,
		Entry:     entryName,
		BaseURL:   "http://" + ln.Addr().String(),
		Kind:      profile.Entry.Kind,
		StartedAt: time.Now().UTC(),
		listener:  ln,
	}
	handle.server = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		// Serve returns as soon as the listener is closed; an error after a
		// deliberate stop is not a runtime failure and must not be surfaced as
		// one.
		_ = handle.server.Serve(ln)
	}()

	if err := r.waitReady(ctx, handle); err != nil {
		_ = handle.ShutdownContext(ctx)
		ev.OK, ev.Class, ev.Summary = false, FailureObservationFailed, err.Error()
		ev.Fields["base_url"] = handle.BaseURL
		return nil, ev, err
	}
	// The handle is tracked ONLY once readiness is proven, so Stop can never
	// terminate a socket that was never actually serving.
	r.mu.Lock()
	previous := r.active
	r.active = handle
	r.mu.Unlock()
	if previous != nil {
		// Starting a second runtime without stopping the first would leave an
		// untracked listener. Terminate the old one explicitly rather than
		// orphaning it.
		_ = previous.ShutdownContext(ctx)
	}

	ev.OK = true
	ev.Summary = fmt.Sprintf("served %s at %s (entry=%s, readiness confirmed)", root, handle.BaseURL, entryName)
	ev.Fields["base_url"] = handle.BaseURL
	ev.Fields["address"] = ln.Addr().String()
	ev.Fields["entry"] = entryName
	ev.Fields["kind"] = handle.Kind
	ev.Fields["workspace_root"] = root
	return handle, ev, nil
}

// waitReady issues real requests against the bound address until one answers or
// the readiness bound expires. Readiness is therefore an observation.
func (r *Runner) waitReady(ctx context.Context, h *ServerHandle) error {
	deadline := time.Now().Add(r.readinessTimeout)
	var lastErr error
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		attempt++
		reqCtx, cancel := context.WithTimeout(ctx, r.probeTimeout)
		req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodGet, h.BaseURL+"/", nil)
		if reqErr != nil {
			cancel()
			return reqErr
		}
		resp, doErr := r.http.Do(req)
		if doErr == nil {
			status := resp.StatusCode
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeBody))
			_ = resp.Body.Close()
			cancel()
			if status == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("runtime answered with HTTP %d before it was ready", status)
		} else {
			cancel()
			lastErr = doErr
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("workspace runtime did not become reachable within %s (%d attempt(s)): %w",
				r.readinessTimeout, attempt, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// ServeDir serves an explicitly named directory (a Node/Go project root)
// rather than the workspace root.
//
// The document served for a directory request is resolved from that directory's
// OWN discovered structure, by the same structural rule every other entry
// selection uses. Naming a directory never names a document.
func (r *Runner) ServeDir(ctx context.Context, dir string) (*ServerHandle, Evidence, error) {
	if err := r.authorize(RuntimeServe); err != nil {
		return nil, Refuse(RuntimeServe, err), err
	}
	abs, err := r.resolveWithinRoot(dir)
	if err != nil {
		ev := Refuse(RuntimeServe, err)
		ev.Class = FailureCapabilityFailed
		return nil, ev, err
	}
	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		reason := dir + " is not a directory that can be served"
		ev := Refuse(RuntimeServe, errors.New(reason))
		ev.Class = FailureTargetUncertain
		return nil, ev, errors.New(reason)
	}
	profile, ev, err := r.Discover(ctx)
	if err != nil {
		return nil, ev, err
	}
	// Re-derive the entry from the SUBDIRECTORY's own files so the entry is
	// still chosen by structure rather than by naming a default document.
	rel, relErr := filepath.Rel(mustAbs(r.root), abs)
	if relErr != nil {
		rel = "."
	}
	rel = filepath.ToSlash(rel)
	sub := Profile{}
	for _, f := range profile.Files {
		if rel == "." || strings.HasPrefix(f.Path, rel+"/") {
			trimmed := f
			if rel != "." {
				trimmed.Path = strings.TrimPrefix(f.Path, rel+"/")
			}
			sub.Files = append(sub.Files, trimmed)
		}
	}
	if len(sub.Files) == 0 {
		reason := dir + " contains no observable files to serve"
		ev := Refuse(RuntimeServe, errors.New(reason))
		ev.Class = FailureContextInsufficient
		return nil, ev, errors.New(reason)
	}
	r.deriveEntry(&sub, abs)
	if sub.Entry == nil {
		reason := "no entry document could be derived from " + dir + " by structure"
		ev := Refuse(RuntimeServe, errors.New(reason))
		ev.Class = FailureTargetUncertain
		ev.Fields["candidates"] = joinCandidatePaths(sub.EntryCandidates)
		return nil, ev, errors.New(reason)
	}
	sub.Entry.Path = path.Join(rel, sub.Entry.Path)
	return r.Serve(ctx, sub)
}

func joinCandidatePaths(in []EntryCandidate) string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		out = append(out, c.Path)
	}
	return strings.Join(out, ",")
}

func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// Stop terminates the tracked served runtime and reports the stop as evidence.
func (r *Runner) Stop(ctx context.Context) (Evidence, error) {
	ev := Evidence{ID: "runtime.stop", Capability: RuntimeServe, Fields: map[string]string{}}
	if err := r.authorize(RuntimeServe); err != nil {
		return Refuse(RuntimeServe, err), err
	}
	r.mu.Lock()
	handle := r.active
	r.active = nil
	r.mu.Unlock()
	if handle == nil {
		ev.OK = false
		ev.Class = FailureObservationFailed
		ev.Summary = "no served runtime is currently tracked by this capability runtime"
		return ev, errors.New(ev.Summary)
	}
	base := handle.BaseURL
	if err := handle.ShutdownContext(ctx); err != nil {
		ev.OK, ev.Class, ev.Summary = false, FailureCapabilityFailed, "stopping the served runtime failed: "+err.Error()
		ev.Fields["base_url"] = base
		return ev, errors.New(ev.Summary)
	}
	// A stop is only real once the socket refuses connections. Reporting
	// "stopped" for a process that still answers would be a fabricated success.
	if err := r.waitClosed(ctx, base); err != nil {
		ev.OK, ev.Class, ev.Summary = false, FailureObservationFailed, "served runtime still answered after stop: "+err.Error()
		ev.Fields["base_url"] = base
		return ev, errors.New(ev.Summary)
	}
	ev.OK = true
	ev.Summary = "stopped served runtime at " + base
	ev.Fields["base_url"] = base
	return ev, nil
}

// errStillAnswering reports that a stopped runtime's socket kept serving.
var errStillAnswering = errors.New("served runtime still answered after stop")

// waitClosed polls the address until it stops answering.
//
// A refused connection is the observation a stop is defined by, so a transport
// error is the SUCCESS case here and the loop exits. A response — any response,
// including a 404 — means the socket is still serving, which is a real failure
// rather than an error to be swallowed.
func (r *Runner) waitClosed(ctx context.Context, baseURL string) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		reqCtx, cancel := context.WithTimeout(ctx, time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, baseURL+"/", nil)
		if err != nil {
			cancel()
			return err
		}
		resp, doErr := r.http.Do(req)
		cancel()
		if doErr != nil {
			// A refused connection is the observation a stop is defined by.
			return nil //nolint:nilerr // refusal IS the success condition here
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		if time.Now().After(deadline) {
			return errStillAnswering
		}
		select {
		case <-ctx.Done():
			return errStillAnswering
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// FetchResult is one real HTTP response observation.
type FetchResult struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
	Body        string `json:"body,omitempty"`
	ElapsedMS   int64  `json:"elapsed_ms"`
}

// OK reports a 2xx response.
func (f FetchResult) OK() bool { return f.Status >= 200 && f.Status < 300 }

// Missing reports the response status a served resource uses to say "there is
// nothing here" — the observable form of a missing local subresource.
func (f FetchResult) Missing() bool {
	return f.Status == http.StatusNotFound || f.Status == http.StatusGone
}

// Fetch issues one HTTP request and reports the real response. A non-2xx status
// is a SUCCESSFUL observation of a bad state (OK=true), not a capability
// failure: the whole point of observing a runtime is to see that it is broken.
func (r *Runner) Fetch(ctx context.Context, url string) (FetchResult, Evidence, error) {
	ev := Evidence{ID: "runtime.fetch", Capability: RuntimeFetch, Fields: map[string]string{"url": url}}
	if err := r.authorize(RuntimeFetch); err != nil {
		return FetchResult{}, Refuse(RuntimeFetch, err), err
	}
	_ = r.authorize(RuntimeInspect)
	if err := ctx.Err(); err != nil {
		ev.OK, ev.Class, ev.Summary = false, FailureObservationFailed, err.Error()
		return FetchResult{}, ev, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, r.probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		reason := "cannot build a request for " + url + ": " + err.Error()
		ev.OK, ev.Class, ev.Summary = false, FailureCapabilityFailed, reason
		return FetchResult{}, ev, errors.New(reason)
	}
	started := time.Now()
	resp, err := r.http.Do(req)
	if err != nil {
		reason := "no response from " + url + ": " + err.Error()
		ev.OK, ev.Class, ev.Summary = false, FailureObservationFailed, reason
		ev.Detail = reason
		return FetchResult{}, ev, errors.New(reason)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
	out := FetchResult{
		URL:         url,
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Bytes:       len(body),
		Body:        string(body),
		ElapsedMS:   time.Since(started).Milliseconds(),
	}
	ev.OK = true
	ev.Summary = fmt.Sprintf("GET %s -> HTTP %d (%d bytes, %s)", url, resp.StatusCode, len(body), elapsedLabel(out.ElapsedMS))
	ev.Fields["status"] = strconv.Itoa(out.Status)
	ev.Fields["content_type"] = out.ContentType
	ev.Fields["bytes"] = strconv.Itoa(out.Bytes)
	ev.Fields["elapsed_ms"] = strconv.FormatInt(out.ElapsedMS, 10)
	ev.Detail = truncateBody(out.Body)
	return out, ev, nil
}

func elapsedLabel(ms int64) string {
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + "ms"
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', 2, 64) + "s"
}

func truncateBody(body string) string {
	const max = 512
	if len(body) <= max {
		return body
	}
	return body[:max] + "…"
}

// ── static handler ──────────────────────────────────────────────────────────

// staticHandler serves workspace files with containment enforced against the
// served root. It is deliberately IZEN's own server rather than a shell
// command: the runtime owns the listener, owns the port, can prove readiness,
// and can prove it stopped.
type staticHandler struct {
	root      string
	entryName string
	workspace string
	mimeByExt map[string]string
}

func newStaticHandler(root, entryName, workspace string) http.Handler {
	return &staticHandler{
		root:      root,
		entryName: entryName,
		workspace: workspace,
		mimeByExt: map[string]string{
			".html":  "text/html; charset=utf-8",
			".htm":   "text/html; charset=utf-8",
			".css":   "text/css; charset=utf-8",
			".js":    "text/javascript; charset=utf-8",
			".mjs":   "text/javascript; charset=utf-8",
			".json":  "application/json",
			".svg":   "image/svg+xml",
			".png":   "image/png",
			".jpg":   "image/jpeg",
			".jpeg":  "image/jpeg",
			".gif":   "image/gif",
			".webp":  "image/webp",
			".ico":   "image/x-icon",
			".txt":   "text/plain; charset=utf-8",
			".md":    "text/markdown; charset=utf-8",
			".xml":   "application/xml",
			".woff":  "font/woff",
			".woff2": "font/woff2",
		},
	}
}

func (h *staticHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	upath := req.URL.Path
	if upath == "/" || upath == "" {
		upath = "/" + h.entryName
	}
	rel := cleanRef(strings.TrimPrefix(upath, "/"))
	if rel == "." || rel == "/" || strings.Contains(rel, "..") {
		http.NotFound(w, req)
		return
	}
	abs := filepath.Join(h.root, filepath.FromSlash(rel))
	abs = filepath.Clean(abs)
	// Containment: the served path must remain inside the served root. The
	// comparison is made in RESOLVED space on both sides — resolving only the
	// request while comparing against an unresolved root would reject every
	// file whenever the workspace itself sits behind a symlink (a temp dir on
	// macOS, a symlinked checkout), which is a false 404 rather than a defence.
	realRoot := h.root
	if real, err := filepath.EvalSymlinks(h.root); err == nil {
		realRoot = real
	}
	resolved := abs
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		resolved = real
	}
	relToRoot, err := filepath.Rel(realRoot, resolved)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(os.PathSeparator)) {
		http.NotFound(w, req)
		return
	}
	info, statErr := os.Stat(abs)
	if statErr != nil || info.IsDir() {
		http.NotFound(w, req)
		return
	}
	ext := strings.ToLower(filepath.Ext(abs))
	ctype, ok := h.mimeByExt[ext]
	if !ok {
		if guessed := mime.TypeByExtension(ext); guessed != "" {
			ctype = guessed
		} else {
			ctype = "application/octet-stream"
		}
	}
	f, openErr := os.Open(abs)
	if openErr != nil {
		http.NotFound(w, req)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

// ResourceProbe reports what happened for ONE referenced resource, whether it
// was checked over HTTP or, for a cross-origin reference, in the workspace.
//
// The workspace check is what turns "this page requests something that is not
// there" into actionable, evidence-backed candidates: it asks the filesystem
// whether ANY file matches the referenced name, so a diagnosis can propose a
// real destination instead of guessing one from a filename convention.
type ResourceProbe struct {
	Ref         string   `json:"ref"`
	URL         string   `json:"url"`
	Local       bool     `json:"local"`
	Status      int      `json:"status"`
	ContentType string   `json:"content_type"`
	Bytes       int      `json:"bytes"`
	Served      bool     `json:"served"`
	Exists      bool     `json:"exists"`
	Candidates  []string `json:"candidates,omitempty"`
	Detail      string   `json:"detail,omitempty"`
}

// Defect is one evidence-backed observation that the runtime does not satisfy
// an observable requirement. A Defect never carries a repair: it carries the
// evidence and the class, and the loop decides what to do about it.
type Defect struct {
	// Code is the stable machine code of the violated requirement.
	Code string `json:"code"`
	// Class is the truthful failure class this defect belongs to.
	Class FailureClass `json:"class"`
	// Summary is the one-line description of what was observed.
	Summary string `json:"summary"`
	// Entry is the served document the defect was observed in ("" when the
	// defect is about the runtime as a whole).
	Entry string `json:"entry,omitempty"`
	// Candidates are evidence-backed workspace paths that plausibly satisfy the
	// requirement. They are CANDIDATES: nothing here authorizes a mutation, and
	// the existing target resolver plus the existing authorization gate still
	// decide whether one of them may be written.
	Candidates []string `json:"candidates,omitempty"`
	// Evidence names the Evidence IDs this defect was derived from, so a reader
	// can walk defect → observation → raw field.
	Evidence []string `json:"evidence"`
	// Detail is bounded additional context.
	Detail string `json:"detail,omitempty"`
}

// Observation is the complete result of one behavioral observation pass.
type Observation struct {
	// BaseURL is the runtime that was observed ("" when nothing was served).
	BaseURL string `json:"base_url,omitempty"`
	// Entry is the served document path.
	Entry string `json:"entry,omitempty"`
	// Verdict is the pass/fail/blocked outcome of the observed requirements.
	Verdict Verdict `json:"verdict"`
	// Resources are the per-subresource observations.
	Resources []ResourceProbe `json:"resources,omitempty"`
	// Defects are the evidence-backed requirement violations.
	Defects []Defect `json:"defects,omitempty"`
	// Evidence is the complete evidence log for the pass, in deterministic
	// order.
	Evidence []Evidence `json:"evidence,omitempty"`
	// Block is the truthful stop when nothing could be observed.
	Block *Block `json:"block,omitempty"`
	// ServedBytes is the size of the observed entry document.
	ServedBytes int `json:"served_bytes,omitempty"`
}

// Clean reports whether the observation observed every requirement holding.
func (o Observation) Clean() bool {
	return o.Verdict == VerdictPass && len(o.Defects) == 0 && o.Block == nil
}

// EvidenceLine renders the bounded evidence log for a prompt or a reason string.
func (o Observation) EvidenceLine() string {
	parts := make([]string, 0, len(o.Evidence))
	for _, ev := range o.Evidence {
		parts = append(parts, ev.String())
	}
	if len(parts) == 0 {
		return "no evidence collected"
	}
	return strings.Join(parts, " | ")
}

// DefectLine renders the bounded defect summary for a prompt or a reason string.
func (o Observation) DefectLine() string {
	if o.Block != nil {
		return o.Block.Error()
	}
	if len(o.Defects) == 0 {
		return "no defects observed"
	}
	parts := make([]string, 0, len(o.Defects))
	for _, d := range o.Defects {
		parts = append(parts, d.Code+": "+d.Summary)
	}
	return strings.Join(parts, " | ")
}

// Inspect loads a served document over HTTP, resolves every LOCAL subresource
// it references, probes each one, and audits the served structure.
//
// This is the capability that makes an objective like "leave the workspace in
// a verified working state" checkable. Everything it reports is a real HTTP
// status code or a real structural fact about the served bytes — never a
// keyword match and never an assumption about filenames.
func (r *Runner) Inspect(ctx context.Context, baseURL, entryPath string) (Observation, error) {
	obs := Observation{BaseURL: strings.TrimRight(baseURL, "/"), Verdict: VerdictNotApplicable}
	if err := r.authorize(RuntimeInspect); err != nil {
		obs.Verdict = VerdictBlocked
		obs.Block = &Block{Class: FailureAuthorizationBlocked, Capability: RuntimeInspect, Reason: NotAuthorized(RuntimeInspect).Error()}
		return obs, NotAuthorized(RuntimeInspect)
	}
	if entryPath == "" {
		entryPath = "/"
	}
	if !strings.HasPrefix(entryPath, "/") {
		entryPath = "/" + entryPath
	}
	obs.Entry = entryPath

	entry, ev, err := r.Fetch(ctx, obs.BaseURL+entryPath)
	obs.Evidence = append(obs.Evidence, ev)
	if err != nil {
		obs.Verdict = VerdictBlocked
		obs.Block = &Block{
			Class: FailureObservationFailed, Capability: RuntimeFetch, Reason: err.Error(), Evidence: []string{ev.ID},
		}
		return obs, err
	}
	if !entry.OK() {
		obs.Verdict = VerdictBlocked
		obs.Block = &Block{
			Class:      FailureObservationFailed,
			Capability: RuntimeFetch,
			Reason:     fmt.Sprintf("entry document %s answered HTTP %d", entryPath, entry.Status),
			Evidence:   []string{ev.ID},
		}
		return obs, fmt.Errorf("capability %s: entry document %s answered HTTP %d", RuntimeFetch, entryPath, entry.Status)
	}
	obs.ServedBytes = entry.Bytes

	// Structural audit of the SERVED bytes. The runtime observed is the thing
	// being judged, so the document is judged as delivered, not as it sits on
	// disk before a mutation. Attributing the defect to the served document is
	// what lets a repair target it without guessing.
	if defect := auditServedDocument(entryPath, entry.Body); defect != nil {
		defect.Entry = entryPath
		defect.Evidence = append(defect.Evidence, ev.ID)
		obs.Defects = append(obs.Defects, *defect)
	}

	refs := extractSubresourceRefs(entry.Body)
	obs.Verdict = VerdictPass
	if len(refs) > maxSubresources {
		trunc := Evidence{
			ID:         "runtime.inspect.limits",
			Capability: RuntimeInspect,
			OK:         true,
			Summary: fmt.Sprintf("resolved %d subresource reference(s); probing stopped at the %d bound",
				len(refs), maxSubresources),
			Fields: map[string]string{"references": strconv.Itoa(len(refs)), "probed": strconv.Itoa(maxSubresources)},
		}
		obs.Evidence = append(obs.Evidence, trunc)
		refs = refs[:maxSubresources]
	}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return obs, err
		}
		probe, defects := r.probeSubresource(ctx, &obs, ref)
		obs.Resources = append(obs.Resources, probe)
		obs.Defects = append(obs.Defects, defects...)
	}
	// Attribute EVERY defect to the document that was served. A subresource
	// defect is raised while probing a reference FOUND IN that document, so the
	// document is where the repair belongs — not the candidate file that could
	// satisfy the reference. Leaving Entry empty here would force a repair
	// downstream to guess, and the one guess available (the candidate) is
	// destructive: it would overwrite a healthy asset with page markup.
	for i := range obs.Defects {
		if obs.Defects[i].Entry == "" {
			obs.Defects[i].Entry = entryPath
		}
	}
	if len(obs.Defects) > 0 {
		obs.Verdict = VerdictFail
	}
	sortEvidence(obs.Evidence)
	return obs, nil
}

// probeSubresource probes one referenced resource and returns the observation
// plus any defect it proves.
func (r *Runner) probeSubresource(ctx context.Context, obs *Observation, ref string) (ResourceProbe, []Defect) {
	local := isLocalRef(ref)
	probe := ResourceProbe{Ref: ref, Local: local}
	if !local {
		// A cross-origin reference is recorded but not fetched: probing the
		// internet is not implied by a workspace-local objective, and a
		// capability that quietly widened its own reach would be a second
		// authority.
		probe.Served = true
		probe.Detail = "cross-origin reference; not probed (outside the authorized workspace surface)"
		ev := Evidence{
			ID:         "runtime.inspect.resource",
			Capability: RuntimeInspect,
			OK:         true,
			Summary:    "external reference " + ref + " recorded without probing",
			Fields:     map[string]string{"ref": ref, "local": "false"},
		}
		obs.Evidence = append(obs.Evidence, ev)
		return probe, nil
	}
	target := resolveRefPath(ref)
	probe.URL = obs.BaseURL + "/" + target
	res, ev, err := r.Fetch(ctx, probe.URL)
	obs.Evidence = append(obs.Evidence, ev)
	if err != nil {
		return probe, []Defect{{
			Code:     CodeUnreachableSubresource,
			Class:    FailureObservationFailed,
			Summary:  fmt.Sprintf("referenced resource %q could not be observed at all: %v", ref, err),
			Evidence: []string{ev.ID},
			Detail:   err.Error(),
		}}
	}
	probe.Status = res.Status
	probe.ContentType = res.ContentType
	probe.Bytes = res.Bytes
	probe.Served = res.OK()
	if res.OK() {
		return probe, nil
	}

	// The reference did not resolve. Ask the FILESYSTEM whether anything in the
	// workspace actually holds that name, so the defect can carry real
	// candidates rather than a guess. This is the whole point: the answer comes
	// from evidence about the workspace, never from a mapping of file types to
	// file names.
	exists, cands := r.nameCandidates(ctx, path.Base(target), len(obs.Resources))
	probe.Exists = exists
	probe.Candidates = cands

	if !res.Missing() {
		return probe, []Defect{{
			Code:     CodeUnreachableSubresource,
			Class:    FailureObservationFailed,
			Summary:  fmt.Sprintf("referenced resource %q answered HTTP %d from the running workspace", ref, res.Status),
			Evidence: []string{ev.ID},
			Detail:   truncateBody(res.Body),
		}}
	}
	detail := fmt.Sprintf("the running workspace answered HTTP %d for %q", res.Status, target)
	if exists {
		detail += fmt.Sprintf("; the workspace does contain %s", strings.Join(cands, ", "))
	} else {
		detail += "; no workspace file provides that resource"
	}
	return probe, []Defect{{
		Code:       CodeMissingSubresource,
		Class:      FailureExecutionFailed,
		Summary:    fmt.Sprintf("referenced resource %q is not served: %s", ref, detail),
		Evidence:   []string{ev.ID},
		Candidates: cands,
		Detail:     detail,
	}}
}

// Defect codes. They are stable, machine-readable and describe a violated
// REQUIREMENT, not a file type.
const (
	// CodeMissingSubresource: the running workspace does not serve a resource
	// the served document references.
	CodeMissingSubresource = "MISSING_SUBRESOURCE"
	// CodeUnreachableSubresource: a referenced resource could not be observed or
	// answered a non-success status.
	CodeUnreachableSubresource = "UNREACHABLE_SUBRESOURCE"
	// CodeDocumentStructureInvalid: the served document's structure is not
	// well-formed.
	CodeDocumentStructureInvalid = "DOCUMENT_STRUCTURE_INVALID"
	// CodeExternalResourceUnreachable: a cross-origin reference failed. Recorded
	// only when the objective explicitly authorizes egress beyond the workspace.
	CodeExternalResourceUnreachable = "EXTERNAL_RESOURCE_UNREACHABLE"
)

// nameCandidates reports whether the workspace holds any file whose basename
// matches the requested one, and lists them. It is a filesystem question, so
// its answer is checkable: the caller can stat every path it returns.
func (r *Runner) nameCandidates(ctx context.Context, want string, budget int) (bool, []string) {
	if want == "" {
		return false, nil
	}
	profile, _, err := r.Discover(ctx)
	if err != nil {
		return false, nil
	}
	var exact, similar []string
	for _, f := range profile.Files {
		base := path.Base(f.Path)
		if base == want {
			exact = append(exact, f.Path)
			continue
		}
		if basenameSimilar(base, want) {
			similar = append(similar, f.Path)
		}
	}
	out := sortStrings(exact)
	if len(out) == 0 {
		out = sortStrings(similar)
	}
	if len(out) > budget && budget > 0 {
		out = out[:budget]
	}
	return len(out) > 0, out
}

// basenameSimilar reports whether two file names are near-identical by token
// overlap. It is a CANDIDATE generator, never a resolver: it only ever proposes
// paths that really exist, and the caller still has to authorize any use of them.
func basenameSimilar(a, b string) bool {
	if a == "" || b == "" || a == b {
		return a == b
	}
	ta := tokenSet(a)
	tb := tokenSet(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	shared := 0
	for t := range ta {
		if tb[t] {
			shared++
		}
	}
	if shared == 0 {
		return false
	}
	// Require the shared vocabulary to dominate BOTH names, so "style.css" and
	// "styles.css" match while "index.html" and "script.js" do not.
	return float64(shared)/float64(len(ta)) >= 0.5 && float64(shared)/float64(len(tb)) >= 0.5
}

// tokenSet splits a file name into its lowercase alphanumeric tokens.
func tokenSet(name string) map[string]bool {
	out := map[string]bool{}
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			out[cur.String()] = true
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// isLocalRef reports whether a document reference points inside the workspace.
// Absolute, protocol-relative, fragment and cross-origin references are not
// local; root-relative paths are.
func isLocalRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "#") {
		return false
	}
	lowered := strings.ToLower(ref)
	for _, scheme := range []string{"http://", "https://", "//", "data:", "mailto:", "javascript:", "blob:"} {
		if strings.HasPrefix(lowered, scheme) {
			return false
		}
	}
	return true
}

// resolveRefPath turns a root-relative or document-relative reference into a
// slash path. Query and fragment are dropped; a parent segment is preserved so
// an escaping reference is detectable rather than silently normalized away.
func resolveRefPath(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		ref = ref[:i]
	}
	if strings.HasPrefix(ref, "/") {
		return cleanRef(ref)
	}
	return cleanRef(ref)
}

// CommandRequest is one authorized command execution.
type CommandRequest struct {
	Command string
	Dir     string
}

// Command executes one command through the injected, already-authorized
// CommandRunner and reports its real stdout, stderr and exit code.
//
// Without an injected runner the capability is CAPABILITY_MISSING. It never
// spawns a process itself: a second spawn path would be a second authorization
// surface, which is precisely what this package must not create.
func (r *Runner) Command(ctx context.Context, req CommandRequest) (CommandResult, Evidence, error) {
	ev := Evidence{ID: "command.run", Capability: CommandRun, Fields: map[string]string{"command": req.Command}}
	if err := r.authorize(CommandRun); err != nil {
		return CommandResult{}, Refuse(CommandRun, err), err
	}
	r.mu.Lock()
	cr := r.cmd
	r.mu.Unlock()
	if cr == nil {
		reason := "no authorized command runner is bound to this capability runtime"
		ev.OK, ev.Class, ev.Summary = false, FailureCapabilityMissing, reason
		return CommandResult{}, ev, &missingCommandError{reason: reason}
	}
	command := strings.TrimSpace(req.Command)
	if command == "" {
		reason := "command.run requires a non-empty command"
		ev.OK, ev.Class, ev.Summary = false, FailureContextInsufficient, reason
		return CommandResult{}, ev, errors.New(reason)
	}
	res, err := cr.RunCommand(ctx, command)
	if err != nil {
		reason := "command could not be started: " + err.Error()
		ev.OK, ev.Class, ev.Summary = false, FailureCapabilityFailed, reason
		ev.Fields["command"] = command
		return CommandResult{}, ev, errors.New(reason)
	}
	ev.OK = res.ExitCode == 0
	ev.Summary = fmt.Sprintf("`%s` exited %d (%d stdout bytes, %d stderr bytes)",
		command, res.ExitCode, len(res.Stdout), len(res.Stderr))
	if !ev.OK {
		ev.Class = FailureExecutionFailed
	}
	ev.Fields["exit_code"] = strconv.Itoa(res.ExitCode)
	ev.Fields["stdout_bytes"] = strconv.Itoa(len(res.Stdout))
	ev.Fields["stderr_bytes"] = strconv.Itoa(len(res.Stderr))
	combined := res.Stderr
	if combined == "" {
		combined = res.Stdout
	}
	ev.Detail = truncateBody(combined)
	return res, ev, nil
}

// missingCommandError is the typed failure for "no command runner bound".
type missingCommandError struct{ reason string }

func (e *missingCommandError) Error() string { return e.reason }

// Unwrap exposes the missing-capability sentinel.
func (e *missingCommandError) Unwrap() error { return errNotImplemented }

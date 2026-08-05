package context

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	stdctx "context"
	"io/fs"
)

// errWalkStop is the internal sentinel used to end a filesystem walk early
// once the entry cap is reached. It is never surfaced to callers.
var errWalkStop = errors.New("context: filesystem walk stopped at entry cap")

// ─── FilesystemProvider ──────────────────────────────────────────────────────

// FilesystemProvider collects the workspace file surface. It never assumes
// the workspace exists: a missing or unreadable root degrades to an empty
// chunk annotated with metadata instead of an error.
type FilesystemProvider struct {
	root       string
	maxEntries int
	skipHidden bool
}

// NewFilesystemProvider returns a provider rooted at workspace. maxEntries<=0
// means no cap; skipHidden skips dot-directories such as .git and .izen.
func NewFilesystemProvider(workspace string, maxEntries int, skipHidden bool) *FilesystemProvider {
	return &FilesystemProvider{root: workspace, maxEntries: maxEntries, skipHidden: skipHidden}
}

// Collect implements ContextProvider.
func (p *FilesystemProvider) Collect(ctx stdctx.Context) (ContextChunk, error) {
	chunk := ContextChunk{
		Provider: ProviderFilesystem,
		Meta:     map[string]string{string(MetaKeyProvider): string(ProviderFilesystem)},
	}
	if ctx.Err() != nil {
		return ContextChunk{}, ctx.Err()
	}
	info, err := os.Stat(p.root)
	if err != nil {
		chunk.Meta[string(MetaKeyEmpty)] = "true"
		chunk.Meta[string(MetaKeyError)] = fmt.Sprintf("workspace %s unavailable: %v", p.root, err)
		return chunk, nil
	}
	if !info.IsDir() {
		chunk.Meta[string(MetaKeyError)] = fmt.Sprintf("workspace %s is not a directory", p.root)
		return chunk, nil
	}

	var entries []string
	walkErr := filepath.WalkDir(p.root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil //nolint:nilerr // skip unreadable entries without failing the walk
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if path == p.root {
			return nil
		}
		if d.IsDir() {
			if p.skipHidden && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil // directories are implied by their files
		}
		rel, rerr := filepath.Rel(p.root, path)
		if rerr != nil {
			rel = path
		}
		entries = append(entries, rel)
		if p.maxEntries > 0 && len(entries) >= p.maxEntries {
			chunk.Meta["truncated"] = "true"
			return errWalkStop
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errWalkStop) {
		if ctx.Err() != nil {
			return ContextChunk{}, ctx.Err()
		}
		chunk.Meta[string(MetaKeyError)] = walkErr.Error()
		return chunk, nil
	}

	if len(entries) == 0 {
		chunk.Meta[string(MetaKeyEmpty)] = "true"
		return chunk, nil
	}
	sort.Strings(entries)
	chunk.Content = strings.Join(entries, "\n")
	return chunk, nil
}

// ─── EnvironmentProvider ─────────────────────────────────────────────────────

// EnvironmentProvider collects runtime and OS facts. Missing values (for
// example an unresolvable home directory) are simply omitted rather than
// failing the assembly.
type EnvironmentProvider struct{}

// NewEnvironmentProvider returns a fresh environment provider.
func NewEnvironmentProvider() *EnvironmentProvider { return &EnvironmentProvider{} }

// Collect implements ContextProvider.
func (p *EnvironmentProvider) Collect(ctx stdctx.Context) (ContextChunk, error) {
	if ctx.Err() != nil {
		return ContextChunk{}, ctx.Err()
	}
	var lines []string
	if cwd, err := os.Getwd(); err == nil {
		lines = append(lines, "cwd="+cwd)
	}
	lines = append(lines,
		"goos="+runtime.GOOS,
		"goarch="+runtime.GOARCH,
		"goruntime="+runtime.Version(),
		"tempdir="+os.TempDir(),
	)
	if home, err := os.UserHomeDir(); err == nil {
		lines = append(lines, "home="+home)
	}
	for _, kv := range []struct{ key, val string }{
		{"izen_model", os.Getenv("IZEN_MODEL")},
		{"izen_provider", os.Getenv("IZEN_PROVIDER")},
		{"ci", os.Getenv("CI")},
	} {
		if kv.val != "" {
			lines = append(lines, kv.key+"="+kv.val)
		}
	}
	chunk := ContextChunk{
		Provider: ProviderEnvironment,
		Meta:     map[string]string{string(MetaKeyProvider): string(ProviderEnvironment)},
		Content:  strings.Join(lines, "\n"),
	}
	return chunk, nil
}

// ─── RepositoryProvider ──────────────────────────────────────────────────────

// CmdRunner executes an external command and returns its combined output.
// It exists so the repository provider can be exercised hermetically in
// tests.
type CmdRunner func(ctx stdctx.Context, name string, args ...string) (string, error)

// defaultCmdRunner shells out to the system using os/exec.
func defaultCmdRunner(ctx stdctx.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// RepositoryProvider collects version-control facts. Git's absence — a
// missing checkout, missing binary or unreachable remote — is recorded in
// metadata and never fails the assembly.
type RepositoryProvider struct {
	root string
	run  CmdRunner
}

// NewRepositoryProvider returns a repository provider rooted at workspace.
// It uses the system git binary; pass WithCmdRunner for hermetic tests.
func NewRepositoryProvider(workspace string, opts ...RepositoryOption) *RepositoryProvider {
	p := &RepositoryProvider{root: workspace, run: defaultCmdRunner}
	for _, o := range opts {
		o(p)
	}
	return p
}

// RepositoryOption configures a RepositoryProvider.
type RepositoryOption func(*RepositoryProvider)

// WithCmdRunner overrides the command runner used to probe git.
func WithCmdRunner(run CmdRunner) RepositoryOption {
	return func(p *RepositoryProvider) {
		if run != nil {
			p.run = run
		}
	}
}

// Collect implements ContextProvider.
func (p *RepositoryProvider) Collect(ctx stdctx.Context) (ContextChunk, error) {
	chunk := ContextChunk{
		Provider: ProviderRepository,
		Meta:     map[string]string{string(MetaKeyProvider): string(ProviderRepository)},
	}
	if ctx.Err() != nil {
		return ContextChunk{}, ctx.Err()
	}
	if _, err := os.Stat(filepath.Join(p.root, ".git")); err != nil {
		chunk.Meta[string(MetaKeyEmpty)] = "true"
		chunk.Meta["git"] = "unavailable"
		return chunk, nil //nolint:nilerr // absence of a checkout degrades to metadata
	}

	var lines []string
	status, err := p.run(ctx, "git", "-C", p.root, "status", "--porcelain")
	if err != nil {
		chunk.Meta[string(MetaKeyEmpty)] = "true"
		chunk.Meta["git"] = "unavailable"
		chunk.Meta[string(MetaKeyError)] = "git status failed: " + err.Error()
		return chunk, nil //nolint:nilerr // an unusable git degrades to metadata
	}
	lines = append(lines, "status="+status)

	if branch, err := p.run(ctx, "git", "-C", p.root, "branch", "--show-current"); err == nil && branch != "" {
		lines = append(lines, "branch="+branch)
	}
	if remote, err := p.run(ctx, "git", "-C", p.root, "remote", "get-url", "origin"); err == nil && remote != "" {
		lines = append(lines, "remote="+remote)
	}
	chunk.Content = strings.Join(lines, "\n")
	return chunk, nil
}

// ─── PromptProvider ──────────────────────────────────────────────────────────

// PromptProvider carries the raw user prompt into the PlanningContext.
type PromptProvider struct {
	prompt string
}

// NewPromptProvider returns a provider that always yields the given prompt.
func NewPromptProvider(prompt string) *PromptProvider {
	return &PromptProvider{prompt: prompt}
}

// Collect implements ContextProvider.
func (p *PromptProvider) Collect(ctx stdctx.Context) (ContextChunk, error) {
	if ctx.Err() != nil {
		return ContextChunk{}, ctx.Err()
	}
	chunk := ContextChunk{
		Provider: ProviderPrompt,
		Meta:     map[string]string{string(MetaKeyProvider): string(ProviderPrompt)},
		Content:  p.prompt,
	}
	if strings.TrimSpace(p.prompt) == "" {
		chunk.Meta[string(MetaKeyEmpty)] = "true"
	}
	return chunk, nil
}

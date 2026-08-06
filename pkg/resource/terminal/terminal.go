// Package terminal implements the shell execution environment Resource
// adapter. A TerminalResource wraps a working directory, an environment and a
// shell binary, and exposes deterministic snapshot/restore of that
// configuration.
package terminal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/pkg/resource"
)

// DefaultShell is used when no shell is configured.
const DefaultShell = "/bin/sh"

// TerminalResource wraps a shell execution working directory and its
// environment variables. It is the concrete resource.Resource implementation
// for terminal targets.
type TerminalResource struct {
	dir   string
	env   []string
	shell string
}

// Compile-time assertion that TerminalResource satisfies resource.Resource.
var _ resource.Resource = (*TerminalResource)(nil)

// NewTerminalResource returns a TerminalResource bound to an execution working
// directory and an environment in KEY=VALUE form (like os.Environ). An empty
// shell defaults to DefaultShell.
func NewTerminalResource(dir string, env []string, shell string) (*TerminalResource, error) {
	if dir == "" {
		return nil, errors.New("terminal: working directory is required")
	}
	if shell == "" {
		shell = DefaultShell
	}
	return &TerminalResource{dir: dir, env: append([]string(nil), env...), shell: shell}, nil
}

// ID returns the working directory the resource wraps.
func (t *TerminalResource) ID() string { return t.dir }

// Kind returns resource.KindTerminal.
func (t *TerminalResource) Kind() resource.ResourceKind { return resource.KindTerminal }

// ValidateState checks that the working directory exists and is a directory,
// and that the configured shell binary is available.
func (t *TerminalResource) ValidateState(ctx context.Context) error {
	_ = ctx
	info, err := os.Stat(t.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("terminal: working directory %q does not exist", t.dir)
		}
		return fmt.Errorf("terminal: stat working directory %q: %w", t.dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("terminal: %q is not a directory", t.dir)
	}
	if _, err := exec.LookPath(t.shell); err != nil {
		return fmt.Errorf("terminal: shell %q is not available: %w", t.shell, err)
	}
	return nil
}

// terminalSnapshotData is the typed payload of a TerminalResource snapshot.
type terminalSnapshotData struct {
	// Dir is the execution working directory.
	Dir string
	// Env is the environment in KEY=VALUE form.
	Env []string
	// Shell is the shell binary used for execution.
	Shell string
}

// terminalSnapshot is the concrete Snapshot implementation for
// TerminalResource.
type terminalSnapshot struct {
	hash string
	data terminalSnapshotData
}

// Hash returns a stable identifier derived from the captured configuration.
func (s *terminalSnapshot) Hash() string { return s.hash }

// Data returns the typed snapshot payload.
func (s *terminalSnapshot) Data() any { return s.data }

// state returns a defensive copy of the current resource configuration.
func (t *TerminalResource) state() terminalSnapshotData {
	return terminalSnapshotData{
		Dir:   t.dir,
		Env:   append([]string(nil), t.env...),
		Shell: t.shell,
	}
}

// hashState derives a canonical string over dir, shell and the sorted
// environment so that identical configurations hash identically regardless of
// environment ordering.
func hashState(d terminalSnapshotData) string {
	env := append([]string(nil), d.Env...)
	sort.Strings(env)
	parts := append([]string{d.Dir, d.Shell}, env...)
	return strings.Join(parts, "\x00")
}

// Snapshot captures the current working directory, environment and shell
// configuration. The capture is read-only.
func (t *TerminalResource) Snapshot(ctx context.Context) (resource.Snapshot, error) {
	if err := t.ValidateState(ctx); err != nil {
		return nil, err
	}
	data := t.state()
	sum := sha256.Sum256([]byte(hashState(data)))
	return &terminalSnapshot{
		hash: hex.EncodeToString(sum[:]),
		data: data,
	}, nil
}

// Restore reverts the resource's working directory, environment and shell to
// the captured snapshot values. Only snapshots produced by a TerminalResource
// are accepted.
func (t *TerminalResource) Restore(ctx context.Context, s resource.Snapshot) error {
	_ = ctx
	if s == nil {
		return errors.New("terminal: restore requires a non-nil snapshot")
	}
	snap, ok := s.(*terminalSnapshot)
	if !ok {
		return fmt.Errorf("terminal: incompatible snapshot type %T", s)
	}
	t.dir = snap.data.Dir
	t.env = append([]string(nil), snap.data.Env...)
	t.shell = snap.data.Shell
	return nil
}

// Run executes command through the configured shell in the resource's working
// directory and environment, returning the combined output. The command is
// bound to ctx so cancellation interrupts the subprocess.
func (t *TerminalResource) Run(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, t.shell, "-c", command)
	cmd.Dir = t.dir
	cmd.Env = append([]string(nil), t.env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("terminal: command %q failed: %w", command, err)
	}
	return string(out), nil
}

package execution

import (
	"fmt"
	"sync"

	"github.com/PizenLabs/izen/internal/modes"
)

type Capability string

const (
	CapWorkspaceRead  Capability = "workspace.read"
	CapWorkspaceWrite Capability = "workspace.write"
	CapGitCommit      Capability = "git.commit"
	CapGitStatus      Capability = "git.status"
	CapGitDiff        Capability = "git.diff"

	CapBuild        Capability = "build"
	CapTest         Capability = "test"
	CapFmt          Capability = "fmt"
	CapLint         Capability = "lint"
	CapVet          Capability = "vet"
	CapVulncheck    Capability = "vulncheck"
	CapShellExecute Capability = "shell.execute"
	CapPatchApply   Capability = "patch.apply"
	CapCheckpoint   Capability = "checkpoint"

	CapFilesystemHome   Capability = "filesystem.home"
	CapFilesystemSystem Capability = "filesystem.system"
	CapNetworkExternal  Capability = "network.external"
	CapSudoExecute      Capability = "sudo.execute"
	CapCredentialRead   Capability = "credential.read"
)

var restrictedCapabilities = map[Capability]string{
	CapFilesystemHome:   "access to home directory files outside workspace",
	CapFilesystemSystem: "access to system files (/etc, /usr, etc.)",
	CapNetworkExternal:  "external network communication",
	CapSudoExecute:      "privileged command execution via sudo",
	CapCredentialRead:   "reading credentials or secrets",
}

var capabilityModeMap = map[Capability]modes.Capability{
	CapWorkspaceRead:  modes.CapRead,
	CapWorkspaceWrite: modes.CapWrite,
	CapGitCommit:      modes.CapCheckpoint,
	CapGitStatus:      modes.CapRead,
	CapGitDiff:        modes.CapRead,
	CapBuild:          modes.CapShell,
	CapTest:           modes.CapTest,
	CapFmt:            modes.CapShell,
	CapLint:           modes.CapShell,
	CapVet:            modes.CapShell,
	CapVulncheck:      modes.CapShell,
	CapShellExecute:   modes.CapShell,
	CapPatchApply:     modes.CapPatch,
	CapCheckpoint:     modes.CapCheckpoint,
}

type PolicyDecision struct {
	Allowed    bool
	Capability Capability
	Reason     string
	Restricted bool
	Unknown    bool
}

type PolicyEngine struct {
	modeCapsFn func() modes.Capability
	mu         sync.RWMutex
}

func NewPolicyEngine(modeCapsFn func() modes.Capability) *PolicyEngine {
	return &PolicyEngine{
		modeCapsFn: modeCapsFn,
	}
}

func (pe *PolicyEngine) Check(cap Capability) PolicyDecision {
	if reason, restricted := restrictedCapabilities[cap]; restricted {
		return PolicyDecision{
			Allowed:    false,
			Capability: cap,
			Reason:     fmt.Sprintf("restricted capability %q denied: %s", cap, reason),
			Restricted: true,
		}
	}

	modeCap, known := capabilityModeMap[cap]
	if !known {
		return PolicyDecision{
			Allowed:    false,
			Capability: cap,
			Reason:     fmt.Sprintf("unknown capability %q denied by default (Default Deny)", cap),
			Unknown:    true,
		}
	}

	pe.mu.RLock()
	fn := pe.modeCapsFn
	pe.mu.RUnlock()

	if fn != nil {
		currentCaps := fn()
		if currentCaps&modeCap == 0 {
			return PolicyDecision{
				Allowed:    false,
				Capability: cap,
				Reason:     fmt.Sprintf("capability %q not permitted in current mode", cap),
			}
		}
	}

	return PolicyDecision{
		Allowed:    true,
		Capability: cap,
		Reason:     fmt.Sprintf("capability %q granted", cap),
	}
}

func (pe *PolicyEngine) Must(cap Capability) error {
	dec := pe.Check(cap)
	if !dec.Allowed {
		return fmt.Errorf("policy denied: %s", dec.Reason)
	}
	return nil
}

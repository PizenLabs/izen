package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	intentdomain "github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/modes"
)

// TestShellAuth_BangDeniedWithoutCapability proves `!` is not itself
// authority: in a mode without CapShell (/ask) the grant is refused and no
// execution seam is reached.
func TestShellAuth_BangDeniedWithoutCapability(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)
	if _, err := m.authorizeShellExecution("echo hi", ShellClassExecute, "! typed"); err == nil {
		t.Fatal("shell grant minted in /ask without CapShell")
	} else if !strings.Contains(err.Error(), "no CapShell") {
		t.Fatalf("denial must name the missing capability, got: %v", err)
	}
}

// TestShellAuth_BangGrantBindsExactCommand proves the grant is
// capability- and operation-specific: it authorizes ONLY the exact command,
// grants zero file-mutation authority, and a substituted command is refused
// with zero side effects.
func TestShellAuth_BangGrantBindsExactCommand(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeBuild)
	grant, err := m.authorizeShellExecution("go test ./...", ShellClassExecute, "! typed")
	if err != nil {
		t.Fatalf("explicit human shell intent must mint a grant: %v", err)
	}
	if grant.Command != "go test ./..." {
		t.Fatalf("grant bound %q, want the exact command", grant.Command)
	}
	if grant.Class != ShellClassExecute || grant.Provenance != "! typed" || grant.Mode != "build" {
		t.Fatalf("grant lost its authorization facts: %+v", grant)
	}
	if grant.GrantedAt.IsZero() || grant.GrantedAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("grant carries no sane authorization timestamp: %+v", grant)
	}
	// Substitution between authorization and execution fails closed.
	runner := execExecutionRunner(".")
	if _, err := runner.RunGranted(context.Background(), grant, "go test ./... --exec rm"); err == nil {
		t.Fatal("substituted command executed under another command's grant — scope expansion without a new grant")
	}
	// A zero grant executes nothing.
	if _, err := runner.RunGranted(context.Background(), ShellGrant{}, "go test ./..."); err == nil {
		t.Fatal("execution without any grant succeeded")
	}
	// Empty commands and missing provenance mint nothing.
	if _, err := m.authorizeShellExecution("   ", ShellClassExecute, "! typed"); err == nil {
		t.Fatal("empty command minted a grant")
	}
	if _, err := m.authorizeShellExecution("echo hi", ShellClassExecute, ""); err == nil {
		t.Fatal("grant without a human provenance was minted")
	}
}

// TestShellAuth_FirewallIsNotAuthority proves the blacklist stays
// defense-in-depth: dangerous commands are denied even in a capable mode,
// and the denial never executes.
func TestShellAuth_FirewallIsNotAuthority(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeBuild)
	for _, cmd := range []string{"sudo rm -rf /", "chmod 777 secret", "go test ./... && sudo make install"} {
		if _, err := m.authorizeShellExecution(cmd, ShellClassExecute, "! typed"); err == nil {
			t.Fatalf("firewall-blocked command minted a grant: %q", cmd)
		}
	}
}

// TestShellAuth_TestTargetInjectionRejected proves the $test/$run seam is
// injection-proof: shell metacharacters and flag smuggling in the target fail
// closed, while plain package patterns are admitted as bounded grants.
func TestShellAuth_TestTargetInjectionRejected(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeReview)
	for _, target := range []string{
		"./...; rm -rf ~",
		"./... && cat /etc/passwd",
		"$(whoami)",
		"`id`",
		"pkg | tee out",
		"-exec /bin/sh",
		"",
		"   ",
	} {
		if _, err := m.authorizeTestExecution("go test -v", target, "$test typed"); err == nil {
			t.Fatalf("injected test target minted a grant: %q", target)
		}
	}
	for _, target := range []string{"./...", "./pkg/foo", "./internal/bar/...", "TestFoo", "TestFoo/TestBar"} {
		grant, err := m.authorizeTestExecution("go test -v", target, "$test typed")
		if err != nil {
			t.Fatalf("legitimate target %q denied: %v", target, err)
		}
		if grant.Command != "go test -v "+target {
			t.Fatalf("grant bound %q, want the exact composed command", grant.Command)
		}
	}
	if _, err := m.authorizeTestExecution("go evil", "./...", "$test typed"); err == nil {
		t.Fatal("unknown test tool minted a grant")
	}
	if _, err := m.authorizeTestExecution("go test -v", "./...", ""); err == nil {
		t.Fatal("test grant without a human provenance was minted")
	}
}

// TestShellAuth_TraceTargetValidated proves the $trace -run seam validates
// its human-controlled value and binds the fixed race-detector invocation.
func TestShellAuth_TraceTargetValidated(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeInvestigate)
	grant, err := m.authorizeTestExecution("go test -run", "TestFoo", "$trace typed")
	if err != nil {
		t.Fatalf("legitimate trace target denied: %v", err)
	}
	if grant.Command != "go test -run=TestFoo -v -race 2>&1" {
		t.Fatalf("trace grant bound %q, want the fixed race invocation", grant.Command)
	}
	if _, err := m.authorizeTestExecution("go test -run", "TestFoo; rm x", "$trace typed"); err == nil {
		t.Fatal("injected trace target minted a grant")
	}
}

// TestShellAuth_EvidenceNilSafe proves evidence recording never panics on
// unwired harnesses (nil bus, nil tree) and needs no filesystem writes.
func TestShellAuth_EvidenceNilSafe(t *testing.T) {
	m := newTestModel()
	m.bus = nil
	m.activityTree = nil
	m.recordShellEvidence(ShellGrant{Command: "go version", Class: ShellClassInspect, Mode: "investigate", Provenance: "$env typed"}, time.Millisecond, 0, nil)
	m.recordShellEvidence(ShellGrant{}, 0, -1, context.DeadlineExceeded)
}

// TestShellAuth_ScopeReplacement proves grants never accumulate at the UI
// layer: binding a new provenance REPLACES the old one (AUTH-07 UI half).
func TestShellAuth_ScopeReplacement(t *testing.T) {
	m := newTestModel()
	m.bindScopeProvenance(intentdomain.ScopeDynamic)
	if !m.sess.ScopeProvenance.AllowsMutation() {
		t.Fatal("$prompt-equivalent binding must allow mutation within its envelope")
	}
	m.bindScopeProvenance(intentdomain.ScopeNone)
	if m.sess.ScopeProvenance.AllowsMutation() {
		t.Fatal("a newer ScopeNone binding did not replace the older grant — authorization accumulated")
	}
	if m.sess.StagedScopeProvenance.AllowsMutation() {
		t.Fatal("staged provenance survived a scope reset")
	}
}

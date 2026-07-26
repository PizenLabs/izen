package execution

import (
	"errors"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
)

func NewTestBudget() *budget.MutationBudget {
	return budget.NewBudget(100, 1000, 10000, 10, 0, 50)
}

func TestExecutionDeniedError(t *testing.T) {
	err := &ExecutionDeniedError{Reason: "no auth token"}
	if err.Error() != "execution denied: no auth token" {
		t.Errorf("unexpected message: %s", err.Error())
	}
}

func TestCheckAuthorizationNil(t *testing.T) {
	err := checkAuthorization(nil)
	if err == nil {
		t.Fatal("expected error for nil auth")
	}
	var denied *ExecutionDeniedError
	if !errors.As(err, &denied) {
		t.Errorf("expected ExecutionDeniedError, got %T", err)
	}
}

func TestCheckAuthorizationExpired(t *testing.T) {
	auth := &authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(-1 * time.Hour),
	}
	err := checkAuthorization(auth)
	if err == nil {
		t.Fatal("expected error for expired auth")
	}
	var denied *ExecutionDeniedError
	if !errors.As(err, &denied) {
		t.Errorf("expected ExecutionDeniedError, got %T", err)
	}
}

func TestCheckAuthorizationSingleUseConsumed(t *testing.T) {
	auth := &authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
		SingleUse: true,
	}
	markAuthConsumed(auth)
	err := checkAuthorization(auth)
	if err == nil {
		t.Fatal("expected error for consumed single-use auth")
	}
	var denied *ExecutionDeniedError
	if !errors.As(err, &denied) {
		t.Errorf("expected ExecutionDeniedError, got %T", err)
	}
}

func TestCheckAuthorizationSingleUseNotConsumed(t *testing.T) {
	auth := &authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
		SingleUse: true,
	}
	err := checkAuthorization(auth)
	if err != nil {
		t.Errorf("expected nil for unconsumed single-use auth, got %v", err)
	}
}

func TestCheckAuthorizationValid(t *testing.T) {
	auth := &authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
		SingleUse: false,
	}
	err := checkAuthorization(auth)
	if err != nil {
		t.Errorf("expected nil for valid auth, got %v", err)
	}
}

func TestCheckAuthorizationNoExpiry(t *testing.T) {
	auth := &authorization.MutationAuthorization{
		ID: authorization.NewAuthorizationID(),
	}
	if auth.IsExpired() {
		t.Error("auth with zero ExpiresAt should not be expired")
	}
	err := checkAuthorization(auth)
	if err != nil {
		t.Errorf("expected nil for auth without expiry, got %v", err)
	}
}

func TestRunnerAuthorizationEnforcement(t *testing.T) {
	runner := NewRunner("/tmp", false, false)

	_, err := runner.Run("echo hello")
	if err == nil {
		t.Fatal("expected ExecutionDeniedError without authorization")
	}
	var denied *ExecutionDeniedError
	if !errors.As(err, &denied) {
		t.Errorf("expected ExecutionDeniedError, got %T: %v", err, err)
	}
}

func TestRunnerAuthorizationValid(t *testing.T) {
	runner := NewRunner("/tmp", false, false)
	runner.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	})
	runner.SetBudget(NewTestBudget())

	result, err := runner.Run("echo hello")
	if err != nil {
		t.Fatalf("expected no error with valid auth, got: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
}

func TestPatchManagerAuthorizationEnforcement(t *testing.T) {
	pm := NewPatchManager("/tmp")

	patch := &Patch{
		File:     "/tmp/test-auth-file.txt",
		Modified: "hello",
	}
	err := pm.Apply(patch)
	if err == nil {
		t.Fatal("expected ExecutionDeniedError without authorization")
	}
	var denied *ExecutionDeniedError
	if !errors.As(err, &denied) {
		t.Errorf("expected ExecutionDeniedError, got %T: %v", err, err)
	}
}

func TestNewRunnerSetsBudget(t *testing.T) {
	runner := NewRunner("/tmp", false, false)
	b := NewTestBudget()
	runner.SetBudget(b)
	if runner.Budget() != b {
		t.Error("Budget() should return the set budget")
	}
}

func TestNewRunnerSetsAuthorization(t *testing.T) {
	runner := NewRunner("/tmp", false, false)
	auth := &authorization.MutationAuthorization{
		ID: authorization.NewAuthorizationID(),
	}
	runner.SetAuthorization(auth)
	if runner.Authorization() != auth {
		t.Error("Authorization() should return the set auth")
	}
}

func TestEngineSetAuthorizationPropagates(t *testing.T) {
	eng := &Engine{
		Runner:  NewRunner("/tmp", false, false),
		Patches: NewPatchManager("/tmp"),
	}
	auth := &authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	eng.SetAuthorization(auth)
	if eng.Runner.Authorization() != auth {
		t.Error("Runner should receive the auth from Engine")
	}
	if eng.Patches.Authorization() != auth {
		t.Error("PatchManager should receive the auth from Engine")
	}
}

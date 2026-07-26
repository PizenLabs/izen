package execution

import (
	"fmt"
	"sync"

	"github.com/PizenLabs/izen/internal/core/authorization"
)

type ExecutionDeniedError struct {
	Reason string
}

func (e *ExecutionDeniedError) Error() string {
	return fmt.Sprintf("execution denied: %s", e.Reason)
}

var (
	consumedMu   sync.Mutex
	consumedAuth = make(map[authorization.AuthorizationID]bool)
)

func markAuthConsumed(auth *authorization.MutationAuthorization) {
	if auth == nil || !auth.SingleUse {
		return
	}
	consumedMu.Lock()
	defer consumedMu.Unlock()
	consumedAuth[auth.ID] = true
}

func isAuthConsumed(auth *authorization.MutationAuthorization) bool {
	if auth == nil || !auth.SingleUse {
		return false
	}
	consumedMu.Lock()
	defer consumedMu.Unlock()
	return consumedAuth[auth.ID]
}

func checkAuthorization(auth *authorization.MutationAuthorization) error {
	if auth == nil {
		return &ExecutionDeniedError{
			Reason: "no authorization token provided",
		}
	}
	if auth.IsExpired() {
		return &ExecutionDeniedError{
			Reason: fmt.Sprintf("authorization token %s expired at %s", auth.ID, auth.ExpiresAt.Format("15:04:05")),
		}
	}
	if isAuthConsumed(auth) {
		return &ExecutionDeniedError{
			Reason: fmt.Sprintf("authorization token %s is single-use and has already been consumed", auth.ID),
		}
	}
	return nil
}

package capability

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by the capability adapters and registry.
var (
	// errAdapterNil is returned when a nil adapter is invoked.
	errAdapterNil = errors.New("capability: nil adapter")
	// errRegistryNil is returned when a nil registry is invoked.
	errRegistryNil = errors.New("capability: nil registry")
	// ErrNoAdapter is returned by Registry.Refresh when no adapter is wired.
	ErrNoAdapter = errors.New("capability: no inspect adapter wired")
)

// errStatus wraps a non-200 provider response status.
func errStatus(code int) error {
	return fmt.Errorf("capability: provider returned status %d", code)
}

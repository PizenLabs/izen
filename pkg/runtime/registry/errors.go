package registry

import (
	"errors"
	"sort"
)

// Sentinel errors returned by the registries.
var (
	errNilStrategy       = errors.New("registry: nil strategy plugin")
	errEmptyStrategyName = errors.New("registry: empty strategy name")
	errNoProviders       = errors.New("registry: capability requires at least one provider")
	errEmptyProvider     = errors.New("registry: empty capability provider")
)

// ErrDuplicate is returned when a name or capability is registered twice.
type ErrDuplicate struct {
	Name string
}

// Error implements error.
func (e *ErrDuplicate) Error() string {
	return "registry: duplicate registration: " + e.Name
}

func sortStrings(s []string) {
	sort.Strings(s)
}

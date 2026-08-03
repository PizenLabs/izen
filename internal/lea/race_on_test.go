//go:build race

package lea

// raceEnabled reports whether the race detector is active. Performance budgets
// are validated strictly only in production builds; under -race the
// instrumentation overhead inflates every timing measurement.
const raceEnabled = true

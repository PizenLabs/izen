package command

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// CompactAction is the parsed /compact subcommand.
type CompactAction int

const (
	// CompactNow forces an immediate compaction run.
	CompactNow CompactAction = iota
	// CompactStats reports the token budget without mutating history.
	CompactStats
	// CompactAuto updates the runtime auto-compaction threshold.
	CompactAuto
)

// String returns the canonical action label.
func (a CompactAction) String() string {
	switch a {
	case CompactStats:
		return "stats"
	case CompactAuto:
		return "auto"
	default:
		return "now"
	}
}

// CompactRequest is the parsed /compact invocation.
type CompactRequest struct {
	Action       CompactAction
	Threshold    float64 // for CompactAuto: new ratio in (0, 1)
	ThresholdOff bool    // for CompactAuto: "off" disables auto compaction
	Force        bool    // for CompactNow: always true
	Raw          string
}

// ParseCompactCommand parses "/compact [now|stats|info|auto [ratio|off]]".
// Bare "/compact" and "/compact now" force immediate compaction;
// "/compact stats" (alias "info") reports the budget;
// "/compact auto <0..1|off>" updates the runtime threshold.
func ParseCompactCommand(input string) (CompactRequest, error) {
	fields := strings.Fields(strings.TrimSpace(input))
	if len(fields) == 0 {
		return CompactRequest{}, fmt.Errorf("usage: /compact [now|stats|auto <ratio|off>]")
	}
	head := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if head != "compact" {
		return CompactRequest{}, fmt.Errorf("not a compact command: %q", input)
	}
	args := fields[1:]
	if len(args) == 0 {
		return CompactRequest{Action: CompactNow, Force: true, Raw: input}, nil
	}
	switch strings.ToLower(args[0]) {
	case "now":
		if len(args) != 1 {
			return CompactRequest{}, fmt.Errorf("usage: /compact now")
		}
		return CompactRequest{Action: CompactNow, Force: true, Raw: input}, nil
	case "stats", "info":
		if len(args) != 1 {
			return CompactRequest{}, fmt.Errorf("usage: /compact stats")
		}
		return CompactRequest{Action: CompactStats, Raw: input}, nil
	case "auto":
		if len(args) != 2 {
			return CompactRequest{}, fmt.Errorf("usage: /compact auto <0.10-0.95|off>")
		}
		if strings.EqualFold(args[1], "off") {
			return CompactRequest{Action: CompactAuto, ThresholdOff: true, Raw: input}, nil
		}
		ratio, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return CompactRequest{}, fmt.Errorf("invalid threshold %q: want a ratio like 0.75 or off", args[1])
		}
		if ratio < 0.10 || ratio > 0.95 {
			return CompactRequest{}, fmt.Errorf("threshold %.2f out of range: want 0.10-0.95", ratio)
		}
		return CompactRequest{Action: CompactAuto, Threshold: ratio, Raw: input}, nil
	default:
		return CompactRequest{}, fmt.Errorf("unknown /compact subcommand %q: want now|stats|auto", args[0])
	}
}

// autoThreshold is the process-wide runtime auto-compaction threshold.
// It lives in the domain so every surface (TUI, headless, tests) shares one
// source of truth without importing UI or engine packages.
var autoThreshold = struct {
	sync.RWMutex
	ratio    float64
	disabled bool
}{ratio: 0.80}

// CompactThreshold returns the effective auto-compaction threshold ratio and
// whether auto compaction is disabled.
func CompactThreshold() (ratio float64, disabled bool) {
	autoThreshold.RLock()
	defer autoThreshold.RUnlock()
	return autoThreshold.ratio, autoThreshold.disabled
}

// SetCompactThreshold updates the runtime threshold (ratio in (0, 1)).
func SetCompactThreshold(ratio float64) error {
	if ratio < 0.10 || ratio > 0.95 {
		return fmt.Errorf("threshold %.2f out of range: want 0.10-0.95", ratio)
	}
	autoThreshold.Lock()
	defer autoThreshold.Unlock()
	autoThreshold.ratio = ratio
	autoThreshold.disabled = false
	return nil
}

// DisableCompactAuto disables threshold-triggered auto compaction.
func DisableCompactAuto() {
	autoThreshold.Lock()
	defer autoThreshold.Unlock()
	autoThreshold.disabled = true
}

// ResetCompactThreshold restores the default threshold (tests only).
func ResetCompactThreshold() {
	autoThreshold.Lock()
	defer autoThreshold.Unlock()
	autoThreshold.ratio = 0.80
	autoThreshold.disabled = false
}

// CompactDescriptor returns the registry descriptor for /compact.
func CompactDescriptor() CommandDescriptor {
	return CommandDescriptor{
		Marker:        MarkerSlash,
		Name:          "compact",
		Kind:          KindGlobal,
		RequiredPerms: PermissionSet(PermRead),
		Description:   "compact context window: /compact [now|stats|auto <ratio|off>]",
		SupportsChain: false,
	}
}

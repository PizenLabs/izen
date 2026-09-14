package modes

import "strings"

type Mode int

const (
	ModeAsk Mode = iota
	ModePlan
	ModeBuild
	ModeInvestigate
	ModeReview
)

func (m Mode) String() string {
	switch m {
	case ModeAsk:
		return "ask"
	case ModePlan:
		return "plan"
	case ModeBuild:
		return "build"
	case ModeInvestigate:
		return "investigate"
	case ModeReview:
		return "review"
	default:
		return "ask"
	}
}

func (m Mode) Description() string {
	switch m {
	case ModeAsk:
		return "explain, inspect, understand — read-only"
	case ModePlan:
		return "architecture, migrations, refactors — no execution"
	case ModeBuild:
		return "implement, refactor, write tests — controlled execution"
	case ModeInvestigate:
		return "debug bugs, failures, regressions — bounded loops"
	case ModeReview:
		return "audit changes, detect risks, inspect regressions"
	default:
		return ""
	}
}

func (m Mode) ReadOnly() bool {
	return m == ModeAsk || m == ModePlan || m == ModeReview
}

// Capability represents a single permission bit for the Capability Matrix.
type Capability int

const (
	CapRead       Capability = 1 << iota // 1 — read files, git log, AST graph
	CapWrite                             // 2 — write/modify files
	CapShell                             // 4 — execute arbitrary shell commands
	CapTest                              // 8 — run test suites
	CapPatch                             // 16 — generate or apply patches
	CapCheckpoint                        // 32 — create or restore git checkpoints
)

// capabilityMatrix defines the immutable POLICY SURFACE for each mode.
//
// Modes are strictly policy-surface filters: they describe which
// operations the UI may propose in a given mode. A mode MUST NOT grant,
// elevate, or infer mutation capabilities. Mutation authority derives
// exclusively from explicit capability tokens evaluated through
// SimpleCapabilityGuard.Evaluate (the 8-clause authorization formula).
// Use EffectiveCapabilities to intersect an explicit grant with the mode
// policy; the result never exceeds the explicit grant.
//
//	| Mode        | Read | Write | Shell | Test | Patch | Checkpoint |
//	|-------------|------|-------|-------|------|-------|------------|
//	| ask         | Y    | N     | N     | N    | N     | N          |
//	| plan        | Y    | N     | N     | N    | N     | N          |
//	| build       | Y    | Y     | Y     | Y    | Y     | Y          |
//	| investigate | Y    | N     | Y     | Y    | N     | N          |
//	| review      | Y    | N     | N     | N    | N     | N          |
var capabilityMatrix = map[Mode]Capability{
	ModeAsk:         CapRead,
	ModePlan:        CapRead,
	ModeBuild:       CapRead | CapWrite | CapShell | CapTest | CapPatch | CapCheckpoint,
	ModeInvestigate: CapRead | CapShell | CapTest,
	ModeReview:      CapRead,
}

func (m Mode) Capabilities() Capability {
	if caps, ok := capabilityMatrix[m]; ok {
		return caps
	}
	return CapRead
}

func (m Mode) Can(c Capability) bool {
	return m.Capabilities()&c != 0
}

func (m Mode) CanRead() bool       { return m.Can(CapRead) }
func (m Mode) CanWrite() bool      { return m.Can(CapWrite) }
func (m Mode) CanShell() bool      { return m.Can(CapShell) }
func (m Mode) CanTest() bool       { return m.Can(CapTest) }
func (m Mode) CanPatch() bool      { return m.Can(CapPatch) }
func (m Mode) CanCheckpoint() bool { return m.Can(CapCheckpoint) }

// PolicySurface returns the mode's capability policy surface: the set of
// operations the mode may propose. It grants zero authority on its own —
// it is a filter, not a credential.
func (m Mode) PolicySurface() Capability { return m.Capabilities() }

// EffectiveCapabilities intersects an explicit capability grant with the
// mode's policy surface. The result NEVER exceeds the explicit grant: a
// mode cannot elevate authority it was not given. In particular, /build
// with an empty grant yields an empty effective set, so the authorization
// gate (SimpleCapabilityGuard.Evaluate) rejects the request with
// ErrCapabilityDenied and zero filesystem mutations occur.
//
// granted is expressed in the modes.Capability bit space; callers
// holding domain capability tokens must project them first.
func EffectiveCapabilities(m Mode, granted Capability) Capability {
	return granted & m.Capabilities()
}

// EffectiveCapabilitiesGrants reports whether the explicit grant,
// filtered through the mode policy surface, still contains want. Modes
// never add bits: without an explicit grant this is always false.
func EffectiveCapabilitiesGrants(m Mode, granted, want Capability) bool {
	return EffectiveCapabilities(m, granted)&want == want
}

func Parse(s string) (Mode, bool) {
	switch strings.ToLower(s) {
	case "ask":
		return ModeAsk, true
	case "plan":
		return ModePlan, true
	case "build":
		return ModeBuild, true
	case "investigate":
		return ModeInvestigate, true
	case "review":
		return ModeReview, true
	default:
		return ModeAsk, false
	}
}

type Resolver struct {
	current Mode
}

func NewResolver() *Resolver {
	return &Resolver{current: ModeAsk}
}

func (r *Resolver) Current() Mode {
	return r.current
}

func (r *Resolver) Set(m Mode) {
	r.current = m
}

func (r *Resolver) Resolve(input string) Mode {
	input = strings.TrimSpace(input)

	for _, m := range []Mode{ModeAsk, ModePlan, ModeBuild, ModeInvestigate, ModeReview} {
		prefix := "/" + m.String()
		if strings.HasPrefix(strings.ToLower(input), prefix) {
			return m
		}
	}

	return r.current
}

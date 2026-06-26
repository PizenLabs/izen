package modes

import "strings"

type Mode int

const (
	ModeAsk Mode = iota
	ModePlan
	ModeBuild
	ModeInvestigate
	ModeReview
	ModeCommit
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
	case ModeCommit:
		return "commit"
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
	case ModeCommit:
		return "generate conventional commit messages from staged changes"
	default:
		return ""
	}
}

func (m Mode) ReadOnly() bool {
	return m == ModeAsk || m == ModePlan || m == ModeReview
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
	case "commit":
		return ModeCommit, true
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

	for _, m := range []Mode{ModeAsk, ModePlan, ModeBuild, ModeInvestigate, ModeReview, ModeCommit} {
		prefix := "/" + m.String()
		if strings.HasPrefix(strings.ToLower(input), prefix) {
			return m
		}
	}

	return r.current
}

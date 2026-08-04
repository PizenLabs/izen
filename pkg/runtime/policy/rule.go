// Package policy evaluates static, readable declarative rules against the
// workspace facts gathered by the analyzer and emits an explicit
// strategy/capability permission decision with human-readable reasons. Every
// rule either grants or denies strategies and capabilities; deny always wins
// over allow, and the evaluation is a pure function of the facts.
package policy

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/runtime/analyzer"
	"github.com/PizenLabs/izen/pkg/runtime/registry"
)

// Rule is a single static policy statement. When its matcher applies to the
// workspace facts, its Allow and Deny entries are folded into the decision
// and its Reason is recorded as a human-readable log line.
type Rule struct {
	ID          string
	Description string
	When        Matcher
	Allow       []string
	Deny        []string
	Reason      string
}

// Matcher declares the conditions under which a Rule applies. Every set
// constraint must hold; unset constraints are ignored. Upper bounds are
// inclusive (max_files 100 matches 100 files), so a threshold rule reads as
// "at most N".
type Matcher struct {
	Intents    []analyzer.Intent
	MaxFiles   int
	MaxTokens  int
	MinTokens  int
	MaxFanout  int
	MinFanout  int
	HasTargets *bool
}

// Matches reports whether a Facts snapshot satisfies every set constraint.
func (m Matcher) Matches(f *analyzer.Facts) bool {
	if len(m.Intents) > 0 && !containsIntent(m.Intents, f.Intent) {
		return false
	}
	if m.MaxFiles > 0 && f.Files > m.MaxFiles {
		return false
	}
	if m.MaxTokens > 0 && f.TokenEstimate > m.MaxTokens {
		return false
	}
	if m.MinTokens > 0 && f.TokenEstimate < m.MinTokens {
		return false
	}
	if m.MaxFanout > 0 && f.MaxFanout > m.MaxFanout {
		return false
	}
	if m.MinFanout > 0 && f.MaxFanout < m.MinFanout {
		return false
	}
	if m.HasTargets != nil && (len(f.TargetFiles) > 0) != *m.HasTargets {
		return false
	}
	return true
}

// Decision is the explicit, human-auditable policy verdict. Granted and
// denied entries map to the reason that produced them.
type Decision struct {
	GrantedStrategies   map[string]string
	GrantedCapabilities map[registry.Capability]string
	DeniedStrategies    map[string]string
	DeniedCapabilities  map[registry.Capability]string
	RuleVerdicts        []RuleVerdict
}

// RuleVerdict records whether a rule applied to the facts and why.
type RuleVerdict struct {
	RuleID  string
	Applied bool
	Reason  string
}

// NewDecision returns an empty decision.
func NewDecision() *Decision {
	return &Decision{
		GrantedStrategies:   make(map[string]string),
		GrantedCapabilities: make(map[registry.Capability]string),
		DeniedStrategies:    make(map[string]string),
		DeniedCapabilities:  make(map[registry.Capability]string),
	}
}

// StrategyGranted reports whether a strategy was explicitly allowed and not
// denied.
func (d *Decision) StrategyGranted(name string) bool {
	_, denied := d.DeniedStrategies[name]
	_, granted := d.GrantedStrategies[name]
	return granted && !denied
}

// CapabilityGranted reports whether a capability was explicitly allowed and
// not denied.
func (d *Decision) CapabilityGranted(c registry.Capability) bool {
	_, denied := d.DeniedCapabilities[c]
	_, granted := d.GrantedCapabilities[c]
	return granted && !denied
}

// ApprovedFor reports whether a strategy plus its required capabilities are
// all permitted. This is the gate the engine applies before execution.
func (d *Decision) ApprovedFor(strategy string, required []registry.Capability) bool {
	if !d.StrategyGranted(strategy) {
		return false
	}
	for _, c := range required {
		if !d.CapabilityGranted(c) {
			return false
		}
	}
	return true
}

// Summary renders the decision as human-readable log lines: one line per
// verdict and one line per grant/denial.
func (d *Decision) Summary() []string {
	lines := make([]string, 0, len(d.RuleVerdicts)+len(d.GrantedStrategies)+len(d.GrantedCapabilities)+len(d.DeniedStrategies)+len(d.DeniedCapabilities))
	for _, v := range d.RuleVerdicts {
		if v.Applied {
			lines = append(lines, "policy: rule "+v.RuleID+" applied — "+v.Reason)
		}
	}
	for name, reason := range d.GrantedStrategies {
		lines = append(lines, "policy: grant strategy "+name+" — "+reason)
	}
	for name, reason := range d.DeniedStrategies {
		lines = append(lines, "policy: deny strategy "+name+" — "+reason)
	}
	for cap, reason := range d.GrantedCapabilities {
		lines = append(lines, "policy: grant capability "+string(cap)+" — "+reason)
	}
	for cap, reason := range d.DeniedCapabilities {
		lines = append(lines, "policy: deny capability "+string(cap)+" — "+reason)
	}
	return lines
}

// Evaluator evaluates Rules against Facts. It is immutable after construction
// and therefore safe for concurrent use.
type Evaluator struct {
	rules []Rule
}

// New returns an Evaluator over the given rules. Nil rules are ignored.
func New(rules ...Rule) *Evaluator {
	filtered := make([]Rule, 0, len(rules))
	for _, r := range rules {
		if r.ID == "" {
			continue
		}
		filtered = append(filtered, r)
	}
	return &Evaluator{rules: filtered}
}

// Evaluate folds every rule into a Decision. The evaluation is a pure
// function of the facts: the same facts always produce the same decision.
func (e *Evaluator) Evaluate(f *analyzer.Facts) *Decision {
	d := NewDecision()
	for _, rule := range e.rules {
		applied := rule.When.Matches(f)
		reason := rule.Reason
		if reason == "" {
			reason = fmt.Sprintf("%s: %s", rule.ID, rule.Description)
		}
		d.RuleVerdicts = append(d.RuleVerdicts, RuleVerdict{
			RuleID:  rule.ID,
			Applied: applied,
			Reason:  reason,
		})
		if !applied {
			continue
		}
		for _, entry := range rule.Allow {
			if kind, value := classify(entry); kind == "capability" {
				d.GrantedCapabilities[registry.Capability(value)] = reason
			} else {
				d.GrantedStrategies[value] = reason
			}
		}
		for _, entry := range rule.Deny {
			if kind, value := classify(entry); kind == "capability" {
				d.DeniedCapabilities[registry.Capability(value)] = reason
			} else {
				d.DeniedStrategies[value] = reason
			}
		}
	}
	return d
}

// classify resolves a rule entry to a capability or a strategy. Entries may
// be prefixed with "capability:" or "strategy:"; bare entries that name a
// known capability are capabilities, everything else is a strategy name.
func classify(entry string) (string, string) {
	lower := strings.ToLower(entry)
	switch {
	case strings.HasPrefix(lower, "capability:"):
		return "capability", strings.TrimSpace(entry[len("capability:"):])
	case strings.HasPrefix(lower, "strategy:"):
		return "strategy", strings.TrimSpace(entry[len("strategy:"):])
	default:
		c := registry.Capability(entry)
		if c.IsKnown() {
			return "capability", entry
		}
		return "strategy", entry
	}
}

func containsIntent(intents []analyzer.Intent, want analyzer.Intent) bool {
	for _, i := range intents {
		if i == want {
			return true
		}
	}
	return false
}

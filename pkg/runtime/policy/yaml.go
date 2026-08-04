package policy

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/PizenLabs/izen/pkg/runtime/analyzer"
)

// yamlConfig is the top-level declarative policy document.
type yamlConfig struct {
	Rules []yamlRule `yaml:"rules"`
}

// yamlRule mirrors Rule with declarative string-based matchers.
type yamlRule struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description"`
	When        yamlWhen `yaml:"when"`
	Allow       []string `yaml:"allow"`
	Deny        []string `yaml:"deny"`
	Reason      string   `yaml:"reason"`
}

// yamlWhen mirrors Matcher with declarative fields.
type yamlWhen struct {
	Intents    []string `yaml:"intents"`
	MaxFiles   int      `yaml:"max_files"`
	MaxTokens  int      `yaml:"max_tokens"`
	MinTokens  int      `yaml:"min_tokens"`
	MaxFanout  int      `yaml:"max_fanout"`
	MinFanout  int      `yaml:"min_fanout"`
	HasTargets *bool    `yaml:"has_targets"`
}

// LoadRules reads a declarative policy document from a YAML file and
// converts it into Rules. Unknown intent names are rejected so typos surface
// at load time instead of silently disabling a rule.
func LoadRules(path string) ([]Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	return LoadRulesBytes(data)
}

// LoadRulesBytes parses a declarative policy document from YAML bytes.
func LoadRulesBytes(data []byte) ([]Rule, error) {
	var cfg yamlConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("policy: parse yaml: %w", err)
	}
	rules := make([]Rule, 0, len(cfg.Rules))
	for _, yr := range cfg.Rules {
		rule, err := yr.rule()
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// rule converts a declarative rule into a typed Rule.
func (yr yamlRule) rule() (Rule, error) {
	if yr.ID == "" {
		return Rule{}, fmt.Errorf("policy: rule with empty id")
	}
	when := Matcher{
		MaxFiles:   yr.When.MaxFiles,
		MaxTokens:  yr.When.MaxTokens,
		MinTokens:  yr.When.MinTokens,
		MaxFanout:  yr.When.MaxFanout,
		MinFanout:  yr.When.MinFanout,
		HasTargets: yr.When.HasTargets,
	}
	for _, intent := range yr.When.Intents {
		typed := analyzer.Intent(intent)
		if !typed.IsKnown() {
			return Rule{}, fmt.Errorf("policy: rule %s references unknown intent %q", yr.ID, intent)
		}
		when.Intents = append(when.Intents, typed)
	}
	return Rule{
		ID:          yr.ID,
		Description: yr.Description,
		When:        when,
		Allow:       yr.Allow,
		Deny:        yr.Deny,
		Reason:      yr.Reason,
	}, nil
}

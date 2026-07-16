package execution

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

type RiskLevel int

const (
	RiskUnknown  RiskLevel = 0
	RiskLow      RiskLevel = 1
	RiskMedium   RiskLevel = 2
	RiskHigh     RiskLevel = 3
	RiskCritical RiskLevel = 4
)

func (r RiskLevel) String() string {
	switch r {
	case RiskLow:
		return "Low"
	case RiskMedium:
		return "Medium"
	case RiskHigh:
		return "High"
	case RiskCritical:
		return "Critical"
	default:
		return "Unknown"
	}
}

type RiskIndicator struct {
	Type   string `json:"type"`
	Detail string `json:"detail"`
	Weight int    `json:"weight"`
}

type RiskResult struct {
	Level      RiskLevel       `json:"level"`
	Label      string          `json:"label"`
	Indicators []RiskIndicator `json:"indicators,omitempty"`
	Summary    string          `json:"summary"`
}

var (
	reDestructiveOp = regexp.MustCompile(`(?i)\b(rm\s+-rf|dd\s+if=|mkfs\.|chmod\s+0|chown\s+-R|:\(\)|> /dev/)`)
	reCredAccess    = regexp.MustCompile(`(?i)(~?/\.ssh|/etc/shadow|/etc/passwd|/etc/ssl|credential|secret|token|api.?key)`)
	rePrivilegeEsc  = regexp.MustCompile(`(?i)\b(sudo|su\s+-|chmod\s+4[0-9]{2}|chown|setuid|setgid)\b`)
	reNetworkOp     = regexp.MustCompile(`(?i)\b(curl|wget|nc\s+|nmap|ssh\s+|telnet|ftp\s+|socat)\b`)
	reShellExec     = regexp.MustCompile(`(?i)\b(sh\s+-c|bash\s+-c|eval\s+|exec\s+|system\(|popen\()`)
	reMassDelete    = regexp.MustCompile(`(?i)(rm\s+-rf\s+[\*~]|rm\s+-rf\s+\.|rm\s+-rf\s+\/\s+)`)
	reCodeObfusc    = regexp.MustCompile(`(?i)(base64\s+-d|frombase64|deobfuscat|eval\(.*base64|rot13|xxd\s+-r)`)
	reSystemPath    = regexp.MustCompile(`^(/etc/|/usr/lib/|/usr/share/|/bin/|/sbin/|/var/log/|/var/lib/)`)
)

type RiskClassifier struct{}

func NewRiskClassifier() *RiskClassifier {
	return &RiskClassifier{}
}

func (rc *RiskClassifier) ClassifyCommand(command string) RiskResult {
	var indicators []RiskIndicator

	check := func(re *regexp.Regexp, typ string, weight int) {
		if m := re.FindString(command); m != "" {
			indicators = append(indicators, RiskIndicator{
				Type:   typ,
				Detail: fmt.Sprintf("matched: %q", m),
				Weight: weight,
			})
		}
	}

	check(reDestructiveOp, "destructive_filesystem_operation", 10)
	check(reCredAccess, "credential_access", 10)
	check(rePrivilegeEsc, "privilege_escalation", 10)
	check(reNetworkOp, "network_communication", 7)
	check(reShellExec, "shell_execution", 6)
	check(reMassDelete, "mass_deletion", 10)
	check(reCodeObfusc, "code_obfuscation", 8)

	return rc.classify(indicators)
}

func (rc *RiskClassifier) ClassifyFileOp(filePath string, isWrite bool) RiskResult {
	var indicators []RiskIndicator

	clean := filepath.Clean(filePath)

	if reSystemPath.MatchString(clean) {
		indicators = append(indicators, RiskIndicator{
			Type:   "destructive_filesystem_operation",
			Detail: fmt.Sprintf("system path: %s", clean),
			Weight: 8,
		})
	}

	if strings.Contains(clean, "..") {
		indicators = append(indicators, RiskIndicator{
			Type:   "destructive_filesystem_operation",
			Detail: fmt.Sprintf("path traversal: %s", clean),
			Weight: 9,
		})
	}

	if strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "~") {
		indicators = append(indicators, RiskIndicator{
			Type:   "destructive_filesystem_operation",
			Detail: fmt.Sprintf("absolute path outside workspace: %s", clean),
			Weight: 5,
		})
	}

	if reCredAccess.MatchString(clean) {
		indicators = append(indicators, RiskIndicator{
			Type:   "credential_access",
			Detail: fmt.Sprintf("sensitive path: %s", clean),
			Weight: 10,
		})
	}

	return rc.classify(indicators)
}

func (rc *RiskClassifier) ClassifyPatch(patch *Patch) RiskResult {
	if patch == nil {
		return RiskResult{Level: RiskUnknown, Label: "Unknown", Summary: "no patch to classify"}
	}

	return rc.ClassifyFileOp(patch.File, true)
}

func (rc *RiskClassifier) classify(indicators []RiskIndicator) RiskResult {
	totalWeight := 0
	maxWeight := 0
	for _, ind := range indicators {
		totalWeight += ind.Weight
		if ind.Weight > maxWeight {
			maxWeight = ind.Weight
		}
	}

	var level RiskLevel
	var label string

	switch {
	case maxWeight >= 10:
		level = RiskCritical
		label = "Critical"
	case maxWeight >= 8:
		level = RiskHigh
		label = "High"
	case maxWeight >= 5 || totalWeight >= 8:
		level = RiskMedium
		label = "Medium"
	default:
		level = RiskLow
		label = "Low"
	}

	summary := fmt.Sprintf("Risk Level: %s", label)
	if len(indicators) > 0 {
		summary += fmt.Sprintf(" (%d risk indicators detected)", len(indicators))
	}

	return RiskResult{
		Level:      level,
		Label:      label,
		Indicators: indicators,
		Summary:    summary,
	}
}

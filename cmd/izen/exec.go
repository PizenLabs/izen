package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/cli"
	"github.com/PizenLabs/izen/internal/runtime/handlers"
)

// execRequest is the normalized input of every non-TUI execution command. It is
// produced by parseExecArgs and consumed by runAuthoritativeCommand, the ONE
// production seam that wires the authoritative runtime. No command builds its
// own execution loop or its own terminal truth.
type execRequest struct {
	// Dir is the workspace root ("." by default).
	Dir string
	// Objective is the human objective, verbatim. The command never rewrites it.
	Objective string
	// Scope is the command surface recorded on the run ("$run"/"$orchestrate").
	// It selects the runtime-resolved mutation authority; it grants nothing.
	Scope string
	// Label is the operator-facing command name used only for messages.
	Label string
	// Targets is the optional explicit target set bound through the
	// authoritative target seam (never a bypass).
	Targets []string
	// Provider and Model are explicit provider/model selections. Empty means
	// "use the configured active binding"; an explicit value must reach the
	// runtime or the command fails, it is never silently ignored.
	Provider string
	Model    string
	// BaseURL overrides the provider's endpoint (automation/self-hosted).
	BaseURL string
	// Mode is the execution mode (ask|investigate|plan|build|review). Empty
	// selects the default ("build") — the mutation-capable mode.
	Mode string
	// Approval is the human-surface policy at a parked approval boundary.
	Approval cli.ApprovalPolicy
	// Timeout bounds the whole command wall-clock. Zero selects the default.
	Timeout time.Duration
	// JSON selects machine-readable stdout. Human diagnostics move to stderr.
	JSON bool
	// Quiet suppresses operator-facing diagnostics on stderr.
	Quiet bool
	// Resume continues the most recently recorded non-terminal objective from
	// the workspace's durable ledger instead of running the given objective.
	Resume bool
	// ResumeFrom records the durable task id this run continues, when known.
	ResumeFrom string
}

// defaultExecMode is the mutation-capable mode every execution command uses
// unless the operator selects another.
const defaultExecMode = "build"

// defaultExecTimeout bounds one CLI objective.
const defaultExecTimeout = 10 * time.Minute

// parseExecArgs parses the shared execution grammar:
//
//	-dir <path>        workspace root (default ".")
//	-target <path>     explicit target (repeatable)
//	-provider <name>   provider override
//	-model <id>        model override
//	-base-url <url>    provider endpoint override
//	-mode <name>       ask|investigate|plan|build|review
//	-approve <policy>  ask|always|never
//	-timeout <dur>     wall-clock bound (e.g. 90s, 5m)
//	-json              machine-readable stdout
//	-quiet             suppress diagnostics
//
// It performs no I/O and no execution, so it is directly testable. Both
// single-dash and double-dash forms are accepted. Unknown flags are rejected
// rather than ignored: a silently ignored option is an option that lies.
func parseExecArgs(command string, args []string) (execRequest, bool, error) {
	req := execRequest{
		Dir:      ".",
		Scope:    "$" + command,
		Label:    command,
		Approval: cli.ApprovalAsk,
		Mode:     defaultExecMode,
		Timeout:  defaultExecTimeout,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if isHelp(arg) {
			return req, true, nil
		}
		name, inline, hasInline := splitFlag(arg)
		next := func() (string, error) {
			if hasInline {
				return inline, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("izen %s: %s requires a value", command, name)
			}
			i++
			return args[i], nil
		}
		switch name {
		case "-dir":
			v, err := next()
			if err != nil {
				return req, false, err
			}
			req.Dir = v
		case "-target":
			v, err := next()
			if err != nil {
				return req, false, err
			}
			if strings.TrimSpace(v) != "" {
				req.Targets = append(req.Targets, v)
			}
		case "-provider":
			v, err := next()
			if err != nil {
				return req, false, err
			}
			req.Provider = strings.TrimSpace(v)
		case "-model":
			v, err := next()
			if err != nil {
				return req, false, err
			}
			req.Model = strings.TrimSpace(v)
		case "-base-url":
			v, err := next()
			if err != nil {
				return req, false, err
			}
			req.BaseURL = strings.TrimSpace(v)
		case "-mode":
			v, err := next()
			if err != nil {
				return req, false, err
			}
			req.Mode = strings.ToLower(strings.TrimSpace(v))
		case "-approve":
			v, err := next()
			if err != nil {
				return req, false, err
			}
			policy, perr := cli.ParseApprovalPolicy(v)
			if perr != nil {
				return req, false, fmt.Errorf("izen %s: %w", command, perr)
			}
			req.Approval = policy
		case "-timeout":
			v, err := next()
			if err != nil {
				return req, false, err
			}
			d, perr := time.ParseDuration(v)
			if perr != nil || d <= 0 {
				return req, false, fmt.Errorf("izen %s: invalid -timeout %q", command, v)
			}
			req.Timeout = d
		case "-json":
			req.JSON = true
		case "-quiet":
			req.Quiet = true
		case "-resume":
			req.Resume = true
		default:
			if strings.HasPrefix(name, "-") {
				return req, false, fmt.Errorf("izen %s: unknown flag %q", command, arg)
			}
			if req.Objective != "" {
				return req, false, fmt.Errorf("izen %s: exactly one objective argument is required", command)
			}
			req.Objective = arg
		}
	}
	if strings.TrimSpace(req.Objective) == "" {
		if !req.Resume {
			return req, false, fmt.Errorf("izen %s: an objective argument is required", command)
		}
	} else if req.Resume {
		return req, false, fmt.Errorf("izen %s: -resume continues the recorded objective and takes no objective argument", command)
	}
	// Validate the mode and provider/model binding early so a misconfiguration
	// fails with a usage error before any wire-up or provider call.
	if _, ok := handlers.ParsePhase(req.Mode); !ok {
		return req, false, fmt.Errorf("izen %s: invalid -mode %q (want ask|investigate|plan|build|review)", command, req.Mode)
	}
	if req.Provider != "" && !isKnownProvider(req.Provider) {
		return req, false, fmt.Errorf("izen %s: unknown provider %q", command, req.Provider)
	}
	if err := validateProviderModelBinding(req.Provider, req.Model); err != nil {
		return req, false, fmt.Errorf("izen %s: %w", command, err)
	}
	return req, false, nil
}

// isHelp reports whether an argument requests help.
func isHelp(arg string) bool {
	switch arg {
	case "-h", "--help", "help":
		return true
	}
	return false
}

// splitFlag normalizes "-x"/"--x"/"-x=v"/"--x=v" into a canonical name plus an
// optional inline value.
func splitFlag(arg string) (name, value string, hasValue bool) {
	if !strings.HasPrefix(arg, "-") {
		return arg, "", false
	}
	trimmed := strings.TrimLeft(arg, "-")
	canonical := "-" + trimmed
	if eq := strings.IndexByte(trimmed, '='); eq >= 0 {
		return "-" + trimmed[:eq], trimmed[eq+1:], true
	}
	return canonical, "", false
}

// isKnownProvider reports whether name is a provider the runtime can construct.
func isKnownProvider(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ollama", "openrouter", "openai", "anthropic", "gemini", "groq", "opencode", "9router":
		return true
	}
	return false
}

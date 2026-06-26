package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/session"
)

type role uint8

const (
	roleSystem role = iota
	roleUser
	roleAI
	roleError
	roleCode
	roleStatus
)

type record struct {
	role role
	text string
}

type patchProposal struct {
	File    string
	Content string
	ID      string
}

type tokenMsg string

type streamDoneMsg struct {
	content     string
	tokenInput  int
	tokenOutput int
}

type streamErrMsg struct{ err error }

type tickMsg time.Time

type investigateResultMsg struct {
	records    []record
	sessionKey string
	err        error
}

type reviewResultMsg struct {
	records      []record
	sessionKey   string
	saveReportFn func()
	err          error
}

type agentDoneMsg struct{ label string }

type buildProposalsReadyMsg struct {
	proposals []patchProposal
}

const (
	version                  = "0.1.0"
	maxInvestigateInvocations = 5
)

var coreModes = []string{"/ask", "/plan", "/build", "/investigate", "/review"}

var utilityCommands = map[modes.Mode][]string{
	modes.ModeAsk:         {"/models", "/clear"},
	modes.ModePlan:        {"/clear"},
	modes.ModeBuild:       {"/undo", "/commit", "/checkpoint", "/clear"},
	modes.ModeInvestigate: {"/history", "/resume", "/clear", "/tokens"},
	modes.ModeReview:      {"/clear"},
}

var globalCommands = []string{"/help", "/mode", "/objective", "/drop", "/quit"}

type model struct {
	cfg      *config.Config
	sess     *session.Session
	provider ai.Provider
	resolver *modes.Resolver
	gitEng   *git.Engine
	graphEng *graph.Engine
	graph    *graph.Graph

	input   strings.Builder
	records []record
	width   int
	height  int

	streamCh       chan tea.Msg
	responseBuffer strings.Builder
	streaming      bool
	spinnerFrame   int
	tokenInput     int
	tokenOutput    int

	agentRunning bool
	agentLabel   string
	agentDone    bool

	showSuggestions bool
	suggestionType  string
	suggestions     []string
	suggestionIdx   int

	pendingFileRefs []string
	attachedFiles   []string

	awaitingConfirmation bool
	pendingProposals     []patchProposal
	acceptAll            bool

	execEng     *execution.Engine
	buildOutput strings.Builder

	investigateInvocationCount int
}

func (m *model) push(r role, text string) {
	lines := strings.Split(text, "\n")
	for _, l := range lines {
		m.records = append(m.records, record{role: r, text: l})
	}
}

func (m *model) pushLines(r role, lines []string) {
	for _, l := range lines {
		m.records = append(m.records, record{role: r, text: l})
	}
}

func (m *model) pushRecords(recs []record) {
	m.records = append(m.records, recs...)
}

package control

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// HandoffLedger persists execution state across model switches, user interrupts,
// and API token exhaustion events. It guarantees that a new model picks up
// seamlessly at step N without re-exploring the codebase or reading past logs.
type HandoffLedger struct {
	mu sync.RWMutex

	ID              string        `json:"id"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	ArchBlueprint   string        `json:"arch_blueprint,omitempty"`
	PreviousFiles   []string      `json:"previous_files,omitempty"`
	Steps           []HandoffStep `json:"steps"`
	CurrentStep     int           `json:"current_step"`
	State           HandoffState  `json:"state"`
	InterruptReason string        `json:"interrupt_reason,omitempty"`
	ModelName       string        `json:"model_name,omitempty"`

	checkpointDir string
}

type HandoffStep struct {
	StepNum      int               `json:"step_num"`
	Type         string            `json:"type"`
	Target       string            `json:"target"`
	Description  string            `json:"description,omitempty"`
	Status       HandoffStepStatus `json:"status"`
	FileSnapshot string            `json:"file_snapshot,omitempty"`
	Result       string            `json:"result,omitempty"`
	Error        string            `json:"error,omitempty"`
}

type HandoffState string

const (
	HandoffActive   HandoffState = "active"
	HandoffPaused   HandoffState = "paused"
	HandoffComplete HandoffState = "complete"
	HandoffFailed   HandoffState = "failed"
)

type HandoffStepStatus string

const (
	StepPending   HandoffStepStatus = "pending"
	StepRunning   HandoffStepStatus = "running"
	StepCompleted HandoffStepStatus = "completed"
	StepFailed    HandoffStepStatus = "failed"
	StepSkipped   HandoffStepStatus = "skipped"
)

var handoffDir = ".izen/handoff"

func NewHandoffLedger(modelName string) *HandoffLedger {
	return &HandoffLedger{
		ID:            fmt.Sprintf("hlo-%d", time.Now().UnixNano()),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		Steps:         nil,
		CurrentStep:   1,
		State:         HandoffActive,
		ModelName:     modelName,
		checkpointDir: handoffDir,
	}
}

func (hl *HandoffLedger) Init(steps []HandoffStep, blueprint string) {
	hl.mu.Lock()
	defer hl.mu.Unlock()
	hl.Steps = steps
	hl.ArchBlueprint = blueprint
	hl.CurrentStep = 1
	hl.State = HandoffActive
	hl.UpdatedAt = time.Now()
}

func (hl *HandoffLedger) Save() error {
	hl.mu.RLock()
	id := hl.ID
	hl.mu.RUnlock()

	dir := filepath.Join(handoffDir, id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, "ledger.json")
	return hl.saveTo(path)
}

func (hl *HandoffLedger) saveTo(path string) error {
	hl.mu.Lock()
	hl.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(hl, "", "  ")
	hl.mu.Unlock()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func LoadHandoffLedger(id string) (*HandoffLedger, error) {
	path := filepath.Join(handoffDir, id, "ledger.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("handoff ledger %s not found", id)
		}
		return nil, err
	}
	var hl HandoffLedger
	if err := json.Unmarshal(data, &hl); err != nil {
		return nil, err
	}
	hl.checkpointDir = handoffDir
	if hl.Steps == nil {
		hl.Steps = []HandoffStep{}
	}
	return &hl, nil
}

func LoadLatestHandoffLedger() (*HandoffLedger, error) {
	entries, err := os.ReadDir(handoffDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			cp := filepath.Join(handoffDir, e.Name(), "ledger.json")
			if _, err := os.Stat(cp); err == nil {
				ids = append(ids, e.Name())
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] > ids[j]
	})
	return LoadHandoffLedger(ids[0])
}

func (hl *HandoffLedger) CompleteStep(stepNum int, result string) {
	hl.mu.Lock()
	defer hl.mu.Unlock()
	for i := range hl.Steps {
		if hl.Steps[i].StepNum == stepNum {
			hl.Steps[i].Status = StepCompleted
			hl.Steps[i].Result = result
			break
		}
	}
	if stepNum >= hl.CurrentStep {
		hl.CurrentStep = stepNum + 1
	}
	if hl.CurrentStep > len(hl.Steps) {
		hl.State = HandoffComplete
	}
	hl.UpdatedAt = time.Now()
}

func (hl *HandoffLedger) FailStep(stepNum int, errMsg string) {
	hl.mu.Lock()
	defer hl.mu.Unlock()
	for i := range hl.Steps {
		if hl.Steps[i].StepNum == stepNum {
			hl.Steps[i].Status = StepFailed
			hl.Steps[i].Error = errMsg
			break
		}
	}
	hl.State = HandoffFailed
	hl.UpdatedAt = time.Now()
}

func (hl *HandoffLedger) Pause(reason string) {
	hl.mu.Lock()
	defer hl.mu.Unlock()
	hl.State = HandoffPaused
	hl.InterruptReason = reason
	hl.UpdatedAt = time.Now()
}

func (hl *HandoffLedger) Resume() {
	hl.mu.Lock()
	defer hl.mu.Unlock()
	hl.State = HandoffActive
	hl.InterruptReason = ""
	hl.UpdatedAt = time.Now()
}

func (hl *HandoffLedger) PendingSteps() []HandoffStep {
	hl.mu.RLock()
	defer hl.mu.RUnlock()
	var pending []HandoffStep
	for _, s := range hl.Steps {
		if s.Status == StepPending || s.Status == StepRunning {
			pending = append(pending, s)
		}
	}
	return pending
}

func (hl *HandoffLedger) CompletedSteps() []HandoffStep {
	hl.mu.RLock()
	defer hl.mu.RUnlock()
	var done []HandoffStep
	for _, s := range hl.Steps {
		if s.Status == StepCompleted {
			done = append(done, s)
		}
	}
	return done
}

func (hl *HandoffLedger) CurrentStepInfo() *HandoffStep {
	hl.mu.RLock()
	defer hl.mu.RUnlock()
	for _, s := range hl.Steps {
		if s.StepNum == hl.CurrentStep {
			return &HandoffStep{
				StepNum:      s.StepNum,
				Type:         s.Type,
				Target:       s.Target,
				Description:  s.Description,
				Status:       s.Status,
				FileSnapshot: s.FileSnapshot,
			}
		}
	}
	return nil
}

// HandoffVectorPayload returns a concise summary for model handoff.
// It includes (1) architecture blueprint, (2) previously generated files,
// (3) exact target for the current step. This is what gets injected into
// a new model's context on resume — no re-exploration needed.
func (hl *HandoffLedger) HandoffVectorPayload() string {
	hl.mu.RLock()
	defer hl.mu.RUnlock()

	var b strings.Builder
	b.WriteString("=== HANDOFF VECTOR ===\n")
	if hl.ArchBlueprint != "" {
		fmt.Fprintf(&b, "Blueprint: %s\n", hl.ArchBlueprint)
	}
	done := hl.completedStepsLocked()
	if len(done) > 0 {
		fmt.Fprintf(&b, "Completed: %d step(s)\n", len(done))
		for _, s := range done {
			fmt.Fprintf(&b, "  [%d] %s: %s\n", s.StepNum, s.Type, s.Description)
		}
	}
	if len(hl.PreviousFiles) > 0 {
		fmt.Fprintf(&b, "Files modified: %s\n", strings.Join(hl.PreviousFiles, ", "))
	}
	if current := hl.currentStepLocked(); current != nil {
		fmt.Fprintf(&b, "Current step: [%d] %s %s — %s\n",
			current.StepNum, current.Type, current.Target, current.Description)
		b.WriteString("Resume at this step. No re-exploration needed.\n")
	}
	return b.String()
}

func (hl *HandoffLedger) completedStepsLocked() []HandoffStep {
	var done []HandoffStep
	for _, s := range hl.Steps {
		if s.Status == StepCompleted {
			done = append(done, s)
		}
	}
	return done
}

func (hl *HandoffLedger) currentStepLocked() *HandoffStep {
	for _, s := range hl.Steps {
		if s.StepNum == hl.CurrentStep {
			return &HandoffStep{
				StepNum:     s.StepNum,
				Type:        s.Type,
				Target:      s.Target,
				Description: s.Description,
				Status:      s.Status,
			}
		}
	}
	return nil
}

func (hl *HandoffLedger) AddPreviousFile(path string) {
	hl.mu.Lock()
	defer hl.mu.Unlock()
	hl.PreviousFiles = append(hl.PreviousFiles, path)
	hl.UpdatedAt = time.Now()
}

func (hl *HandoffLedger) SetArchBlueprint(blueprint string) {
	hl.mu.Lock()
	defer hl.mu.Unlock()
	hl.ArchBlueprint = blueprint
	hl.UpdatedAt = time.Now()
}

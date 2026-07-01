package plan

// Step represents a single tactical operation within an execution plan.
type Step struct {
	TargetFile  string   `json:"target_file"`
	Action      string   `json:"action"`
	Symbols     []string `json:"symbols"`
	Explanation string   `json:"explanation"`
}

// ExecutionPlan is the master plan containing all steps, prerequisites, and impact analysis.
type ExecutionPlan struct {
	Steps          []Step   `json:"steps"`
	Prerequisites  []string `json:"prerequisites"`
	ImpactAnalysis string   `json:"impact_analysis"`
}

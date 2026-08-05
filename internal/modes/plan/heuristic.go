package plan

// This file is intentionally lean: the legacy heuristic plan synthesis
// fallbacks (extractTasksFromProse, extractTasksFromLedger and the
// root-context CODE_MOD [Target 1/1] task) were HARD-KILLED in this cycle.
//
// They regex-mined file paths out of narrative model prose and the
// investigation ledger, producing empty-target "apply the plan to the project
// root" tasks that bypassed every evidence gate. Generation requests are now
// owned deterministically by the IR-driven intent compiler before LLM plan
// synthesis ever runs; when synthesis still fails, the engine surfaces an
// explicit error and the TUI escalates instead of fabricating a heuristic
// plan.
//
// The "plan synthesis fell back to heuristic file extraction" message can no
// longer be logged or triggered in live user flows.

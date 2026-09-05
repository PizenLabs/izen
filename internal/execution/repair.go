package execution

// RepairRecoveryPrompt injects the strict SEARCH/REPLACE contract when a
// mutation transitions from FULL_REWRITE to BOUNDED_PATCH after
// OUTPUT_EXHAUSTED. It forces the LLM to emit ONLY valid
// <<<<<<< SEARCH ... ======= ... >>>>>>> REPLACE blocks and prohibits
// explanation text or full-file output during patch recovery.
func RepairRecoveryPrompt(recoveryReason string) string {
	return "RECOVERY MODE: previous FULL_REWRITE attempt exhausted output budget (OUTPUT_EXHAUSTED).\n" +
		"You MUST output ONLY valid SEARCH/REPLACE patch blocks.\n" +
		"FORMAT REQUIRED:\n" +
		"<<<<<<< SEARCH\n" +
		"... exact original lines ...\n" +
		"=======\n" +
		"... replacement lines ...\n" +
		">>>>>>> REPLACE\n" +
		"PROHIBITED: explaining text, markdown fences, full-file output, or any content outside the SEARCH/REPLACE block.\n" +
		"REASON: " + recoveryReason
}

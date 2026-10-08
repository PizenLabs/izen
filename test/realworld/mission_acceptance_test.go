package realworld

import "testing"

// ── MISSION ACCEPTANCE (opt-in, real local model) ───────────────────────────
//
// These arms pin the closed-loop acceptance for the CREATE-class work:
//
//	create a new file named tata.txt
//	create a new file named tata.txt containing "hello"
//	create a new file named tata2.txt
//	a modification against an existing file
//	a read-only investigation naming its target
//
// They drive the SAME production composition as TestRealWorld_A_Create (the
// real $prompt path: compose.Wire → autonomy.Driver → RuntimeExecutor) and the
// harness asserts nothing about model prose — only the real filesystem, the
// authority's verdict and the recorded evidence. Opt-in via
// IZEN_LIVE_FORENSICS=1, exactly like the rest of test/realworld.

func TestMissionAcceptance_CreateExactPrompt(t *testing.T) {
	requireLiveModel(t)
	run(t, Task{
		ID:     "mission-create",
		Name:   "MISSION — create a new file named tata.txt",
		Prompt: "create a new file named tata.txt",
		Files: map[string]string{
			"README.md": "# Sample Project\n\nA small fixture repository.\n",
		},
		AnswerApprovals: true,
	})
}

func TestMissionAcceptance_CreateWithContent(t *testing.T) {
	requireLiveModel(t)
	run(t, Task{
		ID:     "mission-create-content",
		Name:   `MISSION — create a new file named tata.txt containing "hello"`,
		Prompt: `create a new file named tata.txt containing "hello"`,
		Files: map[string]string{
			"README.md": "# Sample Project\n\nA small fixture repository.\n",
		},
		AnswerApprovals: true,
	})
}

func TestMissionAcceptance_CreateSecondTarget(t *testing.T) {
	requireLiveModel(t)
	run(t, Task{
		ID:     "mission-create-2",
		Name:   "MISSION — create a new file named tata2.txt",
		Prompt: "create a new file named tata2.txt",
		Files: map[string]string{
			"README.md": "# Sample Project\n\nA small fixture repository.\n",
		},
		AnswerApprovals: true,
	})
}

func TestMissionAcceptance_ModifyExisting(t *testing.T) {
	requireLiveModel(t)
	run(t, Task{
		ID:     "mission-modify",
		Name:   "MISSION — modify an existing file",
		Prompt: `change the greeting in @greeting.txt to exactly "Hello, world!"`,
		Files: map[string]string{
			"greeting.txt": "Helo, world!\n",
			"README.md":    "# Sample Project\n\nA small fixture repository.\n",
		},
		AnswerApprovals: true,
	})
}

func TestMissionAcceptance_ReadOnlyInvestigation(t *testing.T) {
	requireLiveModel(t)
	run(t, Task{
		ID:     "mission-read-only",
		Name:   "MISSION — read-only investigation naming the target",
		Prompt: "read @greeting.txt and tell me what it contains",
		Files: map[string]string{
			"greeting.txt": "Helo, world!\n",
			"README.md":    "# Sample Project\n\nA small fixture repository.\n",
		},
		AnswerApprovals: false,
	})
}

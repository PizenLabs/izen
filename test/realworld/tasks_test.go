package realworld

import "testing"

// ── THE FIVE REPRESENTATIVE REAL-WORLD TASKS ────────────────────────────────
//
// Each fixture is small but meaningful and is NOT shaped around one particular
// implementation. The objective text is handed to the runtime verbatim; where
// discovery is under test (Tasks B–E) no filename is supplied.
//
// Task A — CREATE.      target does not exist; content unspecified.
// Task B — MODIFY.      an incorrect greeting must be found and fixed.
// Task C — INVESTIGATE. a failing user endpoint; NO mutation permitted.
// Task D — FIX.         the same failing endpoint; mutation required.
// Task E — PERFORMANCE. a bottleneck must be found; NO mutation permitted.

var taskA = Task{
	ID:     "A-create",
	Name:   "A / CREATE — create new file named testing.md",
	Prompt: "create new file named testing.md",
	Files: map[string]string{
		"README.md":     "# Sample Project\n\nA small fixture repository.\n",
		"docs/notes.md": "# Notes\n\nNothing here yet.\n",
	},
	AnswerApprovals: true,
}

var taskB = Task{
	ID:     "B-modify",
	Name:   "B / MODIFY — find the incorrect greeting and fix it",
	Prompt: "find the incorrect greeting in this project and fix it",
	Files: map[string]string{
		"go.mod": "module greetingfixture\n\ngo 1.21\n",
		"main.go": `package main

import "fmt"

func main() {
	fmt.Println(greeting())
}

func greeting() string {
	return "Helo, world!"
}
`,
		"README.md": "# Greeting Fixture\n\nThe program should print \"Hello, world!\".\n",
	},
	AnswerApprovals: true,
}

// userEndpointBug is one shared fixture for Tasks C and D. The bug is real and
// deterministic: userHandler asks findUser for the empty id instead of the
// request's id, so /user?id=1 always answers 500.
var userEndpointFiles = map[string]string{
	"go.mod": "module userendpoint\n\ngo 1.21\n",
	"main.go": `package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

func main() {
	http.HandleFunc("/user", userHandler)
	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func userHandler(w http.ResponseWriter, r *http.Request) {
	u, ok := findUser("")
	if !ok {
		http.Error(w, "user not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(u)
}
`,
	"store.go": `package main

// User is a stored user record.
type User struct {
	ID   string ` + "`json:\"id\"`" + `
	Name string ` + "`json:\"name\"`" + `
}

var usersByID = map[string]User{
	"1": {ID: "1", Name: "Ada"},
	"2": {ID: "2", Name: "Grace"},
}

// findUser looks up a user by id.
func findUser(id string) (User, bool) {
	u, ok := usersByID[id]
	return u, ok
}
`,
}

var taskC = Task{
	ID:     "C-investigate",
	Name:   "C / INVESTIGATE — user endpoint error, do not modify",
	Prompt: "investigate why this application returns an error from the user endpoint. Do not modify anything.",
	Files:  userEndpointFiles,
	// An investigation must not mutate; if an approval boundary appears the
	// recording is the finding, not an invitation to approve.
	AnswerApprovals: false,
}

var taskD = Task{
	ID:              "D-fix",
	Name:            "D / FIX — find and fix the failing user endpoint",
	Prompt:          "find and fix the bug causing the user endpoint to fail",
	Files:           userEndpointFiles,
	AnswerApprovals: true,
}

var taskE = Task{
	ID:     "E-performance",
	Name:   "E / PERFORMANCE — find the main bottleneck, do not modify",
	Prompt: "find the main performance bottleneck in this project. Do not modify anything.",
	Files: map[string]string{
		"go.mod": "module perffixture\n\ngo 1.21\n",
		"report.go": `package report

import (
	"strconv"
	"strings"
)

// Item is one row of a report.
type Item struct {
	Name string
	Size int
}

// BuildReport renders a report for the given items.
func BuildReport(items []Item) string {
	var out string
	for _, it := range items {
		// Recompute the total on every iteration (the bottleneck).
		total := 0
		for _, other := range items {
			total += other.Size
		}
		out += it.Name + ": " + strings.Repeat("=", total%8) + "\n"
		out += "  size=" + strconv.Itoa(it.Size) + " total=" + strconv.Itoa(total) + "\n"
	}
	return out
}
`,
		"README.md": "# Report Fixture\n\nBuildReport renders a report for a list of items.\n",
	},
	AnswerApprovals: false,
}

func TestRealWorld_A_Create(t *testing.T) {
	requireLiveModel(t)
	run(t, taskA)
}

func TestRealWorld_B_Modify(t *testing.T) {
	requireLiveModel(t)
	run(t, taskB)
}

func TestRealWorld_C_Investigate(t *testing.T) {
	requireLiveModel(t)
	run(t, taskC)
}

func TestRealWorld_D_Fix(t *testing.T) {
	requireLiveModel(t)
	run(t, taskD)
}

func TestRealWorld_E_Performance(t *testing.T) {
	requireLiveModel(t)
	run(t, taskE)
}

// ── DIAGNOSTIC ARMS ─────────────────────────────────────────────────────────
//
// These do NOT replace the benchmark. They answer the clarify boundary with the
// true target so the experiment can tell "discovery is blocked" apart from "the
// inspect→mutate→verify→PROVEN chain is broken". The main arms above leave the
// clarify unanswered on purpose.

func TestRealWorld_B_ModifyDiag(t *testing.T) {
	requireLiveModel(t)
	d := taskB
	d.ID = "B-modify-diag"
	d.Name = "B-diag / MODIFY (target named by the human at clarify)"
	d.ClarifyAnswer = "main.go"
	run(t, d)
}

func TestRealWorld_E_PerformanceDiag(t *testing.T) {
	requireLiveModel(t)
	d := taskE
	d.ID = "E-performance-diag"
	d.Name = "E-diag / PERFORMANCE (target named by the human at clarify)"
	d.ClarifyAnswer = "report.go"
	run(t, d)
}

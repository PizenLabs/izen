package progress

import (
	"reflect"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/execution/capability"
)

func TestFingerprintKeyStability(t *testing.T) {
	tests := []struct {
		name  string
		a, b  Fingerprint
		equal bool
	}{
		{
			name:  "identical fingerprints match",
			a:     Fingerprint{"verify_entry", "EXECUTION_FAILED", "/index.html", "src/app.js", ""},
			b:     Fingerprint{"verify_entry", "EXECUTION_FAILED", "/index.html", "src/app.js", ""},
			equal: true,
		},
		{
			name:  "whitespace and trailing zero-valued fields ignored",
			a:     Fingerprint{"verify_entry", "EXECUTION_FAILED", " /index.html ", "src/app.js", ""},
			b:     Fingerprint{"verify_entry", "EXECUTION_FAILED", "/index.html", "src/app.js ", ""},
			equal: true,
		},
		{
			name:  "field boundary cannot be shifted",
			a:     Fingerprint{"ab", "C", "", "", ""},
			b:     Fingerprint{"a", "bC", "", "", ""},
			equal: false,
		},
		{
			name:  "verifier differs",
			a:     Fingerprint{"verify_entry", "EXECUTION_FAILED", "/i.html", "a.js", ""},
			b:     Fingerprint{"verify_links", "EXECUTION_FAILED", "/i.html", "a.js", ""},
			equal: false,
		},
		{
			name:  "error class differs",
			a:     Fingerprint{"verify_entry", "EXECUTION_FAILED", "/i.html", "a.js", ""},
			b:     Fingerprint{"verify_entry", "DIAGNOSIS_UNCERTAIN", "/i.html", "a.js", ""},
			equal: false,
		},
		{
			name:  "location differs",
			a:     Fingerprint{"verify_entry", "EXECUTION_FAILED", "/i.html", "a.js", ""},
			b:     Fingerprint{"verify_entry", "EXECUTION_FAILED", "/other.html", "a.js", ""},
			equal: false,
		},
		{
			name:  "artifact differs",
			a:     Fingerprint{"verify_entry", "EXECUTION_FAILED", "/i.html", "a.js", ""},
			b:     Fingerprint{"verify_entry", "EXECUTION_FAILED", "/i.html", "b.js", ""},
			equal: false,
		},
		{
			name:  "symbol populated on one side only",
			a:     Fingerprint{"verify_entry", "EXECUTION_FAILED", "/i.html", "a.js", ""},
			b:     Fingerprint{"verify_entry", "EXECUTION_FAILED", "/i.html", "a.js", "render"},
			equal: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.a.Key() == tc.b.Key()
			if got != tc.equal {
				t.Fatalf("Key equality = %v, want %v (a=%q b=%q)", got, tc.equal, tc.a.Key(), tc.b.Key())
			}
		})
	}
}

func TestFingerprintEmpty(t *testing.T) {
	if !(Fingerprint{}).Empty() {
		t.Fatal("zero fingerprint must be empty")
	}
	if (Fingerprint{Symbol: "x"}).Empty() {
		t.Fatal("fingerprint with any populated field must not be empty")
	}
	if (Fingerprint{Symbol: "  "}).Empty() != true {
		t.Fatal("whitespace-only fingerprint carries no identity")
	}
}

func TestFingerprintsDedupAndSort(t *testing.T) {
	d1 := capability.Defect{Code: "zebra", Class: capability.FailureExecutionFailed, Entry: "/i.html"}
	d2 := capability.Defect{Code: "alpha", Class: capability.FailureVerificationFailed, Entry: "/i.html"}
	dup := capability.Defect{Code: "zebra", Class: capability.FailureExecutionFailed, Entry: "/i.html"}
	got := Fingerprints([]capability.Defect{d1, d2, dup, {}})
	if len(got) != 2 {
		t.Fatalf("got %d fingerprints, want 2 (dedup must collapse the duplicate defect): %v", len(got), got)
	}
	if got[0].Key() >= got[1].Key() {
		t.Fatalf("fingerprints not sorted by key: %v", []string{got[0].Key(), got[1].Key()})
	}
	if got[1].Verifier != "zebra" || got[1].ErrorClass != string(capability.FailureExecutionFailed) {
		t.Fatalf("defect fields not mapped: %+v", got[1])
	}
	if Fingerprints(nil) != nil {
		t.Fatal("no defects must yield no fingerprints")
	}
}

// verdictsFor runs a detector over a sequence of snapshots.
func verdictsFor(d *Detector, snaps []Snapshot) []Verdict {
	out := make([]Verdict, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, d.Observe(s))
	}
	return out
}

func TestDetectorVerdicts(t *testing.T) {
	base := Snapshot{Round: 1, Satisfied: 2, Unmet: 1, FingerprintKeys: []string{"k1"}, EvidenceDigest: "e1", Mutations: 1}

	tests := []struct {
		name string
		from Snapshot
		mut  func(Snapshot) Snapshot
		want Verdict
	}{
		{
			name: "satisfied count changes",
			mut:  func(s Snapshot) Snapshot { s.Satisfied = 3; return s },
			want: VerdictProgress,
		},
		{
			name: "unmet count changes",
			mut:  func(s Snapshot) Snapshot { s.Unmet = 2; return s },
			want: VerdictProgress,
		},
		{
			name: "fingerprint set changes",
			mut:  func(s Snapshot) Snapshot { s.FingerprintKeys = []string{"k1", "k2"}; return s },
			want: VerdictProgress,
		},
		{
			name: "evidence digest changes",
			mut:  func(s Snapshot) Snapshot { s.EvidenceDigest = "e2"; return s },
			want: VerdictProgress,
		},
		{
			name: "mutations increase with everything else identical",
			mut:  func(s Snapshot) Snapshot { s.Round = 9; s.Mutations = 2; return s },
			want: VerdictProgress,
		},
		{
			name: "round alone is not progress",
			mut:  func(s Snapshot) Snapshot { s.Round = 42; return s },
			want: VerdictUnknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDetector(3)
			if v := d.Observe(base); v != VerdictUnknown {
				t.Fatalf("first Observe = %v, want UNKNOWN", v)
			}
			got := d.Observe(tc.mut(base))
			if got != tc.want {
				t.Fatalf("verdict = %v, want %v", got, tc.want)
			}
			if tc.want == VerdictProgress && d.StagnantRounds() != 0 {
				t.Fatalf("stagnant counter = %d after progress, want 0", d.StagnantRounds())
			}
			if d.Reason() != "" {
				t.Fatalf("reason must be empty unless NO_PROGRESS, got %q", d.Reason())
			}
		})
	}
}

func TestDetectorNoProgressAtThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		snaps     []Snapshot
		want      []Verdict
	}{
		{
			name:      "default threshold fires on second identical observation",
			threshold: 0, // normalizes to 2
			snaps: []Snapshot{
				{Round: 1, Satisfied: 1, Unmet: 1, FingerprintKeys: []string{"k1"}, EvidenceDigest: "e", Mutations: 3},
				{Round: 2, Satisfied: 1, Unmet: 1, FingerprintKeys: []string{"k1"}, EvidenceDigest: "e", Mutations: 3},
			},
			want: []Verdict{VerdictUnknown, VerdictNoProgress},
		},
		{
			name:      "threshold 3 stays unknown until the third identical snapshot",
			threshold: 3,
			snaps: []Snapshot{
				{Round: 1, Satisfied: 1, Unmet: 1, FingerprintKeys: []string{"k1"}, EvidenceDigest: "e", Mutations: 3},
				{Round: 2, Satisfied: 1, Unmet: 1, FingerprintKeys: []string{"k1"}, EvidenceDigest: "e", Mutations: 3},
				{Round: 3, Satisfied: 1, Unmet: 1, FingerprintKeys: []string{"k1"}, EvidenceDigest: "e", Mutations: 3},
			},
			want: []Verdict{VerdictUnknown, VerdictUnknown, VerdictNoProgress},
		},
		{
			name:      "a mutation between identical states restarts the counter",
			threshold: 2,
			snaps: []Snapshot{
				{Round: 1, Satisfied: 1, Unmet: 1, FingerprintKeys: []string{"k1"}, EvidenceDigest: "e", Mutations: 3},
				{Round: 2, Satisfied: 1, Unmet: 1, FingerprintKeys: []string{"k1"}, EvidenceDigest: "e", Mutations: 4},
				{Round: 3, Satisfied: 1, Unmet: 1, FingerprintKeys: []string{"k1"}, EvidenceDigest: "e", Mutations: 4},
			},
			want: []Verdict{VerdictUnknown, VerdictProgress, VerdictUnknown},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDetector(tc.threshold)
			got := verdictsFor(d, tc.snaps)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("verdicts = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectorNoProgressReasonAndLast(t *testing.T) {
	d := NewDetector(2)
	s := Snapshot{Round: 1, Satisfied: 0, Unmet: 2, FingerprintKeys: []string{"aa", "bb", "cc", "dd"}, EvidenceDigest: "e", Mutations: 0}
	d.Observe(s)
	d.Observe(s)
	if d.Reason() == "" {
		t.Fatal("NO_PROGRESS must carry a reason")
	}
	for _, k := range []string{"aa", "bb", "cc"} {
		if !strings.Contains(d.Reason(), k) {
			t.Fatalf("reason %q must name recurring fingerprint %q", d.Reason(), k)
		}
	}
	if strings.Contains(d.Reason(), "dd") {
		t.Fatalf("reason must stay bounded, got %q", d.Reason())
	}
	if !reflect.DeepEqual(d.Last(), s) {
		t.Fatalf("Last() = %+v, want %+v", d.Last(), s)
	}
}

func TestDetectorNoProgressWithoutFingerprints(t *testing.T) {
	d := NewDetector(2)
	s := Snapshot{Round: 1, Satisfied: 0, Unmet: 0, EvidenceDigest: "e"}
	if v := d.Observe(s); v != VerdictUnknown {
		t.Fatalf("first = %v", v)
	}
	if v := d.Observe(s); v != VerdictNoProgress {
		t.Fatalf("second = %v, want NO_PROGRESS", v)
	}
	if !strings.Contains(d.Reason(), "no progress") {
		t.Fatalf("reason = %q", d.Reason())
	}
}

func TestDetectorReset(t *testing.T) {
	d := NewDetector(2)
	s := Snapshot{Round: 1, Satisfied: 0, Unmet: 1, FingerprintKeys: []string{"k1"}, EvidenceDigest: "e", Mutations: 0}
	d.Observe(s)
	d.Observe(s)
	if d.StagnantRounds() == 0 || d.Reason() == "" {
		t.Fatal("precondition: detector should be in NO_PROGRESS state")
	}
	d.Reset()
	if d.StagnantRounds() != 0 || d.Reason() != "" || !reflect.DeepEqual(d.Last(), Snapshot{}) {
		t.Fatalf("Reset left state: stagnant=%d reason=%q last=%+v", d.StagnantRounds(), d.Reason(), d.Last())
	}
	// A fresh detector after reset must not fire on the first observation.
	if v := d.Observe(s); v != VerdictUnknown {
		t.Fatalf("post-reset Observe = %v, want UNKNOWN", v)
	}
	if v := d.Observe(s); v != VerdictNoProgress {
		t.Fatalf("post-reset second Observe = %v, want NO_PROGRESS", v)
	}
}

package resource

import "testing"

// fakeSnapshot is a minimal resource.Snapshot used for interface checks.
type fakeSnapshot struct{}

func (fakeSnapshot) Hash() string { return "" }
func (fakeSnapshot) Data() any    { return nil }

func TestResourceKindValues(t *testing.T) {
	cases := []struct {
		kind ResourceKind
		want string
	}{
		{KindFile, "res.file"},
		{KindGitRepo, "res.git"},
		{KindTerminal, "res.terminal"},
	}
	for _, c := range cases {
		if got := c.kind.String(); got != c.want {
			t.Errorf("%s.String() = %q, want %q", c.kind, got, c.want)
		}
	}
}

func TestSnapshotInterfaceSatisfied(t *testing.T) {
	var s Snapshot = fakeSnapshot{}
	if s.Hash() != "" || s.Data() != nil {
		t.Fatal("fakeSnapshot should report empty hash and nil data")
	}
}

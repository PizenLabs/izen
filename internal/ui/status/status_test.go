package status

import "testing"

func TestFormatTokens(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{840, "840"},
		{999, "999"},
		{1000, "1.0k"},
		{2340, "2.3k"},
		{1500, "1.5k"},
		{9999, "10.0k"},
		{10000, "10k"},
		{25000, "25k"},
	}
	for _, c := range cases {
		if got := FormatTokens(c.n); got != c.want {
			t.Errorf("FormatTokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestTrackerRecordAndSnapshot(t *testing.T) {
	tr := New()
	if tr.Has() {
		t.Fatal("new tracker should not have usage")
	}
	if got := FormatUsage(tr.Snapshot()); got != "" {
		t.Errorf("empty snapshot = %q, want empty", got)
	}

	tr.Record(2300, 1500)
	s := tr.Snapshot()
	if !s.Has || s.Input != 2300 || s.Output != 1500 || s.Total != 3800 {
		t.Errorf("snapshot = %+v, want input=2300 output=1500 total=3800", s)
	}

	got := FormatUsage(s)
	want := "↓2.3k + ↑1.5k tok"
	if got != want {
		t.Errorf("FormatUsage = %q, want %q", got, want)
	}
}

func TestTrackerReset(t *testing.T) {
	tr := New()
	tr.Record(100, 200)
	tr.Reset()
	if tr.Has() {
		t.Error("reset should clear usage")
	}
	if got := FormatUsage(tr.Snapshot()); got != "" {
		t.Errorf("after reset = %q, want empty", got)
	}
}

func TestFormatUsageZeroTotal(t *testing.T) {
	tr := New()
	tr.Record(0, 0)
	if got := FormatUsage(tr.Snapshot()); got != "0 tok" {
		t.Errorf("zero usage = %q, want %q", got, "0 tok")
	}
}

func TestFormatUsageValues(t *testing.T) {
	if got := FormatUsageValues(800, 300); got != "↓800 + ↑300 tok" {
		t.Errorf("FormatUsageValues = %q, want %q", got, "↓800 + ↑300 tok")
	}
	if got := FormatUsageValues(2300, 1500); got != "↓2.3k + ↑1.5k tok" {
		t.Errorf("FormatUsageValues = %q, want %q", got, "↓2.3k + ↑1.5k tok")
	}
}

func TestFormatUsageContext(t *testing.T) {
	cases := []struct {
		name   string
		input  int
		output int
		total  int
		limit  int
		want   string
	}{
		{"cloud split", 2300, 1500, 3800, 128000, "↓2.3k + ↑1.5k tok (3%)"},
		{"cloud small", 800, 300, 1100, 128000, "↓800 + ↑300 tok (1%)"},
		{"total fallback", 0, 0, 3800, 128000, "3.8k tok (3%)"},
		{"zero usage", 0, 0, 0, 128000, "0 tok (0%)"},
		{"unknown window", 2300, 1500, 3800, 0, "↓2.3k + ↑1.5k tok"},
		{"negative window", 0, 0, 3800, -1, "3.8k tok"},
		{"empty window", 0, 0, 0, 0, "0 tok"},
		{"1M window", 10000, 5000, 15000, 1000000, "↓10k + ↑5.0k tok (2%)"},
	}
	for _, c := range cases {
		if got := FormatUsageContext(c.input, c.output, c.total, c.limit); got != c.want {
			t.Errorf("%s: FormatUsageContext = %q, want %q", c.name, got, c.want)
		}
	}
}

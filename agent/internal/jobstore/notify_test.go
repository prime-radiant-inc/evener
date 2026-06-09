package jobstore

import "testing"

func TestNewTerminalGenerationUnique(t *testing.T) {
	a := NewTerminalGeneration()
	b := NewTerminalGeneration()
	if a == "" || a == b {
		t.Errorf("terminal generations should be non-empty and unique: %q %q", a, b)
	}
}

func TestDedupeKeyComposition(t *testing.T) {
	r := &JobRecord{JobID: "job_A", VisibleToSession: "S1", TerminalGen: "GEN1"}
	k := r.DedupeKey()
	if k != (DedupeKey{VisibleSessionID: "S1", JobID: "job_A", TerminalGen: "GEN1"}) {
		t.Errorf("dedupe key = %+v", k)
	}
}

func TestShouldDeliver(t *testing.T) {
	cases := []struct {
		state NotifyState
		want  bool
	}{
		{NotifyNotArmed, false},
		{NotifyPending, true},
		{NotifyDelivered, false},
	}
	for _, c := range cases {
		r := &JobRecord{NotifyState: c.state}
		if got := ShouldDeliver(r); got != c.want {
			t.Errorf("ShouldDeliver(%q) = %v, want %v", c.state, got, c.want)
		}
	}
}

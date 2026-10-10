package hubapi

import "testing"

func TestAttentionRank(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"errored", 5},
		{"awaiting", 4},
		{"active", 3},
		{"warning", 2},
		{"idle", 1},
		{"ended", 0},
		{"unknown", 0},
	}
	for _, c := range cases {
		if got := AttentionRank(c.in); got != c.want {
			t.Errorf("AttentionRank(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestRollupRank(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"errored", 5},
		{"awaiting", 4},
		{"warning", 3},
		{"active", 2},
		{"idle", 1},
		{"ended", 0},
		{"unknown", 0},
	}
	for _, c := range cases {
		if got := RollupRank(c.in); got != c.want {
			t.Errorf("RollupRank(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAttentionRank_ErroredOutranksAwaiting(t *testing.T) {
	if AttentionRank("errored") <= AttentionRank("awaiting") {
		t.Fatal("errored must outrank awaiting")
	}
	if RollupRank("errored") <= RollupRank("awaiting") {
		t.Fatal("RollupRank: errored must outrank awaiting")
	}
}

func TestStateWord(t *testing.T) {
	cases := []struct {
		state      string
		askPending bool
		want       string
	}{
		{"active", false, "Working"},
		{"awaiting", true, "Question waiting"},
		// Awaiting without a question is a turn that ended on needs_response:
		// the agent can't go on without its human partner (#4093).
		{"awaiting", false, "Needs you"},
		{"warning", false, "Warning"},
		{"warning", true, "Warning"}, // askPending is meaningless outside "awaiting"
		{"errored", false, "Error"},
		{"idle", false, "Idle"},
		{"ended", false, "Ended"},
		{"closed", false, "Ended"},
		{"notLoaded", false, "Not loaded"},
	}
	for _, c := range cases {
		if got := StateWord(c.state, c.askPending); got != c.want {
			t.Errorf("StateWord(%q, %v) = %q, want %q", c.state, c.askPending, got, c.want)
		}
	}
}

func TestAttentionState(t *testing.T) {
	cases := []struct {
		state           string
		approvalPending bool
		want            string
	}{
		// The escalation blocks mid-turn, so the session still reports
		// "active"; its attention is a person's answer, like a question's.
		{"active", true, "awaiting"},
		{"idle", true, "awaiting"},
		{"warning", true, "awaiting"},
		{"restartRequired", true, "awaiting"},
		{"errored", true, "errored"}, // a failure still outranks an approval
		{"active", false, "active"},
		{"warning", false, "warning"},
		{"errored", false, "errored"},
	}
	for _, c := range cases {
		if got := AttentionState(c.state, c.approvalPending); got != c.want {
			t.Errorf("AttentionState(%q, %v) = %q, want %q", c.state, c.approvalPending, got, c.want)
		}
	}
}

func TestNeedsYouBand(t *testing.T) {
	cases := []struct {
		state           string
		askPending      bool
		approvalPending bool
		want            int
	}{
		{"errored", false, false, 2},
		{"errored", true, false, 2}, // errored always outranks ask-pending
		{"errored", false, true, 2}, // ...and a pending approval
		{"awaiting", true, false, 1},
		// An approval blocks mid-turn, so its session still reports "active";
		// it waits on a person the way a question does and shares its band.
		{"active", false, true, 1},
		// A turn that ended on needs_response waits on a person like a
		// question does, and shares its band (#4093).
		{"awaiting", false, false, 1},
		{"warning", false, false, 0},
	}
	for _, c := range cases {
		if got := NeedsYouBand(c.state, c.askPending, c.approvalPending); got != c.want {
			t.Errorf("NeedsYouBand(%q, %v, %v) = %d, want %d", c.state, c.askPending, c.approvalPending, got, c.want)
		}
	}
}

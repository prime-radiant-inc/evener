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
		{"awaiting", false, "Your move"},
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

func TestNeedsYouBand(t *testing.T) {
	cases := []struct {
		state      string
		askPending bool
		want       int
	}{
		{"errored", false, 2},
		{"errored", true, 2}, // errored always outranks ask-pending
		{"awaiting", true, 1},
		{"awaiting", false, 0},
		{"warning", false, 0},
	}
	for _, c := range cases {
		if got := NeedsYouBand(c.state, c.askPending); got != c.want {
			t.Errorf("NeedsYouBand(%q, %v) = %d, want %d", c.state, c.askPending, got, c.want)
		}
	}
}

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

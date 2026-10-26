package hubapi

import (
	"encoding/json"
	"os"
	"testing"
)

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

// The same fixture drives appwire-client's approvalWaiting test, so the hub
// and the clients cannot disagree about which states an approval escalates.
func TestAttentionState(t *testing.T) {
	raw, err := os.ReadFile("../testdata/attention/approval_state.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		State           string `json:"state"`
		ApprovalPending bool   `json:"approval_pending"`
		Want            string `json:"want"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := AttentionState(c.State, c.ApprovalPending); got != c.Want {
			t.Errorf("AttentionState(%q, %v) = %q, want %q", c.State, c.ApprovalPending, got, c.Want)
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
		// A restart outranks the approval, as in AttentionState.
		{"restartRequired", false, true, 0},
	}
	for _, c := range cases {
		if got := NeedsYouBand(c.state, c.askPending, c.approvalPending); got != c.want {
			t.Errorf("NeedsYouBand(%q, %v, %v) = %d, want %d", c.state, c.askPending, c.approvalPending, got, c.want)
		}
	}
}

package appwire

import (
	"encoding/json"
	"testing"
)

// The quiet time is absent when the hub withholds it (a subagent running, or
// the session not working) and present when it is zero: "quiet for 0 ms" and
// "cannot be quiet" are different claims (S5).
func TestSessionActivityOmitsOnlyAWithheldQuietTime(t *testing.T) {
	zero := int64(0)
	for _, tc := range []struct {
		activity SessionActivity
		want     string
	}{
		{SessionActivity{Ref: "local:a", Minutes: []int{0, 1}, RunningSubagents: 2}, `{"ref":"local:a","minutes":[0,1],"runningSubagents":2}`},
		{SessionActivity{Ref: "local:a", Minutes: []int{0, 1}, QuietForMS: &zero}, `{"ref":"local:a","minutes":[0,1],"runningSubagents":0,"quietForMs":0}`},
	} {
		raw, err := json.Marshal(tc.activity)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != tc.want {
			t.Fatalf("marshal = %s, want %s", raw, tc.want)
		}
	}
	raw, err := json.Marshal(ActivityReadParams{})
	if err != nil || string(raw) != `{}` {
		t.Fatalf("empty params = %s (%v), want {}: no refs reads every session", raw, err)
	}
}

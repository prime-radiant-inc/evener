package events

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJobFinishedData_ExhaustionMetadata(t *testing.T) {
	t.Parallel()
	resumable := false
	payload := JobFinishedData{
		JobID:            "job_exhausted",
		JobType:          "delegate",
		Status:           "exhausted",
		ExhaustionBudget: "max_turns",
		ExhaustionLimit:  500,
		Resumable:        &resumable,
	}
	if payload.ExhaustionBudget != "max_turns" || payload.ExhaustionLimit != 500 || payload.Resumable == nil || *payload.Resumable {
		t.Fatalf("typed exhaustion payload = %+v", payload)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal JobFinishedData: %v", err)
	}
	for _, want := range []string{`"status":"exhausted"`, `"exhaustion_budget":"max_turns"`, `"exhaustion_limit":500`, `"resumable":false`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("JobFinishedData JSON %s missing %s", encoded, want)
		}
	}
}

package jobstore

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventJSONRoundTrip(t *testing.T) {
	ts := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	e := Event{
		Kind:        EventJobStarted,
		Seq:         1,
		TS:          ts,
		JobID:       "job_X",
		Type:        JobShell,
		Command:     "make test",
		Description: "run tests",
		StartedAt:   &ts,
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != EventJobStarted || got.JobID != "job_X" || got.Command != "make test" {
		t.Errorf("round trip mismatch: %+v", got)
	}
	// Absent fields must stay absent in the wire form (omitempty).
	if got.Status != "" {
		t.Errorf("status should be empty, got %q", got.Status)
	}
}

func TestEventKindsAreStable(t *testing.T) {
	want := map[EventKind]string{
		EventJobStarted:               "job_started",
		EventJobSessionAssigned:       "job_session_assigned",
		EventJobFinished:              "job_finished",
		EventJobMessageSent:           "job_message_sent",
		EventJobNotificationPending:   "job_notification_pending",
		EventJobNotificationDelivered: "job_notification_delivered",
	}
	for k, s := range want {
		if string(k) != s {
			t.Errorf("event kind %v should serialize as %q", k, s)
		}
	}
}

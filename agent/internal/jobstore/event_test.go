package jobstore

import (
	"bytes"
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
	if got.Type != JobShell {
		t.Errorf("Type: got %q, want %q", got.Type, JobShell)
	}
	if got.Description != "run tests" {
		t.Errorf("Description: got %q, want %q", got.Description, "run tests")
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(ts) {
		t.Errorf("StartedAt: got %v, want %v", got.StartedAt, ts)
	}
	// Absent fields must stay absent in the wire form (omitempty).
	if got.Status != "" {
		t.Errorf("status should be empty, got %q", got.Status)
	}
}

func TestWatchRegistryEventJSONRoundTrip(t *testing.T) {
	raw := []byte(`{"kind":"watch_registered","watch_id":"watch_A","watch":{"generation":"wg_1","owner_session_id":"owner","visible_session_id":"owner","target":"job_1","send_to":"dlg_obs","config_hash":"hash_A"}}`)
	var e Event
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("unmarshal watch event: %v", err)
	}
	if e.Kind != EventWatchRegistered || e.WatchID != "watch_A" || e.Watch == nil || e.Watch.SendTo != "dlg_obs" {
		t.Fatalf("event = %+v, want watch registry payload", e)
	}
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal watch event: %v", err)
	}
	if !bytes.Contains(out, []byte(`"watch_id":"watch_A"`)) || !bytes.Contains(out, []byte(`"send_to":"dlg_obs"`)) {
		t.Fatalf("encoded watch event = %s", out)
	}
}

func TestEventKindsAreStable(t *testing.T) {
	want := map[EventKind]string{
		EventJobStarted:               "job_started",
		EventJobFinished:              "job_finished",
		EventJobMessageSent:           "job_message_sent",
		EventJobNotificationPending:   "job_notification_pending",
		EventJobNotificationDelivered: "job_notification_delivered",
		EventWatchSendPending:         "watch_send_pending",
		EventWatchSendDelivered:       "watch_send_delivered",
		EventWatchSendDropped:         "watch_send_dropped",
		EventWatchSendEvicted:         "watch_send_evicted",
		EventWatchRegistered:          "watch_registered",
		EventWatchCleared:             "watch_cleared",
	}
	for k, s := range want {
		if string(k) != s {
			t.Errorf("event kind %v should serialize as %q", k, s)
		}
	}
}

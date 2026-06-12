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

func TestEventJSONRoundTripDelegateRestoreDescriptorAndStructuredReason(t *testing.T) {
	valid := false
	events := []Event{
		{
			Kind:  EventJobStarted,
			Seq:   1,
			JobID: "job_X",
			Type:  JobDelegate,
			DelegateRestore: &DelegateRestoreDescriptor{
				Version:           1,
				ChildSessionID:    "child_1",
				TranscriptRef:     "transcript_1",
				FrozenSkillBodies: []string{"stored skill body"},
				ResultSchema:      map[string]any{"type": "object"},
			},
		},
		{
			Kind:                   EventJobFinished,
			Seq:                    2,
			JobID:                  "job_X",
			Status:                 StatusCompleted,
			StructuredResultValid:  &valid,
			StructuredResultReason: "schema_result_missing",
		},
	}

	b, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire []map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	restoreWire, ok := wire[0]["delegate_restore"].(map[string]any)
	if !ok {
		t.Fatalf("delegate_restore missing from wire: %+v", wire[0])
	}
	if _, ok := restoreWire["result_schema"]; !ok {
		t.Fatalf("result_schema missing from wire delegate_restore: %+v", restoreWire)
	}
	if bodies, ok := restoreWire["frozen_skill_bodies"].([]any); !ok || len(bodies) != 1 || bodies[0] != "stored skill body" {
		t.Fatalf("frozen_skill_bodies = %#v, want stored body", restoreWire["frozen_skill_bodies"])
	}
	if wire[1]["structured_result_reason"] != "schema_result_missing" {
		t.Fatalf("wire structured_result_reason = %#v", wire[1]["structured_result_reason"])
	}

	var got []Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	restore := got[0].DelegateRestore
	if restore == nil {
		t.Fatalf("delegate_restore missing after round trip: %+v", got[0])
	}
	if restore.Version != 1 || restore.ChildSessionID != "child_1" || restore.TranscriptRef != "transcript_1" {
		t.Fatalf("delegate_restore mismatch: %+v", restore)
	}
	schema, ok := restore.ResultSchema.(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("result_schema = %#v, want object schema", restore.ResultSchema)
	}
	if got[1].StructuredResultReason != "schema_result_missing" {
		t.Fatalf("structured_result_reason = %q", got[1].StructuredResultReason)
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
		EventWatchSendPending:         "watch_send_pending",
		EventWatchSendDelivered:       "watch_send_delivered",
		EventWatchSendDropped:         "watch_send_dropped",
		EventWatchSendEvicted:         "watch_send_evicted",
		EventWatchReadGrant:           "watch_read_grant",
	}
	for k, s := range want {
		if string(k) != s {
			t.Errorf("event kind %v should serialize as %q", k, s)
		}
	}
}

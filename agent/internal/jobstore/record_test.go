package jobstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/identifier"
)

func TestGeneratedJobstoreIDsUseIdentifierDomains(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	tests := []struct {
		name     string
		newID    func() string
		validate func(string) error
		prefix   string
	}{
		{"job", func() string { return NewJobID(owner) }, identifier.ValidateJobID, "job_"},
		{"delegate", NewDelegateID, identifier.ValidateDelegateID, "dlg_"},
		{"delegate generation", NewDelegateGeneration, identifier.ValidateDelegateGeneration, "dg_"},
		{"watch", NewWatchID, identifier.ValidateWatchID, "watch_"},
		{"watch generation", NewWatchGeneration, identifier.ValidateWatchGeneration, "wg_"},
		{"watch delivery", NewWatchSendDeliveryID, identifier.ValidateWatchDeliveryID, "wd_"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.newID()
			if !strings.HasPrefix(got, tt.prefix) {
				t.Fatalf("ID = %q, want prefix %s", got, tt.prefix)
			}
			if err := tt.validate(got); err != nil {
				t.Fatalf("validate %q: %v", got, err)
			}
		})
	}
}

// TestWatchSendState_TimestampsAlwaysShipOnWire locks in that CreatedAt and
// UpdatedAt have no "omitempty" tag: encoding/json can never omit a struct
// value regardless of the tag, so both keys ship even for the zero
// time.Time. WatchSendState is purely internal durable job-store state (no
// external wire consumer decodes for key absence), so the tag was already a
// no-op lie.
func TestWatchSendState_TimestampsAlwaysShipOnWire(t *testing.T) {
	data, err := json.Marshal(WatchSendState{})
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, `"created_at":`) {
		t.Errorf("expected created_at key present even for zero time.Time, got %s", got)
	}
	if !strings.Contains(got, `"updated_at":`) {
		t.Errorf("expected updated_at key present even for zero time.Time, got %s", got)
	}
}

// TestJobRecord_BackgroundAndPhaseStayOffTheWire locks in that Background and
// Phase never appear in a JobRecord's JSON — the live-only contract
// LastActivity already carries. No Event field feeds either one, so a record
// folded from the durable log always reports "foreground, no phase" whatever
// the job actually did; only the runtime's in-memory record knows better.
// Emitting them advertises durable state no fold can reproduce, and a reader
// trusting a folded record's silence reads every job as foreground.
func TestJobRecord_BackgroundAndPhaseStayOffTheWire(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	folded := Fold([]Event{
		ev(EventJobStarted, 1, "job_bg", func(e *Event) {
			e.Type = JobShell
			e.Command = "npm run dev"
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
	})["job_bg"]
	if folded == nil {
		t.Fatal("expected record for job_bg")
	}
	if folded.Background || folded.Phase != "" {
		t.Fatalf("folded record claims background/phase the log never carried: %+v", folded)
	}

	live := *folded
	live.Background = true
	live.Phase = "process_running"
	b, err := json.Marshal(live)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	if _, ok := wire["background"]; ok {
		t.Errorf("background reached the wire: %s", b)
	}
	if _, ok := wire["phase"]; ok {
		t.Errorf("phase reached the wire: %s", b)
	}
	if !live.Background || live.Phase != "process_running" {
		t.Errorf("live record lost its in-memory background/phase: %+v", live)
	}
}

func TestJobRecordJSONRoundTripDelegateRestoreDescriptorAndStructuredReason(t *testing.T) {
	valid := false
	r := JobRecord{
		JobID:  "job_X",
		Type:   JobDelegate,
		Status: StatusCompleted,
		DelegateRestore: &DelegateRestoreDescriptor{
			Version:           1,
			ChildSessionID:    "child_1",
			TranscriptRef:     "transcript_1",
			FrozenSkillBodies: []string{"stored skill body"},
			ResultSchema:      map[string]any{"type": "object"},
		},
		StructuredResultValid:  &valid,
		StructuredResultReason: "schema_result_missing",
	}

	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	restoreWire, ok := wire["delegate_restore"].(map[string]any)
	if !ok {
		t.Fatalf("delegate_restore missing from wire: %+v", wire)
	}
	if _, ok := restoreWire["result_schema"]; !ok {
		t.Fatalf("result_schema missing from wire delegate_restore: %+v", restoreWire)
	}
	if bodies, ok := restoreWire["frozen_skill_bodies"].([]any); !ok || len(bodies) != 1 || bodies[0] != "stored skill body" {
		t.Fatalf("frozen_skill_bodies = %#v, want stored body", restoreWire["frozen_skill_bodies"])
	}
	if wire["structured_result_reason"] != "schema_result_missing" {
		t.Fatalf("wire structured_result_reason = %#v", wire["structured_result_reason"])
	}

	var got JobRecord
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	restore := got.DelegateRestore
	if restore == nil {
		t.Fatalf("delegate_restore missing after round trip: %+v", got)
	}
	if restore.Version != 1 || restore.ChildSessionID != "child_1" || restore.TranscriptRef != "transcript_1" {
		t.Fatalf("delegate_restore mismatch: %+v", restore)
	}
	if len(restore.FrozenSkillBodies) != 1 || restore.FrozenSkillBodies[0] != "stored skill body" {
		t.Fatalf("frozen_skill_bodies = %+v, want stored body", restore.FrozenSkillBodies)
	}
	schema, ok := restore.ResultSchema.(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("result_schema = %#v, want object schema", restore.ResultSchema)
	}
	if got.StructuredResultReason != "schema_result_missing" {
		t.Fatalf("structured_result_reason = %q", got.StructuredResultReason)
	}
}

func TestStatusIsTerminal(t *testing.T) {
	terminal := []Status{StatusCompleted, StatusFailed, StatusCancelled, StatusStopped}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("Status %q should be terminal", s)
		}
	}
	if StatusRunning.IsTerminal() {
		t.Errorf("Status %q should not be terminal", StatusRunning)
	}
}

func TestStatusIsTerminal_Exhausted(t *testing.T) {
	if !StatusExhausted.IsTerminal() {
		t.Fatal("exhausted status is not terminal")
	}
}

func TestNewJobIDFormatAndUniqueness(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	a := NewJobID(owner)
	b := NewJobID(owner)
	if !strings.HasPrefix(a, "job_") {
		t.Errorf("job id %q should start with job_", a)
	}
	if a == b {
		t.Errorf("two job ids should differ: %q == %q", a, b)
	}
	if len(a) != len("job_")+22+1+12 {
		t.Errorf("job id %q has unexpected length %d", a, len(a))
	}
}

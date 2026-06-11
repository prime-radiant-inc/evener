package jobstore

import (
	"encoding/json"
	"strings"
	"testing"
)

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

func TestNewJobIDFormatAndUniqueness(t *testing.T) {
	a := NewJobID()
	b := NewJobID()
	if !strings.HasPrefix(a, "job_") {
		t.Errorf("job id %q should start with job_", a)
	}
	if a == b {
		t.Errorf("two job ids should differ: %q == %q", a, b)
	}
	// "job_" + 26-char ULID
	if len(a) != len("job_")+26 {
		t.Errorf("job id %q has unexpected length %d", a, len(a))
	}
}

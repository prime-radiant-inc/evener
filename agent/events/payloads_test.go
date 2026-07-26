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

// Every kind the UI can label must exist as a constant. This list is the
// contract Task 3's call sites are checked against.
func TestSteeringKindConstants(t *testing.T) {
	want := map[string]string{
		"interrupted":        SteeringKindInterrupted,
		"agent-message":      SteeringKindAgentMessage,
		"hook-context":       SteeringKindHookContext,
		"precompact-hook":    SteeringKindPrecompactHook,
		"compact-nudge":      SteeringKindCompactNudge,
		"image-description":  SteeringKindImageDescription,
		"no-tool-calls":      SteeringKindNoToolCalls,
		"loop-detected":      SteeringKindLoopDetected,
		"tasks-done":         SteeringKindTasksDone,
		"task-nudge":         SteeringKindTaskNudge,
		"task-inactive":      SteeringKindTaskInactive,
		"note-handoff":       SteeringKindNoteHandoff,
		"goal-objective":     SteeringKindGoalObjective,
		"transcript-pointer": SteeringKindTranscriptPointer,
		"current-task":       SteeringKindCurrentTask,
		"task-list":          SteeringKindTaskList,
		"notification":       SteeringKindNotification,
	}
	for literal, got := range want {
		if got != literal {
			t.Errorf("constant for %q = %q, want %q", literal, got, literal)
		}
	}
	if len(AllSteeringKinds) != len(want) {
		t.Errorf("AllSteeringKinds has %d entries, want %d", len(AllSteeringKinds), len(want))
	}
	for _, k := range AllSteeringKinds {
		if _, ok := want[k]; !ok {
			t.Errorf("AllSteeringKinds contains unknown kind %q", k)
		}
	}
}

func TestSteeringInjectedDataCarriesKind(t *testing.T) {
	d := SteeringInjectedData{Text: "x", Kind: SteeringKindTasksDone}
	if d.Kind != "tasks-done" {
		t.Errorf("Kind = %q, want %q", d.Kind, "tasks-done")
	}
}

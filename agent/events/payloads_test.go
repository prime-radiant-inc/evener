package events

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskUpdatedData_CurrentJSON(t *testing.T) {
	withCurrent, err := json.Marshal(TaskUpdatedData{
		TaskStateData: {
			Total:   3,
			Done:    1,
			Current: &TaskSummaryData{ID: 2, Description: "live current task"},
		},
		TaskPublicationEpoch:    11,
		TaskPublicationRevision: 17,
	})
	if err != nil {
		t.Fatalf("marshal TaskUpdatedData with current: %v", err)
	}
	if !strings.Contains(string(withCurrent), `"current":{"id":2,"description":"live current task"}`) {
		t.Fatalf("TaskUpdatedData JSON = %s", withCurrent)
	}
	if strings.Contains(string(withCurrent), "revision") || strings.Contains(string(withCurrent), "epoch") {
		t.Fatalf("TaskUpdatedData JSON leaked internal publication identity: %s", withCurrent)
	}

	withoutCurrent, err := json.Marshal(TaskUpdatedData{TaskStateData: {Total: 3, Done: 1}})
	if err != nil {
		t.Fatalf("marshal TaskUpdatedData without current: %v", err)
	}
	if strings.Contains(string(withoutCurrent), `"current"`) {
		t.Fatalf("TaskUpdatedData without current = %s", withoutCurrent)
	}
}

func TestSessionStartCurrentWorkSeedTriStateJSON(t *testing.T) {
	tests := []struct {
		name string
		data SessionStartData
		want string
	}{
		{name: "unknown", data: SessionStartData{}, want: `{"profile":"","model":""}`},
		{
			name: "explicit clear",
			data: SessionStartData{CurrentWork: &CurrentWorkSeedData{}},
			want: `{"profile":"","model":"","current_work":{"goal":null}}`,
		},
		{
			name: "goal",
			data: SessionStartData{CurrentWork: &CurrentWorkSeedData{Goal: &GoalStateData{
				Objective:  "ship focus sentence",
				Status:     "active",
				Iterations: 2,
			}}},
			want: `{"profile":"","model":"","current_work":{"goal":{"objective":"ship focus sentence","status":"active","iterations":2}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(encoded); got != tt.want {
				t.Fatalf("SessionStartData JSON = %s, want %s", got, tt.want)
			}
			var roundTrip SessionStartData
			if err := json.Unmarshal(encoded, &roundTrip); err != nil {
				t.Fatal(err)
			}
			switch tt.name {
			case "unknown":
				if roundTrip.CurrentWork != nil {
					t.Fatalf("CurrentWork = %+v, want nil unknown seed", roundTrip.CurrentWork)
				}
			case "explicit clear":
				if roundTrip.CurrentWork == nil || roundTrip.CurrentWork.Goal != nil {
					t.Fatalf("CurrentWork = %+v, want present seed with nil Goal", roundTrip.CurrentWork)
				}
			default:
				if roundTrip.CurrentWork == nil || roundTrip.CurrentWork.Goal == nil || roundTrip.CurrentWork.Goal.Objective != "ship focus sentence" {
					t.Fatalf("CurrentWork = %+v, want structured goal", roundTrip.CurrentWork)
				}
			}
		})
	}
}

func TestSessionStartInternalTaskRevisionDoesNotLeakToJSON(t *testing.T) {
	encoded, err := json.Marshal(SessionStartData{
		Profile:                 "openai",
		Model:                   "gpt-5",
		TaskPublicationEpoch:    19,
		TaskPublicationRevision: 23,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "revision") || strings.Contains(string(encoded), "epoch") {
		t.Fatalf("SessionStartData JSON leaked internal publication identity: %s", encoded)
	}
}

func TestSessionStartCurrentWorkSeedCarriesAuthoritativeEmptyTasks(t *testing.T) {
	data := SessionStartData{CurrentWork: &CurrentWorkSeedData{
		Tasks: &TaskStateData{},
	}}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"profile":"","model":"","current_work":{"tasks":{"total":0,"done":0},"goal":null}}`; got != want {
		t.Fatalf("SessionStartData JSON = %s, want %s", got, want)
	}
	var roundTrip SessionStartData
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.CurrentWork == nil || roundTrip.CurrentWork.Tasks == nil {
		t.Fatalf("round trip lost authoritative empty task state: %+v", roundTrip.CurrentWork)
	}
}

func TestTaskUpdatedDataCarriesTaskStoreOwnerSessionID(t *testing.T) {
	want := TaskUpdatedData{
		TaskStateData: {
			Total:   1,
			Current: &TaskSummaryData{ID: 4, Description: "current"},
		},
		TaskStoreOwnerSessionID: "root-session",
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var typed TaskUpdatedData
	if err := json.Unmarshal(encoded, &typed); err != nil {
		t.Fatal(err)
	}
	if typed.TaskStoreOwnerSessionID != want.TaskStoreOwnerSessionID {
		t.Fatalf("typed owner = %q, want %q", typed.TaskStoreOwnerSessionID, want.TaskStoreOwnerSessionID)
	}
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatal(err)
	}
	if got := generic["task_store_owner_session_id"]; got != want.TaskStoreOwnerSessionID {
		t.Fatalf("generic owner = %#v, want %q", got, want.TaskStoreOwnerSessionID)
	}
}

func TestGoalUpdatedDataJSONPreservesExplicitClear(t *testing.T) {
	set, err := json.Marshal(GoalUpdatedData{Goal: &GoalStateData{
		Objective:  "ship focus sentence",
		Status:     "active",
		Iterations: 1,
	}})
	if err != nil {
		t.Fatalf("marshal GoalUpdatedData with goal: %v", err)
	}
	if got, want := string(set), `{"goal":{"objective":"ship focus sentence","status":"active","iterations":1}}`; got != want {
		t.Fatalf("GoalUpdatedData JSON = %s, want %s", got, want)
	}

	cleared, err := json.Marshal(GoalUpdatedData{Goal: nil})
	if err != nil {
		t.Fatalf("marshal GoalUpdatedData clear: %v", err)
	}
	if got, want := string(cleared), `{"goal":null}`; got != want {
		t.Fatalf("GoalUpdatedData clear JSON = %s, want %s", got, want)
	}
}

func TestJobFinishedData_ExhaustionMetadata(t *testing.T) {
	t.Parallel()
	resumable := false
	payload := JobFinishedData{
		JobID:            "job_exhausted",
		JobType:          "shell",
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

func TestDelegateUpdatedDataJSONRoundTrip(t *testing.T) {
	valid := true
	exhaustionResumable := false
	runningForMS := int64(1200)
	in := DelegateUpdatedData{
		DelegateID: "dlg_1", OwnerSessionID: "owner", RootSessionID: "root", ChildSessionID: "child",
		TranscriptRef: "local:child", ParentDelegateID: "dlg_parent", Type: "delegate", Lifecycle: "idle", Phase: "idle", Status: "idle",
		Resumable: true, NeedsAttention: true, ProjectionRevision: 9, Message: json.RawMessage("null"), StructuredResult: json.RawMessage("null"),
		StructuredValid: &valid, StructuredReason: "valid null", ExhaustionBudget: "max_tool_rounds_per_input", ExhaustionLimit: 4,
		ExhaustionResumable: &exhaustionResumable, RunningForMS: &runningForMS, Warnings: []string{"warning"}, Diagnostics: []string{"diagnostic"},
		Usage:    &DelegateUsageData{InputTokens: 3, OutputTokens: 2, CacheReadTokens: 1, TotalTokens: 5},
		Worktree: &DelegateWorktreeData{Path: "/tmp/lane", Branch: "delegate/lane", HeadSHA: "abc", Ahead: 2, Dirty: true},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal DelegateUpdatedData: %v", err)
	}
	for _, want := range []string{`"message":null`, `"structured_result":null`, `"needs_attention":true`, `"projection_revision":9`, `"parent_delegate_id":"dlg_parent"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("DelegateUpdatedData JSON %s missing %s", raw, want)
		}
	}
	if strings.Contains(string(raw), "wait_ignored_reason") {
		t.Fatalf("stable delegate payload leaked call-scoped wait result: %s", raw)
	}
	var out DelegateUpdatedData
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal DelegateUpdatedData: %v", err)
	}
	if out.DelegateID != in.DelegateID || !out.NeedsAttention || out.ProjectionRevision != in.ProjectionRevision || string(out.Message) != "null" || string(out.StructuredResult) != "null" ||
		out.StructuredValid == nil || !*out.StructuredValid || out.ExhaustionResumable == nil || *out.ExhaustionResumable || out.RunningForMS == nil || *out.RunningForMS != runningForMS ||
		out.Usage == nil || out.Usage.TotalTokens != 5 || out.Worktree == nil || !out.Worktree.Dirty || len(out.Warnings) != 1 || len(out.Diagnostics) != 1 {
		t.Fatalf("DelegateUpdatedData round trip = %+v", out)
	}
}

func TestToolCallEndDataMarshalsOutputRef(t *testing.T) {
	encoded, err := json.Marshal(ToolCallEndData{OutputRef: "artifact:abc"})
	if err != nil {
		t.Fatalf("marshal ToolCallEndData: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal ToolCallEndData: %v", err)
	}
	if got := payload["output_ref"]; got != "artifact:abc" {
		t.Fatalf("output_ref = %#v, want artifact:abc (JSON: %s)", got, encoded)
	}

	empty, err := json.Marshal(ToolCallEndData{})
	if err != nil {
		t.Fatalf("marshal empty ToolCallEndData: %v", err)
	}
	if strings.Contains(string(empty), "output_ref") {
		t.Fatalf("empty OutputRef was not omitted: %s", empty)
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
		"provider-failure":   SteeringKindProviderFailure,
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

// TestTurnStartedDataBindsItsKind pins the payload-to-kind binding that
// events.New derives Kind from. A notification turn announces itself through
// this event and nothing else, so a mismatched binding routes the one thing
// that makes such a turn addressable as some other event entirely.
func TestTurnStartedDataBindsItsKind(t *testing.T) {
	if got := (TurnStartedData{}).eventKind(); got != EventTurnStarted {
		t.Fatalf("TurnStartedData.eventKind() = %q, want %q", got, EventTurnStarted)
	}
}

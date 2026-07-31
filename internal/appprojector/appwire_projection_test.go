package appprojector

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

func TestAppEventProjectorProjectsAssistantDelta(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{Model: "gpt-5"}})
	out := projector.Project(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "hi"}})

	if len(out) != 1 {
		t.Fatalf("notifications=%+v", out)
	}
	if out[0].Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("method=%q", out[0].Method)
	}
	params, ok := out[0].Params.(appwire.AgentMessageDeltaParams)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params.ThreadID != "th_1" || params.Ref != "local:th_1" || params.TurnID == "" || params.ItemID == "" || params.Delta != "hi" {
		t.Fatalf("params=%+v", params)
	}
}

func TestAppEventProjectorCarriesUserInputTranscriptEntryIndex(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello", Turn: 3}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.TranscriptEntryIndex != 3 {
		t.Fatalf("transcript entry index=%d, want 3", item.TranscriptEntryIndex)
	}
}

func TestProject_ModelChanged(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventModelChanged,
		Data: events.ModelChangedData{
			OldProvider:           "openai",
			OldModel:              "gpt-5.4",
			NewProvider:           "anthropic",
			NewModel:              "claude-opus-4-6",
			ReasoningEffortLevels: []string{"low", "high"},
			SupportsReasoning:     true,
			MarkerText:            "Switched model: openai/gpt-5.4 → anthropic/claude-opus-4-6",
		},
	})
	if len(out) != 2 {
		t.Fatalf("want thread/model/changed + systemMessage notifications, got %+v", out)
	}
	if out[0].Method != appwire.NotifyThreadModelChanged {
		t.Fatalf("out[0].Method = %q, want thread/model/changed", out[0].Method)
	}
	params, ok := out[0].Params.(appwire.ThreadModelChangedParams)
	if !ok {
		t.Fatalf("params type = %T, want appwire.ThreadModelChangedParams", out[0].Params)
	}
	if params.ThreadID != "th1" || params.Ref != "local:th1" {
		t.Fatalf("params missing threadId/ref: %+v", params)
	}
	if params.ModelProvider != "anthropic" || params.Model != "claude-opus-4-6" {
		t.Fatalf("params modelProvider/model = %s/%s, want anthropic/claude-opus-4-6", params.ModelProvider, params.Model)
	}
	if !params.SupportsReasoning || len(params.ReasoningEffortLevels) != 2 {
		t.Fatalf("params = %+v, want SupportsReasoning=true and 2 effort levels", params)
	}

	// (b) the live-only echo of the persisted switch marker: a systemMessage
	// item carrying the exact MarkerText SetModel wrote to the transcript.
	// No turn is active in this projector, so systemAnnouncement wraps the
	// item in a synthetic turn/completed notification (same as any other
	// systemAnnouncement fired outside a turn, e.g. context compaction).
	turn := notificationTurn(t, out[1:], appwire.NotifyTurnCompleted)
	if len(turn.Items) != 1 || turn.Items[0].Type != "systemMessage" {
		t.Fatalf("marker turn items = %+v, want one systemMessage item", turn.Items)
	}
	if turn.Items[0].Text != "Switched model: openai/gpt-5.4 → anthropic/claude-opus-4-6" {
		t.Fatalf("marker item Text = %q", turn.Items[0].Text)
	}
	if turn.Items[0].EventKind != appwire.ThreadItemEventKindModelSwitch {
		t.Fatalf("item eventKind=%q, want %q", turn.Items[0].EventKind, appwire.ThreadItemEventKindModelSwitch)
	}
}

func TestProject_ReasoningEffortChanged(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventReasoningEffortChanged,
		Data: events.ReasoningEffortChangedData{ReasoningEffort: "high"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyThreadReasoningEffortChanged {
		t.Fatalf("want one thread/reasoning-effort/changed notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.ThreadReasoningEffortChangedParams)
	if !ok {
		t.Fatalf("params type = %T, want appwire.ThreadReasoningEffortChangedParams", out[0].Params)
	}
	if params.ThreadID != "th1" || params.Ref != "local:th1" || params.ReasoningEffort != "high" {
		t.Fatalf("params = %+v", params)
	}
}

// TestProject_ModelThenEffortNotificationOrdering pins the client-facing
// contract for a switch that also clamps effort: the model-changed
// notification (which carries the new reasoning-effort ladder) is delivered
// before the reasoning-effort-changed notification, so a client re-derives the
// ladder before applying the new effort value. The switch marker systemMessage
// rides between them, immediately after model-changed.
func TestProject_ModelThenEffortNotificationOrdering(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	var out []AppNotification
	out = append(out, p.Project(events.SessionEvent{
		Kind: events.EventModelChanged,
		Data: events.ModelChangedData{
			OldProvider:           "anthropic",
			OldModel:              "claude-opus-4-6",
			NewProvider:           "openai",
			NewModel:              "gpt-5.5",
			ReasoningEffortLevels: []string{"low", "high"},
			SupportsReasoning:     true,
			MarkerText:            "Switched model: anthropic/claude-opus-4-6 → openai/gpt-5.5",
		},
	})...)
	out = append(out, p.Project(events.SessionEvent{
		Kind: events.EventReasoningEffortChanged,
		Data: events.ReasoningEffortChangedData{ReasoningEffort: "high"},
	})...)

	if got := len(out); got != 3 {
		t.Fatalf("want model-changed + marker + effort-changed, got %d: %+v", got, out)
	}
	if out[0].Method != appwire.NotifyThreadModelChanged {
		t.Fatalf("out[0].Method = %q, want thread/model/changed first", out[0].Method)
	}
	if out[len(out)-1].Method != appwire.NotifyThreadReasoningEffortChanged {
		t.Fatalf("out[last].Method = %q, want thread/reasoning-effort/changed last", out[len(out)-1].Method)
	}
}

func TestProject_TaskUpdated(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventTaskUpdated,
		Data: events.TaskUpdatedData{Total: 3, Done: 1},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfTaskUpdated {
		t.Fatalf("want one serf/task/updated notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.TaskUpdatedParams)
	if !ok || params.Total != 3 || params.Done != 1 {
		t.Fatalf("params = %+v, want Total=3 Done=1", out[0].Params)
	}
}

func TestProject_JobStartedUpdated(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventJobStarted,
		Data: events.JobStartedData{JobID: "job_1", JobType: "shell", Status: "running"},
	})
	// The job lifecycle case emits the full serf/job/started notification plus
	// the lightweight serf/job/updated the webui jobs panel reducer consumes.
	if len(out) != 2 || out[1].Method != appwire.NotifySerfJobUpdated {
		t.Fatalf("want serf/job/updated after serf/job/started, got %+v", out)
	}
	params, ok := out[1].Params.(appwire.JobUpdatedParams)
	if !ok {
		t.Fatalf("params type = %T, want appwire.JobUpdatedParams", out[1].Params)
	}
	if params.ThreadID != "th1" || params.Ref != "local:th1" || params.JobID != "job_1" || params.Status != "running" {
		t.Fatalf("params = %+v", params)
	}
}

func TestProject_JobFinishedUpdated(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventJobFinished,
		Data: events.JobFinishedData{JobID: "job_1", JobType: "shell", Status: "completed"},
	})
	if len(out) != 2 || out[1].Method != appwire.NotifySerfJobUpdated {
		t.Fatalf("want serf/job/updated after serf/job/finished, got %+v", out)
	}
	params, ok := out[1].Params.(appwire.JobUpdatedParams)
	if !ok {
		t.Fatalf("params type = %T, want appwire.JobUpdatedParams", out[1].Params)
	}
	if params.ThreadID != "th1" || params.Ref != "local:th1" || params.JobID != "job_1" || params.Status != "completed" {
		t.Fatalf("params = %+v", params)
	}
}

func TestProject_SandboxEscalationRequested(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventSandboxEscalationRequested,
		Data: events.SandboxEscalationRequestedData{
			EscalationID: "esc_1",
			Mode:         "read-only",
			Tool:         "write_file",
			Kind:         "file_tool",
			DeniedPath:   "/etc/hosts", // full path for informed consent (set at the session)
		},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfSandboxEscalationRequested {
		t.Fatalf("want one serf/sandbox/escalation/requested notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.SandboxEscalationRequested)
	if !ok {
		t.Fatalf("params type = %T, want appwire.SandboxEscalationRequested", out[0].Params)
	}
	if params.EscalationID != "esc_1" || params.Kind != "file_tool" || params.DeniedPath != "/etc/hosts" {
		t.Fatalf("params = %+v", params)
	}
	// The notification must carry its session ref/threadId so a client can route it
	// by session (answer the right one, enqueue a non-viewed one).
	if params.ThreadID != "th1" || params.Ref != "local:th1" {
		t.Fatalf("escalation notification must carry threadId/ref, got %+v", params)
	}
}

// TestProject_SandboxEscalationResolved (wire-honesty spec Part B) mirrors
// TestProject_SandboxEscalationRequested above for the pair notification: the
// projector maps EventSandboxEscalationResolved to
// serf/sandbox/escalation/resolved, carrying only threadId/ref/escalationId
// (no reason or approved — a review decision the spec's doc comments explain).
func TestProject_SandboxEscalationResolved(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventSandboxEscalationResolved,
		Data: events.SandboxEscalationResolvedData{EscalationID: "esc_1"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfSandboxEscalationResolved {
		t.Fatalf("want one serf/sandbox/escalation/resolved notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.SandboxEscalationResolved)
	if !ok {
		t.Fatalf("params type = %T, want appwire.SandboxEscalationResolved", out[0].Params)
	}
	if params.EscalationID != "esc_1" {
		t.Fatalf("params = %+v", params)
	}
	// Same session-routing requirement as the requested notification.
	if params.ThreadID != "th1" || params.Ref != "local:th1" {
		t.Fatalf("resolved notification must carry threadId/ref, got %+v", params)
	}
}

// TestProject_SandboxEscalationNotInTranscript covers both directions of the
// M7 escalation pair (requested and resolved, wire-honesty spec Part B). Both
// ride the event stream only and are never a transcript turn: the projector
// emits exactly its own notification and touches no turn/item state, so the
// model can neither observe nor replay either one. This also stands in for
// the spec's demoted unknown-notification-tolerance check: an older client
// that does not yet recognize serf/sandbox/escalation/resolved sees no
// item/turn notification riding alongside it to react to badly — it drops the
// unrecognized notification exactly like any other, the established norm in
// the TUI and legacy web.
func TestProject_SandboxEscalationNotInTranscript(t *testing.T) {
	cases := []events.SessionEvent{
		{Kind: events.EventSandboxEscalationRequested, Data: events.SandboxEscalationRequestedData{EscalationID: "esc_1", Mode: "read-only", Tool: "write_file", Kind: "file_tool", DeniedPath: "hosts"}},
		{Kind: events.EventSandboxEscalationResolved, Data: events.SandboxEscalationResolvedData{EscalationID: "esc_1"}},
	}
	for _, ev := range cases {
		p := NewAppEventProjector("th1", "local:th1")
		out := p.Project(ev)
		for _, n := range out {
			switch n.Method {
			case appwire.NotifyItemStarted, appwire.NotifyItemCompleted, appwire.NotifyTurnStarted, appwire.NotifyTurnCompleted:
				t.Fatalf("%s must not project a turn/item (transcript) notification, got %s", ev.Kind, n.Method)
			}
		}
	}
}

func TestAppEventProjectorTurnStartedCarriesStartedAt(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	ts := time.Unix(1_700_000_000, 0).UTC()
	out := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Timestamp: ts, Data: events.UserInputData{Text: "hello"}})

	started := notificationParamsJSON(t, out, appwire.NotifyTurnStarted)
	var params struct {
		Turn appwire.Turn `json:"turn"`
	}
	if err := json.Unmarshal(started, &params); err != nil {
		t.Fatalf("turn/started json: %v", err)
	}
	if params.Turn.StartedAt == nil {
		t.Fatalf("turn/started StartedAt is nil, want %d", ts.UnixMilli())
	}
	if *params.Turn.StartedAt != ts.UnixMilli() {
		t.Fatalf("turn/started StartedAt=%d, want %d", *params.Turn.StartedAt, ts.UnixMilli())
	}
}

// TestAppEventProjectorTurnStartedZeroTimestampOmitsStartedAt (kata F1) verifies
// that when EventUserInput carries a zero Timestamp, startedTurn leaves
// StartedAt nil rather than emitting the Unix epoch (1970-01-01).
func TestAppEventProjectorTurnStartedZeroTimestampOmitsStartedAt(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	// Timestamp zero value — must NOT produce StartedAt on the wire.
	out := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	started := notificationParamsJSON(t, out, appwire.NotifyTurnStarted)
	var params struct {
		Turn appwire.Turn `json:"turn"`
	}
	if err := json.Unmarshal(started, &params); err != nil {
		t.Fatalf("turn/started json: %v", err)
	}
	if params.Turn.StartedAt != nil {
		t.Fatalf("turn/started StartedAt=%v, want nil for zero Timestamp", *params.Turn.StartedAt)
	}
}

func TestAppEventProjectorProjectsReasoningDelta(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	out := projector.Project(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_1", Data: events.ReasoningSummaryDeltaData{Delta: "thinking..."}})

	var delta *appwire.ReasoningSummaryDeltaParams
	var startedReasoning bool
	for _, n := range out {
		switch n.Method {
		case appwire.NotifyReasoningSummaryDelta:
			p := n.Params.(appwire.ReasoningSummaryDeltaParams)
			delta = &p
		case appwire.NotifyItemStarted:
			item := notificationThreadItem(t, []AppNotification{n}, appwire.NotifyItemStarted)
			if item.Type == "reasoning" {
				startedReasoning = true
			}
		}
	}
	if !startedReasoning {
		t.Fatalf("expected a reasoning item/started, got %+v", out)
	}
	if delta == nil {
		t.Fatalf("no reasoning delta notification: %+v", out)
		return
	}
	if delta.ThreadID != "th_1" || delta.Ref != "local:th_1" || delta.TurnID == "" || delta.ItemID == "" || delta.Delta != "thinking..." {
		t.Fatalf("reasoning delta params=%+v", *delta)
	}
}

func TestAppEventProjectorJSONUsesCodexLifecycleShape(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	started := notificationParamsJSON(t, out, appwire.NotifyTurnStarted)
	var turnStarted struct {
		ThreadID string       `json:"threadId"`
		Ref      string       `json:"ref"`
		Turn     appwire.Turn `json:"turn"`
	}
	if err := json.Unmarshal(started, &turnStarted); err != nil {
		t.Fatalf("turn/started json: %v", err)
	}
	if turnStarted.ThreadID != "th_1" || turnStarted.Ref != "local:th_1" || turnStarted.Turn.Status != appwire.TurnStatusInProgress {
		t.Fatalf("turn/started=%s", started)
	}

	completed := notificationParamsJSON(t, out, appwire.NotifyItemCompleted)
	var itemCompleted struct {
		ThreadID string `json:"threadId"`
		Ref      string `json:"ref"`
		TurnID   string `json:"turnId"`
		Item     struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			Status string `json:"status"`
		} `json:"item"`
	}
	if err := json.Unmarshal(completed, &itemCompleted); err != nil {
		t.Fatalf("item/completed json: %v", err)
	}
	if itemCompleted.ThreadID != "th_1" || itemCompleted.Ref != "local:th_1" || itemCompleted.Item.Type != "userMessage" || itemCompleted.Item.Text != "hello" || itemCompleted.Item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("item/completed=%s", completed)
	}
}

func TestAppEventProjectorCarriesUserInputImages(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data: events.UserInputData{
			Text: "",
			Images: []events.UserInputImage{{
				MediaType: "image/png",
				Data:      []byte("png"),
				Name:      "shot.png",
			}},
		},
	})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if len(item.Images) != 1 {
		t.Fatalf("images=%+v, want one image", item.Images)
	}
	if item.Images[0].Type != "image" || item.Images[0].MediaType != "image/png" || string(item.Images[0].Data) != "png" || item.Images[0].Name != "shot.png" {
		t.Fatalf("image item=%+v", item.Images[0])
	}
}

func TestAppEventProjectorCarriesToolCallOutputImages(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName: "shell",
		CallID:   "call_img",
	}})
	end := events.New(events.ToolCallEndData{
		ToolName: "shell",
		CallID:   "call_img",
		Output:   "wrote out.png",
		OutputImages: []events.OutputImage{{
			Source: "shell-path", Name: "out.png", MediaType: "image/png", URL: "/doc/image?session=01&path=out.png", Path: "out.png",
		}},
	})
	notes := p.Project(end)
	item := notificationThreadItem(t, notes, appwire.NotifyItemCompleted)
	if len(item.OutputImages) != 1 || item.OutputImages[0].Name != "out.png" {
		t.Fatalf("OutputImages=%+v", item.OutputImages)
	}
}

func TestAppEventProjectorCompletesActiveTurnBeforeQueuedUserInput(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	first := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "first"}})
	firstTurnID := notificationTurnID(t, first, appwire.NotifyTurnStarted)

	second := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "second"}})
	if len(second) < 2 {
		t.Fatalf("second notifications=%+v", second)
	}
	if second[0].Method != appwire.NotifyTurnCompleted {
		t.Fatalf("first notification=%q, want turn/completed (notifications=%+v)", second[0].Method, second)
	}
	completed := notificationTurn(t, second, appwire.NotifyTurnCompleted)
	if completed.ID != firstTurnID || completed.Status != appwire.TurnStatusCompleted {
		t.Fatalf("completed turn=%+v, want id=%q completed", completed, firstTurnID)
	}
	started := notificationTurn(t, second, appwire.NotifyTurnStarted)
	if started.ID == "" || started.ID == firstTurnID {
		t.Fatalf("queued user input did not start a fresh turn: %+v", started)
	}
	item := notificationThreadItem(t, second, appwire.NotifyItemCompleted)
	if item.TurnID != started.ID || item.Text != "second" {
		t.Fatalf("queued user item=%+v, want turn=%q text=second", item, started.ID)
	}
}

func TestAppEventProjectorGoalContinuationOpensNonUserTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventGoalContinuation, SessionID: "th_1", Data: events.GoalContinuationData{Text: "continue toward the goal"}})

	started := notificationTurn(t, out, appwire.NotifyTurnStarted)
	if started.ID == "" || started.Status != appwire.TurnStatusInProgress {
		t.Fatalf("turn/started=%+v, want a fresh in-progress turn", started)
	}
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type == "userMessage" {
		t.Fatalf("goal continuation rendered a userMessage; continuations must not look like the user spoke (item=%+v)", item)
	}
	if item.Type != "systemMessage" {
		t.Fatalf("goal continuation item type=%q, want systemMessage", item.Type)
	}
	if item.TurnID != started.ID || item.Text != "continue toward the goal" {
		t.Fatalf("goal continuation item=%+v, want turn=%q text=%q", item, started.ID, "continue toward the goal")
	}
}

func TestAppEventProjectorGoalContinuationCompletesActivePriorTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	first := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "do the thing"}})
	firstTurnID := notificationTurnID(t, first, appwire.NotifyTurnStarted)

	out := projector.Project(events.SessionEvent{Kind: events.EventGoalContinuation, SessionID: "th_1", Data: events.GoalContinuationData{Text: "keep going"}})
	if len(out) < 2 || out[0].Method != appwire.NotifyTurnCompleted {
		t.Fatalf("first notification=%+v, want turn/completed to close the prior turn", out)
	}
	completed := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if completed.ID != firstTurnID || completed.Status != appwire.TurnStatusCompleted {
		t.Fatalf("completed turn=%+v, want id=%q completed", completed, firstTurnID)
	}
	started := notificationTurn(t, out, appwire.NotifyTurnStarted)
	if started.ID == "" || started.ID == firstTurnID {
		t.Fatalf("goal continuation did not start a fresh turn: %+v", started)
	}
}

func TestAppEventProjectorGoalEndedRendersSystemAnnouncement(t *testing.T) {
	tests := []struct {
		name     string
		data     events.GoalEndedData
		contains string
	}{
		{
			name:     "achieved",
			data:     events.GoalEndedData{Status: "complete", Iterations: 4},
			contains: "✓ Goal achieved",
		},
		{
			name:     "blocked with reason",
			data:     events.GoalEndedData{Status: "blocked", Reason: "no progress", Iterations: 3},
			contains: "⊘ Goal blocked: no progress",
		},
		{
			name:     "blocked without reason",
			data:     events.GoalEndedData{Status: "blocked", Iterations: 2},
			contains: "⊘ Goal blocked",
		},
		{
			name:     "other terminal status stopped",
			data:     events.GoalEndedData{Status: "weird", Iterations: 1},
			contains: "⊘ Goal stopped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projector := NewAppEventProjector("th_1", "local:th_1")
			out := projector.Project(events.SessionEvent{Kind: events.EventGoalEnded, SessionID: "th_1", Data: tt.data})

			if len(out) != 1 || out[0].Method != appwire.NotifyTurnCompleted {
				t.Fatalf("notifications=%+v, want a single systemAnnouncement turn", out)
			}
			turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
			if turn.Status != appwire.TurnStatusCompleted || turn.ItemsView != "full" || len(turn.Items) != 1 {
				t.Fatalf("turn=%+v", turn)
			}
			item := turn.Items[0]
			if item.Type != "systemMessage" || item.Description != "Goal" || item.Status != appwire.TurnStatusCompleted {
				t.Fatalf("item=%+v", item)
			}
			if !strings.Contains(item.Text, tt.contains) {
				t.Fatalf("item text %q does not contain %q", item.Text, tt.contains)
			}
		})
	}
}

func TestAppEventProjectorProjectsThreadLifecycle(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "th_1",
		Data:      events.SessionStartData{Profile: "openai", Model: "gpt-5"},
	})

	thread := notificationThread(t, started, appwire.NotifyThreadStarted)
	if thread.ID != "th_1" || thread.SessionID != "th_1" || thread.Serf.Ref != "local:th_1" {
		t.Fatalf("started thread identity=%+v", thread)
	}
	if thread.Serf.Profile != "openai" || thread.ModelProvider != "gpt-5" {
		t.Fatalf("started thread model/profile=%+v", thread)
	}
	if status := notificationThreadStatus(t, started, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("started status=%+v, want idle", status)
	}

	closed := projector.Project(events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "th_1",
		Data:      events.SessionEndData{Reason: "done", State: "closed"},
	})
	if !hasAppNotification(closed, appwire.NotifyThreadClosed) {
		t.Fatalf("closed lifecycle missing thread/closed: %+v", closed)
	}
	if status := notificationThreadStatus(t, closed, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusClosed {
		t.Fatalf("closed status=%+v, want closed", status)
	}
}

// TestAppEventProjectorRestoredSessionStartCarriesAwaitingState covers spec
// §5.4's "two touchpoints": a SessionStart event whose payload carries the
// restored session's re-derived state (agent/session_init.go's tail scan,
// spec §6) projects ThreadStatusAwaiting on both the initial Thread.Status and
// the threadStatus notification, instead of the old hardcoded idle.
// TestAppEventProjectorProjectsThreadLifecycle above is the paired negative:
// a SessionStart with no State set still projects idle.
func TestAppEventProjectorRestoredSessionStartCarriesAwaitingState(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "th_1",
		Data:      events.SessionStartData{Profile: "openai", Model: "gpt-5", Restored: true, State: appwire.ThreadStatusAwaiting},
	})

	thread := notificationThread(t, started, appwire.NotifyThreadStarted)
	if thread.Status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("restored SessionStart thread status=%+v, want awaiting", thread.Status)
	}
	if status := notificationThreadStatus(t, started, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("restored SessionStart status notification=%+v, want awaiting", status)
	}
}

// TestAppEventProjectorSeedsTurnCounterFromRestoredTranscript (kata eptj)
// verifies that a resumed session's SessionStart event — which carries the
// count of entries already persisted in the transcript at resume time — seeds
// the live turn counter above that count. Without the seed, nextTurn starts at
// its zero value regardless of resume, so the first turn started live mints
// "turn_1" and collides with the id internal/apptranscript's reload path
// (numbering by entry index) already assigned to the resumed session's first
// persisted entry.
func TestAppEventProjectorSeedsTurnCounterFromRestoredTranscript(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "th_1",
		Data:      events.SessionStartData{Profile: "openai", Model: "gpt-5", Restored: true, TranscriptEntries: 4},
	})

	out := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	started := notificationParamsJSON(t, out, appwire.NotifyTurnStarted)
	var params struct {
		Turn appwire.Turn `json:"turn"`
	}
	if err := json.Unmarshal(started, &params); err != nil {
		t.Fatalf("turn/started json: %v", err)
	}
	switch params.Turn.ID {
	case "turn_1", "turn_2", "turn_3", "turn_4":
		t.Fatalf("live turn id=%q collides with a reload-path id already assigned to one of the 4 persisted entries", params.Turn.ID)
	}
	if params.Turn.ID != "turn_5" {
		t.Fatalf("live turn id=%q, want turn_5 (first id above the 4 already-persisted entries)", params.Turn.ID)
	}
}

func TestAppEventProjectorCompletesTurnOnSessionEnd(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	assistantEnd := projector.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "hi"}})
	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})

	if len(started) == 0 || started[0].Method != appwire.NotifyTurnStarted {
		t.Fatalf("started=%+v", started)
	}
	if hasAppNotification(assistantEnd, appwire.NotifyTurnCompleted) {
		t.Fatalf("assistant end completed turn early: %+v", assistantEnd)
	}
	if !hasAppNotification(sessionEnd, appwire.NotifyTurnCompleted) {
		t.Fatalf("session end did not complete turn: %+v", sessionEnd)
	}
	completedTurn := notificationTurn(t, sessionEnd, appwire.NotifyTurnCompleted)
	if completedTurn.Status != appwire.TurnStatusCompleted {
		t.Fatalf("idle session end turn status=%s, want completed", completedTurn.Status)
	}
	if !hasAppNotification(sessionEnd, appwire.NotifyThreadStatusChanged) {
		t.Fatalf("session end did not update thread status: %+v", sessionEnd)
	}
}

func TestAppEventProjectorMapsAwaitingSessionEnd(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{
		Reason: "input_complete",
		State:  "awaiting",
	}})

	if hasAppNotification(sessionEnd, appwire.NotifyThreadClosed) {
		t.Fatalf("awaiting SessionEnd emitted thread/closed: %+v", sessionEnd)
	}
	if status := notificationThreadStatus(t, sessionEnd, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("awaiting status=%+v, want awaiting", status)
	}
	for _, n := range sessionEnd {
		if n.Method != appwire.NotifyTurnCompleted {
			continue
		}
		params, ok := n.Params.(map[string]any)
		if !ok {
			t.Fatalf("turnCompleted params=%T", n.Params)
		}
		turn, ok := params["turn"].(appwire.Turn)
		if !ok {
			t.Fatalf("turn=%T", params["turn"])
		}
		if turn.Status != appwire.TurnStatusCompleted {
			t.Fatalf("awaiting turn status=%s, want completed", turn.Status)
		}
		return
	}
	t.Fatalf("awaiting SessionEnd missing turn/completed: %+v", sessionEnd)
}

// TestAppEventProjectorMarksInterruptedTurnCanceled covers kata 0ax1:
// an interrupted turn keeps the thread alive (status=idle) but the
// active turn must be reported as canceled, not completed.
func TestAppEventProjectorMarksInterruptedTurnCanceled(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{
		Reason:      "interrupted",
		State:       "idle",
		Interrupted: true,
	}})

	var sawCanceled bool
	var sawIdle bool
	for _, n := range sessionEnd {
		switch n.Method {
		case appwire.NotifyTurnCompleted:
			params, ok := n.Params.(map[string]any)
			if !ok {
				t.Fatalf("turnCompleted params=%T", n.Params)
			}
			turn, ok := params["turn"].(appwire.Turn)
			if !ok {
				t.Fatalf("turn=%T", params["turn"])
			}
			if turn.Status == appwire.TurnStatusInterrupted {
				sawCanceled = true
			}
		case appwire.NotifyThreadStatusChanged:
			params, ok := n.Params.(appwire.ThreadStatusChangedParams)
			if !ok {
				t.Fatalf("threadStatus params=%T", n.Params)
			}
			if params.Status.Type == appwire.ThreadStatusIdle {
				sawIdle = true
			}
		}
	}
	if !sawCanceled {
		t.Fatalf("interrupted SessionEnd did not mark turn canceled: %+v", sessionEnd)
	}
	if !sawIdle {
		t.Fatalf("interrupted SessionEnd did not flip thread status to idle: %+v", sessionEnd)
	}
}

func TestAppEventProjectorLetsInterruptedSessionEndCancelAfterContextCanceledError(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	errOut := projector.Project(events.SessionEvent{
		Kind:      events.EventError,
		SessionID: "th_1",
		Data:      events.ErrorData{Error: "context canceled"},
	})
	if !hasAppNotification(errOut, appwire.NotifyWarning) {
		t.Fatalf("context canceled EventError missing warning: %+v", errOut)
	}
	if hasAppNotification(errOut, appwire.NotifyTurnCompleted) {
		t.Fatalf("context canceled EventError completed turn before interrupted SessionEnd: %+v", errOut)
	}

	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{
		Reason:      "interrupted",
		State:       "idle",
		Interrupted: true,
	}})
	for _, n := range sessionEnd {
		if n.Method != appwire.NotifyTurnCompleted {
			continue
		}
		params, ok := n.Params.(map[string]any)
		if !ok {
			t.Fatalf("turnCompleted params=%T", n.Params)
		}
		turn, ok := params["turn"].(appwire.Turn)
		if !ok {
			t.Fatalf("turn=%T", params["turn"])
		}
		if turn.Status != appwire.TurnStatusInterrupted {
			t.Fatalf("turn status=%s, want canceled", turn.Status)
		}
		return
	}
	t.Fatalf("interrupted SessionEnd did not complete the active turn: %+v", sessionEnd)
}

// TestProjectorTurnEndedStampsTiming verifies that EventTurnEnded's recorded
// duration and completion time land on the Turn built by the SessionEnd site
// that later completes the active turn — EventTurnEnded itself only records
// pending timing; it does not complete or clear the turn.
func TestProjectorTurnEndedStampsTiming(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	ts := time.Unix(1_700_000_100, 0).UTC()
	turnEnded := projector.Project(events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "th_1", Timestamp: ts, Data: events.TurnEndedData{TurnDurationMS: 4200}})
	if len(turnEnded) != 0 {
		t.Fatalf("EventTurnEnded emitted notifications=%+v, want none", turnEnded)
	}
	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})

	turn := notificationTurn(t, sessionEnd, appwire.NotifyTurnCompleted)
	if turn.Status != appwire.TurnStatusCompleted {
		t.Fatalf("turn status=%s, want completed", turn.Status)
	}
	if turn.CompletedAt == nil || *turn.CompletedAt != ts.UnixMilli() {
		t.Fatalf("turn CompletedAt=%v, want %d", turn.CompletedAt, ts.UnixMilli())
	}
	if turn.DurationMS == nil || *turn.DurationMS != 4200 {
		t.Fatalf("turn DurationMS=%v, want 4200", turn.DurationMS)
	}
}

// TestProjectorTurnEndedPreservesInterruptStatus verifies that when
// EventTurnEnded is followed by an interrupted SessionEnd, the interrupt
// status wins (the turn is reported interrupted, not completed) while the
// recorded duration still attaches — timing and status are independent.
func TestProjectorTurnEndedPreservesInterruptStatus(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "th_1", Data: events.TurnEndedData{TurnDurationMS: 100}})
	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{
		Reason:      "interrupted",
		State:       "idle",
		Interrupted: true,
	}})

	turn := notificationTurn(t, sessionEnd, appwire.NotifyTurnCompleted)
	if turn.Status != appwire.TurnStatusInterrupted {
		t.Fatalf("turn status=%s, want interrupted", turn.Status)
	}
	if turn.DurationMS == nil || *turn.DurationMS != 100 {
		t.Fatalf("turn DurationMS=%v, want 100", turn.DurationMS)
	}
}

// TestProjectorAccumulatesPerTurnUsageAcrossRounds verifies the completed
// Turn's Usage is the turn's own total across every round (not a
// cumulative-session figure) — each EventAssistantTextEnd's usage is summed
// until the turn itself completes, and Cost is estimated from the model seen
// on those rounds.
func TestProjectorAccumulatesPerTurnUsageAcrossRounds(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{Model: "claude-opus-4-5"}})
	projector.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{
		Text:  "first round",
		Usage: llm.Usage{InputTokens: 100, OutputTokens: 50},
		Model: "claude-opus-4-5",
	}})
	projector.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{Model: "claude-opus-4-5"}})
	projector.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{
		Text:  "second round",
		Usage: llm.Usage{InputTokens: 20, OutputTokens: 10},
		Model: "claude-opus-4-5",
	}})
	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})

	turn := notificationTurn(t, sessionEnd, appwire.NotifyTurnCompleted)
	if turn.Usage == nil {
		t.Fatalf("turn.Usage=nil, want accumulated usage")
	}
	if turn.Usage.InputTokens != 120 {
		t.Fatalf("turn.Usage.InputTokens=%d, want 120", turn.Usage.InputTokens)
	}
	if turn.Usage.OutputTokens != 60 {
		t.Fatalf("turn.Usage.OutputTokens=%d, want 60", turn.Usage.OutputTokens)
	}
	if !strings.HasPrefix(turn.Cost, "~$") {
		t.Fatalf("turn.Cost=%q, want ~$ prefix", turn.Cost)
	}
}

// TestProjectorNewTurnResetsUsageAccumulator verifies the per-turn usage
// accumulator resets in startTurn() rather than leaking a prior turn's usage
// onto a turn that recorded no EventAssistantTextEnd of its own.
func TestProjectorNewTurnResetsUsageAccumulator(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{
		Text:  "first turn",
		Usage: llm.Usage{InputTokens: 100, OutputTokens: 50},
		Model: "claude-opus-4-5",
	}})
	// Second turn: no EventAssistantTextEnd at all before it completes.
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello again"}})
	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})

	turn := notificationTurn(t, sessionEnd, appwire.NotifyTurnCompleted)
	if turn.Usage != nil {
		t.Fatalf("turn.Usage=%+v, want nil (accumulator should have reset)", turn.Usage)
	}
}

func TestAppEventProjectorKeepsToolEventsInActiveTurnAfterAssistantText(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	turnID := notificationTurnID(t, started, appwire.NotifyTurnStarted)

	assistantEnd := projector.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "I'll check."}})
	if hasAppNotification(assistantEnd, appwire.NotifyTurnCompleted) {
		t.Fatalf("assistant end completed turn early: %+v", assistantEnd)
	}
	toolStart := projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "shell",
		CallID:        "call_1",
		ArgumentsJSON: `{"command":"pwd"}`,
	}})

	if got := notificationItemTurnID(t, toolStart, appwire.NotifyItemStarted); got != turnID {
		t.Fatalf("tool turn_id=%q, want active turn %q (notifications=%+v)", got, turnID, toolStart)
	}
}

func TestAppEventProjectorCarriesToolDescription(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "shell",
		CallID:        "call_1",
		ArgumentsJSON: `{"command":"pwd"}`,
		Description:   "Check the working directory.",
	}})

	item := notificationThreadItem(t, out, appwire.NotifyItemStarted)
	if item.Description != "Check the working directory." {
		t.Fatalf("tool description=%q", item.Description)
	}
}

func TestAppEventProjectorProjectsCommunicateAsAssistantMessage(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventCommunicate,
		SessionID: "th_1",
		Data:      events.CommunicateData{Message: "done"},
	})

	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type != "agentMessage" || item.Text != "done" || item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("communicate item=%+v", item)
	}
}

func TestAppEventProjectorSuppressesCommunicateToolEvents(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{
		Kind:      events.EventAssistantTextEnd,
		SessionID: "th_1",
		Data:      events.AssistantTextEndData{Text: "done"},
	})

	for _, ev := range []events.SessionEvent{
		{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
			ToolName:      "communicate",
			CallID:        "call_1",
			ArgumentsJSON: `{"message":"done"}`,
		}},
		{Kind: events.EventToolCallOutputDelta, SessionID: "th_1", Data: events.ToolCallOutputDeltaData{
			ToolName: "communicate",
			CallID:   "call_1",
			Delta:    `{"accepted":true}`,
		}},
		{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
			ToolName: "communicate",
			CallID:   "call_1",
			Output:   `{"accepted":true}`,
		}},
	} {
		if out := projector.Project(ev); len(out) != 0 {
			t.Fatalf("%s projected communicate tool notifications: %+v", ev.Kind, out)
		}
	}
}

func TestAppEventProjectorIncludesCallIDOnToolOutputDelta(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "shell",
		CallID:        "call_1",
		ArgumentsJSON: `{"command":"pwd"}`,
	}})

	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallOutputDelta, SessionID: "th_1", Data: events.ToolCallOutputDeltaData{
		CallID: "call_1",
		Delta:  "partial\n",
	}})

	if len(out) != 1 || out[0].Method != appwire.NotifyToolOutputDelta {
		t.Fatalf("notifications=%+v", out)
	}
	params, ok := out[0].Params.(appwire.ToolOutputDeltaParams)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params.CallID != "call_1" {
		t.Fatalf("callId=%q, want call_1 (params=%+v)", params.CallID, params)
	}
	if params.ItemID == "" || params.ItemID == params.CallID {
		t.Fatalf("itemId should preserve projected item identity separately from callId: %+v", params)
	}
}

func TestAppEventProjectorProjectsJobEvents(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{
		Kind:      events.EventJobStarted,
		SessionID: "th_1",
		Data: events.JobStartedData{
			JobID:            "job_1",
			JobType:          "delegate",
			Status:           "running",
			FromWatch:        true,
			DelegateID:       "dlg_1",
			Task:             "inspect invoices",
			TranscriptRef:    "local:child-start",
			OriginTurnID:     "turn_parent",
			OriginToolCallID: "call_delegate",
			OriginItemID:     "item_delegate",
		},
	})
	if len(started) != 2 || started[0].Method != appwire.NotifySerfJobStarted {
		t.Fatalf("started=%+v", started)
	}
	startedParams, ok := started[0].Params.(appwire.SerfJobParams)
	if !ok {
		t.Fatalf("started params=%T", started[0].Params)
	}
	startedJob := startedParams.Job
	if startedJob.JobID != "job_1" || startedJob.JobType != "delegate" || startedJob.Status != "running" || !startedJob.FromWatch ||
		startedJob.DelegateID != "dlg_1" || startedJob.Task != "inspect invoices" || startedJob.TranscriptRef != "local:child-start" ||
		startedJob.OriginTurnID != "turn_parent" || startedJob.OriginToolCallID != "call_delegate" || startedJob.OriginItemID != "item_delegate" {
		t.Fatalf("started job=%+v", startedJob)
	}

	exitCode := 137
	finished := projector.Project(events.SessionEvent{
		Kind:      events.EventJobFinished,
		SessionID: "th_1",
		Data: events.JobFinishedData{
			JobID:            "job_1",
			JobType:          "delegate",
			Status:           "failed",
			Reason:           "signal",
			ExitCode:         &exitCode,
			OutputBytes:      0,
			TranscriptRef:    "local:child",
			DelegateID:       "dlg_1",
			Task:             "inspect invoices",
			OriginTurnID:     "turn_parent",
			OriginToolCallID: "call_delegate",
			OriginItemID:     "item_delegate",
		},
	})
	if len(finished) != 2 || finished[0].Method != appwire.NotifySerfJobFinished {
		t.Fatalf("finished=%+v", finished)
	}
	finishedParams, ok := finished[0].Params.(appwire.SerfJobParams)
	if !ok {
		t.Fatalf("finished params=%T", finished[0].Params)
	}
	finishedJob := finishedParams.Job
	if finishedJob.JobID != "job_1" || finishedJob.JobType != "delegate" || finishedJob.Status != "failed" ||
		finishedJob.Reason != "signal" || finishedJob.ExitCode == nil || *finishedJob.ExitCode != exitCode ||
		finishedJob.OutputBytes != 0 || finishedJob.TranscriptRef != "local:child" ||
		finishedJob.DelegateID != "dlg_1" || finishedJob.Task != "inspect invoices" ||
		finishedJob.OriginTurnID != "turn_parent" || finishedJob.OriginToolCallID != "call_delegate" || finishedJob.OriginItemID != "item_delegate" {
		t.Fatalf("finished job=%+v", finishedJob)
	}
	finishedJSON := string(notificationParamsJSON(t, finished, appwire.NotifySerfJobFinished))
	if !strings.Contains(finishedJSON, `"outputBytes":0`) {
		t.Fatalf("finished notification json=%s missing zero outputBytes", finishedJSON)
	}
}

func TestProjectJobFinished_ExhaustionMetadata(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	resumable := true
	finished := projector.Project(events.SessionEvent{
		Kind:      events.EventJobFinished,
		SessionID: "th_1",
		Data: events.JobFinishedData{
			JobID:            "job_exhausted",
			JobType:          "delegate",
			Status:           "exhausted",
			Reason:           "tool_round_budget_exhausted",
			ExhaustionBudget: "max_tool_rounds_per_input",
			ExhaustionLimit:  1,
			Resumable:        &resumable,
		},
	})
	if len(finished) != 2 || finished[0].Method != appwire.NotifySerfJobFinished {
		t.Fatalf("finished = %+v", finished)
	}
	params, ok := finished[0].Params.(appwire.SerfJobParams)
	if !ok {
		t.Fatalf("finished params = %T", finished[0].Params)
	}
	job := params.Job
	if job.Status != "exhausted" || job.Reason != "tool_round_budget_exhausted" ||
		job.ExhaustionBudget != "max_tool_rounds_per_input" || job.ExhaustionLimit != 1 ||
		job.Resumable == nil || !*job.Resumable {
		t.Fatalf("finished job = %+v", job)
	}
}

func TestSerfJobInfoDelegateFieldsAreOptional(t *testing.T) {
	payload, err := json.Marshal(appwire.SerfJobInfo{
		JobID:       "job_shell",
		JobType:     "shell",
		Status:      "running",
		OutputBytes: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, forbidden := range []string{"delegateId", "task", "originTurnId", "originToolCallId", "originItemId", "transcriptRef"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("shell job payload %s unexpectedly contains %s", text, forbidden)
		}
	}

}

// TestAppEventProjectorProjectsQueueChanged (kata r80p) verifies the
// projector wraps QUEUE_CHANGED into a thread/queueChanged appwire
// notification carrying the authoritative depth + first-line-truncated
// preview.
func TestAppEventProjectorProjectsQueueChanged(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventQueueChanged,
		SessionID: "th_1",
		Data: events.QueueChangedData{
			Depth:   2,
			Preview: []string{"first line", "second"},
		},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyThreadQueueChanged {
		t.Fatalf("out=%+v", out)
	}
	params, ok := out[0].Params.(appwire.ThreadQueueChangedParams)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params.ThreadID != "th_1" || params.Ref != "local:th_1" {
		t.Fatalf("params identity=%+v", params)
	}
	if params.Queue.Depth != 2 {
		t.Fatalf("depth=%d, want 2", params.Queue.Depth)
	}
	if len(params.Queue.Preview) != 2 || params.Queue.Preview[0] != "first line" || params.Queue.Preview[1] != "second" {
		t.Fatalf("preview=%+v", params.Queue.Preview)
	}
}

func TestAppEventProjectorProjectsSteeringInjected(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventSteeringInjected,
		SessionID: "th_1",
		Data:      events.SteeringInjectedData{Text: "stay focused"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfSteeringInjected {
		t.Fatalf("out=%+v", out)
	}
	params, ok := out[0].Params.(map[string]any)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params["threadId"] != "th_1" || params["ref"] != "local:th_1" || params["text"] != "stay focused" {
		t.Fatalf("params=%+v", params)
	}
}

// TestAppEventProjectorProjectsSteeringInjectedUserSource (issue #24): the
// provenance source on SteeringInjectedData must reach the wire so the web UI
// can render user-sent steering as a user message instead of a system
// steering divider.
func TestAppEventProjectorProjectsSteeringInjectedUserSource(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventSteeringInjected,
		SessionID: "th_1",
		Data:      events.SteeringInjectedData{Text: "focus on the tests", Source: events.SteeringSourceUser},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfSteeringInjected {
		t.Fatalf("out=%+v", out)
	}
	params, ok := out[0].Params.(map[string]any)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params["source"] != events.SteeringSourceUser {
		t.Fatalf("params[source]=%v, want %q", params["source"], events.SteeringSourceUser)
	}

	// System steering carries no source key (empty source omitted from the
	// wire payload).
	out = projector.Project(events.SessionEvent{
		Kind:      events.EventSteeringInjected,
		SessionID: "th_1",
		Data:      events.SteeringInjectedData{Text: "<SYSTEM-REMINDER>nudge</SYSTEM-REMINDER>"},
	})
	params, ok = out[0].Params.(map[string]any)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if src, present := params["source"]; present && src != "" {
		t.Fatalf("system steering params[source]=%v, want absent or empty", src)
	}
}

func TestAppEventProjectorProjectsCompactionTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventCompactionTurn,
		SessionID: "th_1",
		Data: events.CompactionTurnData{
			Kind: string(schema.TurnSummary),
			Text: "[CONTEXT SUMMARY]\nkept the useful state",
		},
	})

	if len(out) != 1 || out[0].Method != appwire.NotifyTurnCompleted {
		t.Fatalf("notifications=%+v", out)
	}
	turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if turn.ID == "" || turn.Status != appwire.TurnStatusCompleted || turn.ItemsView != "full" || len(turn.Items) != 1 {
		t.Fatalf("turn=%+v", turn)
	}
	item := turn.Items[0]
	if item.TurnID != turn.ID || item.Type != "systemMessage" || item.Description != "Context summary" || item.Text != "[CONTEXT SUMMARY]\nkept the useful state" || item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("item=%+v", item)
	}
}

func TestAppEventProjectorProjectsCompactionTurnInActiveTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	turnID := notificationTurnID(t, started, appwire.NotifyTurnStarted)

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventCompactionTurn,
		SessionID: "th_1",
		Data: events.CompactionTurnData{
			Kind: string(schema.TurnCheckpoint),
			Text: "[CONTEXT CHECKPOINT]\nkept raw context",
		},
	})

	if len(out) != 1 || out[0].Method != appwire.NotifyItemCompleted {
		t.Fatalf("notifications=%+v", out)
	}
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.TurnID != turnID || item.Type != "systemMessage" || item.Description != "Context checkpoint" || item.Text != "[CONTEXT CHECKPOINT]\nkept raw context" || item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("item=%+v", item)
	}
}

func TestAppEventProjectorGroupsSkillActivationBeforeUseSkillEnd(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	turnID := notificationTurnID(t, started, appwire.NotifyTurnStarted)

	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "use_skill",
		CallID:        "call_skill",
		ArgumentsJSON: `{"skill_name":"superpowers:using-superpowers"}`,
	}})

	activationOut := projector.Project(events.SessionEvent{Kind: events.EventSkillActivated, SessionID: "th_1", Data: events.SkillActivatedData{Name: "superpowers:using-superpowers"}})
	if len(activationOut) != 0 {
		t.Fatalf("in-flight skill activation should be stashed until tool completion, got %+v", activationOut)
	}

	deltaOut := projector.Project(events.SessionEvent{Kind: events.EventToolCallOutputDelta, SessionID: "th_1", Data: events.ToolCallOutputDeltaData{
		ToolName: "use_skill",
		CallID:   "call_skill",
		Delta:    "Skill loaded",
	}})
	if len(deltaOut) != 1 || deltaOut[0].Method != appwire.NotifyToolOutputDelta {
		t.Fatalf("tool output delta should still stream normally: %+v", deltaOut)
	}

	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "use_skill",
		CallID:   "call_skill",
		Output:   "Skill loaded",
	}})
	if len(out) != 1 || out[0].Method != appwire.NotifyItemCompleted {
		t.Fatalf("notifications=%+v", out)
	}
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type != "commandExecution" || item.TurnID != turnID || item.ToolName != "use_skill" || item.CallID != "call_skill" {
		t.Fatalf("completed item has wrong identity: %+v", item)
	}
	var raw struct {
		SkillActivation *struct {
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"skillActivation"`
	}
	if err := json.Unmarshal(item.Raw, &raw); err != nil {
		t.Fatalf("Raw is not valid JSON: %v (%s)", err, item.Raw)
	}
	if raw.SkillActivation == nil || raw.SkillActivation.Name != "superpowers:using-superpowers" || raw.SkillActivation.Text != "Activated skill: superpowers:using-superpowers" {
		t.Fatalf("wrong skill activation raw: %+v raw=%s", raw.SkillActivation, item.Raw)
	}
}

func TestAppEventProjectorGroupsSkillActivationWithUseSkill(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	turnID := notificationTurnID(t, started, appwire.NotifyTurnStarted)

	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "use_skill",
		CallID:        "call_skill",
		ArgumentsJSON: `{"skill_name":"superpowers:using-superpowers"}`,
	}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "use_skill",
		CallID:   "call_skill",
		Output:   "Skill loaded",
	}})

	out := projector.Project(events.SessionEvent{Kind: events.EventSkillActivated, SessionID: "th_1", Data: events.SkillActivatedData{Name: "superpowers:using-superpowers"}})
	if len(out) != 1 || out[0].Method != appwire.NotifyItemCompleted {
		t.Fatalf("notifications=%+v", out)
	}
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type != "commandExecution" || item.TurnID != turnID || item.ToolName != "use_skill" || item.CallID != "call_skill" {
		t.Fatalf("grouped item has wrong identity: %+v", item)
	}
	if item.Description == "Skill activated" {
		t.Fatalf("skill activation should not be projected as system message: %+v", item)
	}
	var raw struct {
		SkillActivation *struct {
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"skillActivation"`
	}
	if err := json.Unmarshal(item.Raw, &raw); err != nil {
		t.Fatalf("Raw is not valid JSON: %v (%s)", err, item.Raw)
	}
	if raw.SkillActivation == nil || raw.SkillActivation.Name != "superpowers:using-superpowers" || raw.SkillActivation.Text != "Activated skill: superpowers:using-superpowers" {
		t.Fatalf("wrong skill activation raw: %+v raw=%s", raw.SkillActivation, item.Raw)
	}
}

func TestAppEventProjectorLeavesUnmatchedSkillActivationStandalone(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "use_skill",
		CallID:        "call_skill",
		ArgumentsJSON: `{"skill_name":"superpowers:other"}`,
	}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{ToolName: "use_skill", CallID: "call_skill", Output: "Skill loaded"}})

	out := projector.Project(events.SessionEvent{Kind: events.EventSkillActivated, SessionID: "th_1", Data: events.SkillActivatedData{Name: "superpowers:using-superpowers"}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type != "systemMessage" || item.Description != "Skill activated" || !strings.Contains(item.Text, "superpowers:using-superpowers") {
		t.Fatalf("unmatched activation should remain standalone system message: %+v", item)
	}
}

func TestAppEventProjectorGroupsSkillActivationWithLegacyUseSkillNameArg(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "use_skill",
		CallID:        "call_skill",
		ArgumentsJSON: `{"name":"legacy-skill"}`,
	}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{ToolName: "use_skill", CallID: "call_skill", Output: "Skill loaded"}})

	out := projector.Project(events.SessionEvent{Kind: events.EventSkillActivated, SessionID: "th_1", Data: events.SkillActivatedData{Name: "legacy-skill"}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type != "commandExecution" || item.CallID != "call_skill" || len(item.Raw) == 0 {
		t.Fatalf("legacy use_skill name arg should correlate: %+v", item)
	}
}

func TestAppEventProjectorDoesNotInferSkillActivationAcrossAssistantText(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "use_skill",
		CallID:        "call_skill",
		ArgumentsJSON: `{"skill_name":"superpowers:using-superpowers"}`,
	}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{ToolName: "use_skill", CallID: "call_skill", Output: "Skill loaded"}})
	projector.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "I will continue."}})

	out := projector.Project(events.SessionEvent{Kind: events.EventSkillActivated, SessionID: "th_1", Data: events.SkillActivatedData{Name: "superpowers:using-superpowers"}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type != "systemMessage" || item.Description != "Skill activated" {
		t.Fatalf("activation after intervening assistant text should remain standalone: %+v", item)
	}
}

func TestAppEventProjectorProjectsAgentOnlyEventsAsSystemAnnouncements(t *testing.T) {
	tests := []struct {
		name        string
		event       events.SessionEvent
		description string
		eventKind   appwire.ThreadItemEventKind
		contains    []string
		notContains []string
		singleLine  bool
	}{
		{
			name:        "turn limit max turns",
			event:       events.SessionEvent{Kind: events.EventTurnLimit, SessionID: "th_1", Data: events.TurnLimitData{MaxTurns: 3}},
			description: "Turn limit",
			contains:    []string{"Maximum turns reached: 3"},
		},
		{
			name:        "turn limit tool rounds",
			event:       events.SessionEvent{Kind: events.EventTurnLimit, SessionID: "th_1", Data: events.TurnLimitData{MaxToolRoundsPerInput: 7}},
			description: "Turn limit",
			contains:    []string{"Maximum tool rounds per input reached: 7"},
		},
		{
			name:        "loop detection",
			event:       events.SessionEvent{Kind: events.EventLoopDetection, SessionID: "th_1", Data: events.LoopDetectionData{Message: "Repeated tool pattern detected"}},
			description: "Loop detection",
			contains:    []string{"Repeated tool pattern detected"},
		},
		{
			name:        "skill activated",
			event:       events.SessionEvent{Kind: events.EventSkillActivated, SessionID: "th_1", Data: events.SkillActivatedData{Name: "using-superpowers"}},
			description: "Skill activated",
			contains:    []string{"using-superpowers"},
		},
		{
			name: "context compaction",
			event: events.SessionEvent{Kind: events.EventContextCompaction, SessionID: "th_1", Data: events.ContextCompactionData{
				Layer:           "L4",
				TurnsBefore:     42,
				TurnsAfter:      8,
				EstTokensBefore: 120000,
				EstTokensAfter:  23000,
			}},
			description: "Context compaction",
			contains:    []string{"Layer: L4", "Turns: 42 -> 8", "Estimated tokens: 120000 -> 23000"},
		},
		{
			name: "plugin loaded",
			event: events.SessionEvent{Kind: events.EventPluginLoaded, SessionID: "th_1", Data: events.PluginLoadedData{
				Name: "superpowers", SkillCount: 5, AgentCount: 2, MCPCount: 1,
			}},
			description: "Loaded plugin superpowers (5 skills, 2 agents, 1 MCP servers)",
			eventKind:   appwire.ThreadItemEventKindPluginLoaded,
		},
		{
			name: "hook end",
			event: events.SessionEvent{Kind: events.EventHookEnd, SessionID: "th_1", Data: events.HookEndData{
				Event: "SessionStart", HookType: "command", Matcher: "using-superpowers", PluginName: "superpowers", ExitCode: 0, DurationMS: 37,
			}},
			description: "Hook",
			contains:    []string{"SessionStart hook", "using-superpowers", "superpowers", "command", "exit 0"},
			notContains: []string{"37ms"},
			singleLine:  true,
		},
		{
			name:        "fork summary",
			event:       events.SessionEvent{Kind: events.EventForkSummary, SessionID: "th_1", Data: events.ForkSummaryData{Turn: 12}},
			description: "Fork summary",
			contains:    []string{"turn 12"},
		},
		{
			name:        "prompt loaded",
			event:       events.SessionEvent{Kind: events.EventPromptLoaded, SessionID: "th_1", Data: events.PromptLoadedData{Label: "system.md", Size: 2048}},
			description: "Prompt loaded",
			contains:    []string{"system.md", "2048 B"},
		},
		{
			name: "round timings",
			event: events.SessionEvent{Kind: events.EventRoundTimings, SessionID: "th_1", Data: events.RoundTimings{
				Round:        2,
				TotalRound:   1500 * time.Millisecond,
				LLMCall:      1200 * time.Millisecond,
				ContextMgmt:  25 * time.Millisecond,
				ToolExec:     40 * time.Millisecond,
				LoopOverhead: 5 * time.Millisecond,
			}},
			description: "Round timings",
			contains:    []string{"Round 2", "total=1.5s", "llm=1.2s", "context=25ms", "tools=40ms"},
		},
		{
			name: "tool call repaired: alias",
			event: events.SessionEvent{Kind: events.EventToolCallRepaired, SessionID: "th_1", Data: events.ToolCallRepairedData{
				ToolName: "edit_file",
				CallID:   "c1",
				Changes:  []string{"alias:old_string:old_str→old_string"},
			}},
			description: "Tool call repaired",
			contains:    []string{"edit_file", "renamed", "old_str", "old_string"},
			notContains: []string{"alias:old_string:old_str→old_string"},
		},
		{
			name: "tool call repaired: drop_unknown",
			event: events.SessionEvent{Kind: events.EventToolCallRepaired, SessionID: "th_1", Data: events.ToolCallRepairedData{
				ToolName: "communicate",
				CallID:   "c1",
				Changes:  []string{"drop_unknown:artifacts:dropped artifacts"},
			}},
			description: "Tool call repaired",
			contains:    []string{"communicate", "unrecognized", "artifacts"},
			notContains: []string{"drop_unknown:artifacts:dropped artifacts", "dropped artifacts"},
		},
		{
			name: "tool call repaired: coerce_type",
			event: events.SessionEvent{Kind: events.EventToolCallRepaired, SessionID: "th_1", Data: events.ToolCallRepairedData{
				ToolName: "run_command",
				CallID:   "c1",
				Changes:  []string{`coerce_type:timeout:"30"→30`},
			}},
			description: "Tool call repaired",
			contains:    []string{"run_command", "timeout"},
			notContains: []string{`coerce_type:timeout:"30"→30`},
		},
		{
			name: "tool call repaired: unicode_repair",
			event: events.SessionEvent{Kind: events.EventToolCallRepaired, SessionID: "th_1", Data: events.ToolCallRepairedData{
				ToolName: "edit_file",
				CallID:   "c1",
				Changes:  []string{`unicode_repair::invalid \u escape → �`},
			}},
			description: "Tool call repaired",
			contains:    []string{"edit_file", "invalid character"},
			notContains: []string{`unicode_repair::invalid \u escape → �`},
		},
		{
			name: "tool call repaired: multiple changes",
			event: events.SessionEvent{Kind: events.EventToolCallRepaired, SessionID: "th_1", Data: events.ToolCallRepairedData{
				ToolName: "communicate",
				CallID:   "c1",
				Changes:  []string{"alias:message:msg→message", "drop_unknown:artifacts:dropped artifacts"},
			}},
			description: "Tool call repaired",
			contains:    []string{"communicate", "renamed", "unrecognized", "artifacts"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projector := NewAppEventProjector("th_1", "local:th_1")
			out := projector.Project(tt.event)

			if len(out) != 1 || out[0].Method != appwire.NotifyTurnCompleted {
				t.Fatalf("notifications=%+v", out)
			}
			turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
			if turn.Status != appwire.TurnStatusCompleted || turn.ItemsView != "full" || len(turn.Items) != 1 {
				t.Fatalf("turn=%+v", turn)
			}
			item := turn.Items[0]
			if item.Type != "systemMessage" || item.Description != tt.description || item.Status != appwire.TurnStatusCompleted {
				t.Fatalf("item=%+v", item)
			}
			if tt.eventKind != "" && item.EventKind != tt.eventKind {
				t.Fatalf("item eventKind=%q, want %q", item.EventKind, tt.eventKind)
			}
			for _, want := range tt.contains {
				if !strings.Contains(item.Text, want) {
					t.Fatalf("item text %q does not contain %q", item.Text, want)
				}
			}
			for _, unwanted := range tt.notContains {
				if strings.Contains(item.Text, unwanted) {
					t.Fatalf("item text %q contains unwanted %q", item.Text, unwanted)
				}
			}
			if tt.singleLine && strings.Contains(item.Text, "\n") {
				t.Fatalf("item text %q should be one line", item.Text)
			}
		})
	}
}

// Before a session's first real turn, SESSION_START-time announcements
// (plugin loads, prompt-loaded notices, ...) each arrive with no active
// turn. Left ungrouped, a dormant spawn's prompt-loading burst renders as one
// standalone line per event instead of the single collapsed disclosure
// SystemNoticeItem's consecutive-run grouping already exists to produce
// (kata bz2z: "a wall of prompt-loading lines"). They must share ONE
// synthetic turn id - appwire.SystemPreludeTurnID, the same id
// apptranscript.PreludeTurn uses for the persisted-transcript system prompt -
// so the client's existing grouping actually has one turn's worth of items
// to fold, and so a dormant session's live and replayed views agree on what
// "no real turn yet" looks like.
func TestAppEventProjectorFoldsPreFirstTurnAnnouncementsIntoOnePreludeTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	first := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventPluginLoaded, SessionID: "th_1",
		Data: events.PluginLoadedData{Name: "superpowers", SkillCount: 5},
	}), appwire.NotifyTurnCompleted)

	second := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventPromptLoaded, SessionID: "th_1",
		Data: events.PromptLoadedData{Label: "identity.md", Size: 2212},
	}), appwire.NotifyTurnCompleted)

	third := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventPromptLoaded, SessionID: "th_1",
		Data: events.PromptLoadedData{Label: "capabilities.md", Size: 276},
	}), appwire.NotifyTurnCompleted)

	if first != appwire.SystemPreludeTurnID || second != appwire.SystemPreludeTurnID || third != appwire.SystemPreludeTurnID {
		t.Fatalf("pre-first-turn announcement ids = %q, %q, %q; want all %q",
			first, second, third, appwire.SystemPreludeTurnID)
	}

	// Once a real turn starts, the prelude is over: a SESSION_START-only event
	// occurring later (e.g. a hook that fires between two real turns) must NOT
	// fold into the prelude bucket - it happened after turn_1, not before it,
	// and the prelude turn already rendered at the top of the transcript.
	realTurnID := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hi"},
	}), appwire.NotifyTurnStarted)
	if realTurnID == "" || realTurnID == appwire.SystemPreludeTurnID {
		t.Fatalf("real turn id = %q, want a genuine turn_N distinct from the prelude", realTurnID)
	}

	// Close turn 1 (as a failure, for convenience - any of the four completion
	// sites clears activeTurnID the same way) so the next announcement arrives
	// in the gap between turns, exactly like the between-turns case above.
	projector.Project(events.SessionEvent{Kind: events.EventError, SessionID: "th_1", Data: events.ErrorData{Error: "boom"}})

	afterFirstTurn := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventPluginLoaded, SessionID: "th_1",
		Data: events.PluginLoadedData{Name: "late-loader"},
	}), appwire.NotifyTurnCompleted)
	if afterFirstTurn == appwire.SystemPreludeTurnID || afterFirstTurn == realTurnID {
		t.Fatalf("post-first-turn announcement id = %q, want a fresh turn distinct from both the prelude (%q) and turn 1 (%q)",
			afterFirstTurn, appwire.SystemPreludeTurnID, realTurnID)
	}
}

// A turn id RESERVED before the session's first real turn - turn/start's
// reservation (server.reserveAppTurnIDForStart), or the SetProcessing
// auto-continuation's (server.setProcessingLocked) - bumps the projector's
// turn counter without anything having actually run. A spawned session with
// a queued initial prompt hits exactly this: the reservation lands while
// plugins, prompts and hooks are still announcing. Those SESSION_START-time
// announcements still happened BEFORE any real turn, so they must still fold
// into the one prelude turn. Exiling them to a gap id minted after turn_1's
// reservation is how a session's startup burst came to render as a "25
// system events" group anchored at the END of the transcript.
func TestAppEventProjectorReservationBeforeFirstTurnKeepsAnnouncementsInPrelude(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	reserved := projector.ReserveTurnID()
	if reserved == "" || reserved == appwire.SystemPreludeTurnID {
		t.Fatalf("reserved id = %q, want a genuine turn_N", reserved)
	}

	first := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventPluginLoaded, SessionID: "th_1",
		Data: events.PluginLoadedData{Name: "superpowers", SkillCount: 5},
	}), appwire.NotifyTurnCompleted)
	second := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventPromptLoaded, SessionID: "th_1",
		Data: events.PromptLoadedData{Label: "identity.md", Size: 2212},
	}), appwire.NotifyTurnCompleted)

	if first != appwire.SystemPreludeTurnID || second != appwire.SystemPreludeTurnID {
		t.Fatalf("post-reservation announcement ids = %q, %q; want both the prelude %q",
			first, second, appwire.SystemPreludeTurnID)
	}

	// The reserved turn then starts for real, consuming the reservation.
	realTurnID := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hi"},
	}), appwire.NotifyTurnStarted)
	if realTurnID != reserved {
		t.Fatalf("real turn id = %q, want the reserved %q", realTurnID, reserved)
	}
}

// The prelude carve-out above is for a FRESH session only. A resumed
// session's startup burst (plugin loads, prompt-loaded notices firing as the
// daemon reattaches) happened after the persisted history, not before any
// real turn, so it must keep minting its own gap id (kata 9ekv) - folding it
// into the prelude turn at the very top would misrepresent when it happened.
func TestAppEventProjectorSeededHistoryKeepsAnnouncementsOutOfPrelude(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.SeedPersistedTurns(5)

	got := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventPluginLoaded, SessionID: "th_1",
		Data: events.PluginLoadedData{Name: "late-loader"},
	}), appwire.NotifyTurnCompleted)
	if got == appwire.SystemPreludeTurnID {
		t.Fatalf("resumed-session announcement id = %q, want a fresh gap id distinct from the prelude (%q)",
			got, appwire.SystemPreludeTurnID)
	}
}

// A burst of no-active-turn announcements landing BETWEEN two real turns
// (nextTurn > 0, activeTurnID == "") must share one turn id with each other —
// same rationale as the pre-first-turn prelude (kata bz2z): otherwise
// SystemNoticeItem's consecutive-run grouping never gets 3+ same-turn items to
// fold and a back-to-back run of hook completions renders as a wall of
// one-line turns, just like the dormant-session case bz2z fixed.
//
// This must NOT reuse bz2z's single global SystemPreludeTurnID: two different
// gaps (between turn 1/2, and between turn 2/3) each get their OWN shared id,
// because folding both gaps' events into one bucket would misrepresent when
// they happened relative to the real turns between them (kata 9ekv).
func TestAppEventProjectorFoldsMidSessionAnnouncementBurstsIntoOneTurnPerGap(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	turn1 := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hi"},
	}), appwire.NotifyTurnStarted)
	projector.Project(events.SessionEvent{Kind: events.EventError, SessionID: "th_1", Data: events.ErrorData{Error: "boom"}})

	// Three system announcements land back-to-back in the gap after turn 1,
	// before turn 2 starts.
	gap1First := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventPluginLoaded, SessionID: "th_1", Data: events.PluginLoadedData{Name: "hook-a"},
	}), appwire.NotifyTurnCompleted)
	gap1Second := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventPluginLoaded, SessionID: "th_1", Data: events.PluginLoadedData{Name: "hook-b"},
	}), appwire.NotifyTurnCompleted)
	gap1Third := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventPluginLoaded, SessionID: "th_1", Data: events.PluginLoadedData{Name: "hook-c"},
	}), appwire.NotifyTurnCompleted)

	if gap1First != gap1Second || gap1Second != gap1Third {
		t.Fatalf("gap-1 announcement ids = %q, %q, %q; want all equal", gap1First, gap1Second, gap1Third)
	}
	if gap1First == turn1 || gap1First == appwire.SystemPreludeTurnID {
		t.Fatalf("gap-1 announcement id = %q, want distinct from turn 1 (%q) and the prelude id (%q)",
			gap1First, turn1, appwire.SystemPreludeTurnID)
	}

	turn2 := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "again"},
	}), appwire.NotifyTurnStarted)
	if turn2 == gap1First || turn2 == turn1 {
		t.Fatalf("turn 2 id = %q, want distinct from gap 1 (%q) and turn 1 (%q)", turn2, gap1First, turn1)
	}
	projector.Project(events.SessionEvent{Kind: events.EventError, SessionID: "th_1", Data: events.ErrorData{Error: "boom again"}})

	// A single announcement lands in the gap after turn 2. It must get a
	// FRESH id, not gap 1's id — reuse across a real turn boundary would
	// misplace it chronologically.
	gap2First := notificationTurnID(t, projector.Project(events.SessionEvent{
		Kind: events.EventPluginLoaded, SessionID: "th_1", Data: events.PluginLoadedData{Name: "hook-d"},
	}), appwire.NotifyTurnCompleted)
	if gap2First == gap1First || gap2First == turn1 || gap2First == turn2 {
		t.Fatalf("gap-2 announcement id = %q, want fresh (distinct from gap 1 %q, turn 1 %q, turn 2 %q)",
			gap2First, gap1First, turn1, turn2)
	}
}

// A hook-completed announcement must carry the hook's exit code as a typed
// field, not only inside its prose ("... exit 1"). The web's Settings ->
// Transcript "Hook exits (normal only)" toggle has to split exit-0 from
// nonzero hooks, and it must do so off the wire's own number rather than by
// re-parsing English out of the announcement text.
func TestAppEventProjectorHookEndCarriesTypedExitCode(t *testing.T) {
	for _, code := range []int{0, 1, -1, 127} {
		projector := NewAppEventProjector("th_1", "local:th_1")
		out := projector.Project(events.SessionEvent{Kind: events.EventHookEnd, SessionID: "th_1", Data: events.HookEndData{
			Event: "PreToolUse", HookType: "command", ExitCode: code,
		}})

		turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
		if len(turn.Items) != 1 {
			t.Fatalf("turn=%+v", turn)
		}
		item := turn.Items[0]
		if item.EventKind != appwire.ThreadItemEventKindHookCompleted {
			t.Fatalf("item eventKind=%q, want %q", item.EventKind, appwire.ThreadItemEventKindHookCompleted)
		}
		if item.ExitCode == nil {
			t.Fatalf("hook item carries no typed ExitCode (text-only %q)", item.Text)
		}
		if *item.ExitCode != int64(code) {
			t.Fatalf("hook item ExitCode=%d, want %d", *item.ExitCode, code)
		}
	}
}

// Only hook items get an exit code. A skill/plugin/compaction announcement
// has no process behind it, so fabricating a zero there would let the web
// mistake it for a cleanly-exited hook.
func TestAppEventProjectorNonHookAnnouncementsCarryNoExitCode(t *testing.T) {
	events_ := []events.SessionEvent{
		{Kind: events.EventSkillActivated, SessionID: "th_1", Data: events.SkillActivatedData{Name: "using-superpowers"}},
		{Kind: events.EventPromptLoaded, SessionID: "th_1", Data: events.PromptLoadedData{Label: "system.md", Size: 2048}},
		{Kind: events.EventLoopDetection, SessionID: "th_1", Data: events.LoopDetectionData{Message: "loop"}},
	}
	for _, event := range events_ {
		projector := NewAppEventProjector("th_1", "local:th_1")
		turn := notificationTurn(t, projector.Project(event), appwire.NotifyTurnCompleted)
		if len(turn.Items) != 1 {
			t.Fatalf("turn=%+v", turn)
		}
		if got := turn.Items[0].ExitCode; got != nil {
			t.Fatalf("%s item ExitCode=%d, want nil", event.Kind, *got)
		}
	}
}

// A context-compaction announcement must carry the structured before/after
// numbers (not only prose) so the web can render an honest, inspectable
// before→after expand (mockup #17 Alt A) instead of re-parsing the text.
func TestAppEventProjectorContextCompactionCarriesStructuredNumbers(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventContextCompaction, SessionID: "th_1", Data: events.ContextCompactionData{
		Layer:           "L4",
		TurnsBefore:     42,
		TurnsAfter:      8,
		EstTokensBefore: 120000,
		EstTokensAfter:  23000,
	}})

	if len(out) != 1 || out[0].Method != appwire.NotifyTurnCompleted {
		t.Fatalf("notifications=%+v", out)
	}
	turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if len(turn.Items) != 1 {
		t.Fatalf("turn=%+v", turn)
	}
	item := turn.Items[0]
	if len(item.Raw) == 0 {
		t.Fatalf("compaction item should carry structured numbers in Raw, got none: %+v", item)
	}
	var got struct {
		Compaction *events.ContextCompactionData `json:"compaction"`
	}
	if err := json.Unmarshal(item.Raw, &got); err != nil {
		t.Fatalf("Raw is not valid JSON: %v (%s)", err, item.Raw)
	}
	if got.Compaction == nil {
		t.Fatalf("Raw should carry a compaction object, got %s", item.Raw)
	}
	if got.Compaction.Layer != "L4" ||
		got.Compaction.TurnsBefore != 42 || got.Compaction.TurnsAfter != 8 ||
		got.Compaction.EstTokensBefore != 120000 || got.Compaction.EstTokensAfter != 23000 {
		t.Fatalf("Raw compaction numbers wrong: %+v", got.Compaction)
	}
}

// A round-timings announcement must carry the per-phase durations as
// structured numbers in Raw, not only inside its "total=... llm=..." prose:
// the web renders a rounded, prioritized summary from the real numbers
// (kata 7zkv) rather than re-parsing nanosecond-precision text.
func TestAppEventProjectorRoundTimingsCarriesStructuredNumbers(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventRoundTimings, SessionID: "th_1", Data: events.RoundTimings{
		Round:        2,
		TotalRound:   1500 * time.Millisecond,
		LLMCall:      1200 * time.Millisecond,
		ContextMgmt:  25 * time.Millisecond,
		ToolExec:     40 * time.Millisecond,
		LoopOverhead: 5 * time.Millisecond,
	}})

	if len(out) != 1 || out[0].Method != appwire.NotifyTurnCompleted {
		t.Fatalf("notifications=%+v", out)
	}
	turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if len(turn.Items) != 1 {
		t.Fatalf("turn=%+v", turn)
	}
	item := turn.Items[0]
	if len(item.Raw) == 0 {
		t.Fatalf("round-timings item should carry structured numbers in Raw, got none: %+v", item)
	}
	var got struct {
		RoundTimings *events.RoundTimings `json:"roundTimings"`
	}
	if err := json.Unmarshal(item.Raw, &got); err != nil {
		t.Fatalf("Raw is not valid JSON: %v (%s)", err, item.Raw)
	}
	if got.RoundTimings == nil {
		t.Fatalf("Raw should carry a roundTimings object, got %s", item.Raw)
	}
	rt := got.RoundTimings
	if rt.Round != 2 || rt.TotalRound != 1500*time.Millisecond || rt.LLMCall != 1200*time.Millisecond ||
		rt.ContextMgmt != 25*time.Millisecond || rt.ToolExec != 40*time.Millisecond || rt.LoopOverhead != 5*time.Millisecond {
		t.Fatalf("Raw roundTimings numbers wrong: %+v", rt)
	}
	// The prose line stays as it always was - a heterogeneous-version relay,
	// or any non-web consumer, still gets a readable text fallback.
	for _, want := range []string{"Round 2", "total=1.5s", "llm=1.2s", "context=25ms", "tools=40ms"} {
		if !strings.Contains(item.Text, want) {
			t.Fatalf("item text %q does not contain %q", item.Text, want)
		}
	}
}

func TestAppEventProjectorProjectsPluginLoadedWithSafeEventKindDetail(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventPluginLoaded, SessionID: "th_1", Data: events.PluginLoadedData{
		Name:           "superpowers",
		Dir:            "/home/jesse/.codex/plugins/superpowers",
		SkillCount:     14,
		AgentCount:     0,
		MCPCount:       0,
		ManifestFlavor: "codex",
		ManifestPath:   "/home/jesse/.codex/plugins/superpowers/.codex-plugin/plugin.json",
	}})

	if len(out) != 1 || out[0].Method != appwire.NotifyTurnCompleted {
		t.Fatalf("notifications=%+v", out)
	}
	turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if len(turn.Items) != 1 {
		t.Fatalf("turn=%+v", turn)
	}
	item := turn.Items[0]
	if item.Type != "systemMessage" {
		t.Fatalf("item type=%q, want systemMessage", item.Type)
	}
	itemJSON, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal item: %v", err)
	}
	var itemFields map[string]json.RawMessage
	if err := json.Unmarshal(itemJSON, &itemFields); err != nil {
		t.Fatalf("unmarshal item fields: %v (%s)", err, itemJSON)
	}
	var eventKind string
	if err := json.Unmarshal(itemFields["eventKind"], &eventKind); err != nil {
		t.Fatalf("item eventKind missing or invalid: %v (%s)", err, itemJSON)
	}
	if eventKind != "plugin_loaded" {
		t.Fatalf("item eventKind=%q, want plugin_loaded", eventKind)
	}
	if _, ok := itemFields["noDisclosure"]; ok {
		t.Fatalf("item must not expose noDisclosure: %s", itemJSON)
	}
	const wantSummary = "Loaded plugin superpowers (14 skills, 0 agents, 0 MCP servers)"
	if item.Description != wantSummary {
		t.Fatalf("item description=%q, want %q", item.Description, wantSummary)
	}
	if item.Text != "" {
		t.Fatalf("plugin-loaded inline contract should not require text, got %q", item.Text)
	}
	if len(item.Raw) == 0 {
		t.Fatalf("plugin-loaded item should carry safe raw detail, got none: %+v", item)
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(item.Raw, &rawFields); err != nil {
		t.Fatalf("Raw is not valid JSON: %v (%s)", err, item.Raw)
	}
	pluginRaw, ok := rawFields["pluginLoaded"]
	if !ok {
		t.Fatalf("Raw should carry pluginLoaded detail, got %s", item.Raw)
	}
	var pluginLoaded struct {
		Name       string `json:"name"`
		SkillCount int    `json:"skillCount"`
		AgentCount int    `json:"agentCount"`
		MCPCount   int    `json:"mcpCount"`
	}
	if err := json.Unmarshal(pluginRaw, &pluginLoaded); err != nil {
		t.Fatalf("pluginLoaded detail is invalid: %v (%s)", err, pluginRaw)
	}
	if pluginLoaded.Name != "superpowers" || pluginLoaded.SkillCount != 14 || pluginLoaded.AgentCount != 0 || pluginLoaded.MCPCount != 0 {
		t.Fatalf("pluginLoaded detail=%+v, want safe display counts", pluginLoaded)
	}
	var pluginFields map[string]json.RawMessage
	if err := json.Unmarshal(pluginRaw, &pluginFields); err != nil {
		t.Fatalf("pluginLoaded fields invalid: %v (%s)", err, pluginRaw)
	}
	for _, forbidden := range []string{"dir", "manifest_path", "manifestFlavor", "manifest_flavor"} {
		if _, ok := pluginFields[forbidden]; ok {
			t.Fatalf("pluginLoaded detail must not expose %q: %s", forbidden, pluginRaw)
		}
	}
}

func TestAppEventProjectorDoesNotDisplayHookStart(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventHookStart, SessionID: "th_1", Data: events.HookStartData{
		Event: "SessionStart", HookType: "command", Matcher: "using-superpowers", PluginName: "superpowers",
	}})

	if len(out) != 0 {
		t.Fatalf("hook start should not project appwire notifications: %+v", out)
	}
}

func TestAppEventProjectorProjectsAgentOnlyAnnouncementInActiveTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	turnID := notificationTurnID(t, started, appwire.NotifyTurnStarted)

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventSkillActivated,
		SessionID: "th_1",
		Data:      events.SkillActivatedData{Name: "using-superpowers"},
	})

	if len(out) != 1 || out[0].Method != appwire.NotifyItemCompleted {
		t.Fatalf("notifications=%+v", out)
	}
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.TurnID != turnID || item.Type != "systemMessage" || item.Description != "Skill activated" || !strings.Contains(item.Text, "using-superpowers") {
		t.Fatalf("item=%+v", item)
	}
}

func TestAppEventProjectorProjectsImageOnlySteeringInjected(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventSteeringInjected,
		SessionID: "th_1",
		Data: events.SteeringInjectedData{Images: []events.UserInputImage{{
			MediaType: "image/png",
			Data:      []byte("png"),
			Name:      "shot.png",
		}}},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfSteeringInjected {
		t.Fatalf("out=%+v", out)
	}
	params, ok := out[0].Params.(map[string]any)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params["text"] != "[image]" {
		t.Fatalf("text=%q, want [image]", params["text"])
	}
	images, ok := params["images"].([]appwire.InputItem)
	if !ok || len(images) != 1 || images[0].MediaType != "image/png" {
		t.Fatalf("images=%+v", params["images"])
	}
}

// TestAppEventProjectorProjectsSteeringInjectedKind (system steering voice):
// the Kind the daemon named at the injection site (events.SteeringKind*)
// must reach the wire so the web UI labels a steer from ground truth instead
// of pattern-matching its prose.
func TestAppEventProjectorProjectsSteeringInjectedKind(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventSteeringInjected,
		SessionID: "th_1",
		Data:      events.SteeringInjectedData{Text: "done", Kind: events.SteeringKindTasksDone},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfSteeringInjected {
		t.Fatalf("out=%+v", out)
	}
	params, ok := out[0].Params.(map[string]any)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params["kind"] != events.SteeringKindTasksDone {
		t.Fatalf("params[kind]=%v, want %q", params["kind"], events.SteeringKindTasksDone)
	}
}

// TestAppEventProjectorOmitsSteeringInjectedEmptyKind is the other half of
// TestAppEventProjectorProjectsSteeringInjectedKind: a steer the daemon did
// not classify carries no "kind" key at all on the wire, matching how an
// unset Source is omitted (issue #24).
func TestAppEventProjectorOmitsSteeringInjectedEmptyKind(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventSteeringInjected,
		SessionID: "th_1",
		Data:      events.SteeringInjectedData{Text: "mystery"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfSteeringInjected {
		t.Fatalf("out=%+v", out)
	}
	params, ok := out[0].Params.(map[string]any)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if _, present := params["kind"]; present {
		t.Fatalf("kind key present for an unkinded steer; want it omitted, params=%+v", params)
	}
}

// TestProjector_ForwardsProviderCause (kata cmfz) verifies that when an
// EventError carries a structured ErrorCause (populated by agent.Session
// when the underlying error is a typed llm.Error), the projector forwards
// the cause into the failed-turn TurnError so consumers can typed-branch
// on Cause.Kind instead of substring-matching the message. A genuine failure
// is surfaced only as a failed turn — never also as a NotifyWarning.
func TestProjector_ForwardsProviderCause(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventError,
		SessionID: "th_1",
		Data: events.ErrorData{
			Error: "anthropic: 503 service unavailable",
			Cause: &events.ErrorCause{
				Kind:     "provider",
				Provider: "anthropic",
				Model:    "claude-opus-4-7",
				Status:   503,
			},
		},
	})

	if hasAppNotification(out, appwire.NotifyWarning) {
		t.Fatalf("non-cancelled error emitted a redundant NotifyWarning: %+v", out)
	}
	var completed *AppNotification
	for i := range out {
		if out[i].Method == appwire.NotifyTurnCompleted {
			completed = &out[i]
			break
		}
	}
	if completed == nil {
		t.Fatalf("no turn/completed notification: %+v", out)
		return
	}
	completedParams, ok := completed.Params.(map[string]any)
	if !ok {
		t.Fatalf("completed params=%T", completed.Params)
	}
	turn, ok := completedParams["turn"].(appwire.Turn)
	if !ok {
		t.Fatalf("completed turn=%T", completedParams["turn"])
	}
	if turn.Error == nil || turn.Error.Cause == nil {
		t.Fatalf("turn error cause missing: %+v", turn.Error)
	}
	if turn.Error.Cause.Kind != "provider" || turn.Error.Cause.Provider != "anthropic" || turn.Error.Cause.Model != "claude-opus-4-7" || turn.Error.Cause.Status != 503 {
		t.Fatalf("turn error cause=%+v", turn.Error.Cause)
	}
}

// TestProjector_OmitsCauseWhenAbsent (kata cmfz) verifies that when an
// EventError has no structured cause, the projector does not invent one
// and the failed-turn TurnError's cause stays nil.
func TestProjector_OmitsCauseWhenAbsent(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventError,
		SessionID: "th_1",
		Data:      events.ErrorData{Error: "something else broke"},
	})

	if hasAppNotification(out, appwire.NotifyWarning) {
		t.Fatalf("non-cancelled error emitted a redundant NotifyWarning: %+v", out)
	}
	turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if turn.Error == nil {
		t.Fatalf("failed turn missing error: %+v", turn)
	}
	if turn.Error.Cause != nil {
		t.Fatalf("expected nil cause, got %+v", turn.Error.Cause)
	}
}

// TestProjector_BackcompatNonProviderError (kata cmfz) regression-locks the
// diagnostic projection for a non-provider error — message, an explicit hub
// source, title, and hint must pass through unchanged on the failed-turn
// TurnError, with no cause invented and no redundant NotifyWarning.
func TestProjector_BackcompatNonProviderError(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventError,
		SessionID: "th_1",
		Data: events.ErrorData{
			Error:  "subscribe failed",
			Source: "hub",
			Title:  "Live updates unavailable",
			Hint:   "Retry the action.",
		},
	})

	if hasAppNotification(out, appwire.NotifyWarning) {
		t.Fatalf("non-cancelled error emitted a redundant NotifyWarning: %+v", out)
	}
	turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if turn.Error == nil {
		t.Fatalf("failed turn missing error: %+v", turn)
	}
	if turn.Error.Message != "subscribe failed" {
		t.Fatalf("message=%v", turn.Error.Message)
	}
	if turn.Error.Source != "hub" {
		t.Fatalf("source=%v", turn.Error.Source)
	}
	if turn.Error.Title != "Live updates unavailable" {
		t.Fatalf("title=%v", turn.Error.Title)
	}
	if turn.Error.Hint != "Retry the action." {
		t.Fatalf("hint=%v", turn.Error.Hint)
	}
	if turn.Error.Cause != nil {
		t.Fatalf("expected nil cause, got %+v", turn.Error.Cause)
	}
}

// TestProjector_GenuineErrorEmitsSingleDiagnostic verifies that a non-cancelled
// EventError surfaces exactly once — as a failed turn carrying the full
// diagnostic (message/source/title/hint/cause) — and does NOT also emit a
// redundant NotifyWarning. Emitting both made the same error render twice in
// clients that show both channels (the web UI drew a "Provider warning" card
// and a "Provider error" card for one failure).
func TestProjector_GenuineErrorEmitsSingleDiagnostic(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventError,
		SessionID: "th_1",
		Data: events.ErrorData{
			Error:  `openai error: chat.completions stream closed without [DONE] (model: "gpt-5.4-mini")`,
			Source: "provider",
			Title:  "Provider error",
			Cause: &events.ErrorCause{
				Kind:     "provider",
				Provider: "openai",
				Model:    "gpt-5.4-mini",
			},
		},
	})

	if hasAppNotification(out, appwire.NotifyWarning) {
		t.Fatalf("non-cancelled error emitted a redundant NotifyWarning: %+v", out)
	}
	if !hasAppNotification(out, appwire.NotifyTurnCompleted) {
		t.Fatalf("non-cancelled error did not complete the turn as failed: %+v", out)
	}
	turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if turn.Status != appwire.TurnStatusFailed {
		t.Fatalf("turn status=%s, want failed", turn.Status)
	}
	if turn.Error == nil {
		t.Fatalf("failed turn missing error: %+v", turn)
	}
	if turn.Error.Message == "" || turn.Error.Title != "Provider error" || turn.Error.Source != "provider" {
		t.Fatalf("failed turn error missing diagnostic fields: %+v", turn.Error)
	}
	if turn.Error.Cause == nil || turn.Error.Cause.Provider != "openai" {
		t.Fatalf("failed turn error missing cause: %+v", turn.Error)
	}
}

// TestProjector_AssistantTextResetDiscardsInProgressItem verifies that an
// EventAssistantTextReset discards the in-progress assistant item (naming it so
// consumers can remove it) and clears projector state so the next delta opens a
// fresh item. With no in-progress assistant, it is a no-op.
func TestProjector_AssistantTextResetDiscardsInProgressItem(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hi"}})

	startOut := p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{}})
	startedItem := ""
	for _, n := range startOut {
		if n.Method == appwire.NotifyItemStarted {
			if params, ok := n.Params.(appwire.ItemLifecycleParams); ok {
				startedItem = params.Item.ID
			}
		}
	}
	if startedItem == "" {
		t.Fatalf("assistant start did not open an item: %+v", startOut)
	}
	p.Project(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "partial"}})

	resetOut := p.Project(events.SessionEvent{Kind: events.EventAssistantTextReset, SessionID: "th_1", Data: events.AssistantTextResetData{}})
	var resetParams *appwire.AgentMessageResetParams
	for i := range resetOut {
		if resetOut[i].Method == appwire.NotifyAgentMessageReset {
			pp, ok := resetOut[i].Params.(appwire.AgentMessageResetParams)
			if !ok {
				t.Fatalf("reset params=%T", resetOut[i].Params)
			}
			resetParams = &pp
		}
	}
	if resetParams == nil {
		t.Fatalf("reset did not emit NotifyAgentMessageReset: %+v", resetOut)
		return
	}
	if resetParams.ItemID != startedItem {
		t.Fatalf("reset itemID=%q, want the discarded item %q", resetParams.ItemID, startedItem)
	}

	// State is cleared: the next delta opens a fresh item, not the discarded one.
	deltaOut := p.Project(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "fresh"}})
	freshItem := ""
	for _, n := range deltaOut {
		if n.Method == appwire.NotifyAgentMessageDelta {
			if params, ok := n.Params.(appwire.AgentMessageDeltaParams); ok {
				freshItem = params.ItemID
			}
		}
	}
	if freshItem == "" || freshItem == startedItem {
		t.Fatalf("post-reset delta itemID=%q, want a fresh item distinct from %q", freshItem, startedItem)
	}

	// No in-progress assistant → reset is a no-op.
	p.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "fresh"}})
	noop := p.Project(events.SessionEvent{Kind: events.EventAssistantTextReset, SessionID: "th_1", Data: events.AssistantTextResetData{}})
	if hasAppNotification(noop, appwire.NotifyAgentMessageReset) {
		t.Fatalf("reset with no in-progress assistant should be a no-op: %+v", noop)
	}
}

func hasAppNotification(items []AppNotification, method string) bool {
	for _, item := range items {
		if item.Method == method {
			return true
		}
	}
	return false
}

func notificationParamsJSON(t *testing.T, items []AppNotification, method string) []byte {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		data, err := json.Marshal(item.Params)
		if err != nil {
			t.Fatalf("marshal params for %s: %v", method, err)
		}
		return data
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return nil
}

func notificationTurnID(t *testing.T, items []AppNotification, method string) string {
	t.Helper()
	return notificationTurn(t, items, method).ID
}

// Issue #26: live tool frames feed the web UI's inline subagent activity line,
// which renders the tool call's purpose. The started item carries the purpose
// in Description; the completed item must carry it too (derived from the
// call's arguments, mirroring apptranscript.ToolIntentFromArguments) so the
// activity line stays on the purpose when the tool finishes.
func TestAppEventProjectorToolCallEndCarriesPurposeDescription(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "shell",
		CallID:        "call_1",
		ArgumentsJSON: `{"command":"go test ./...","purpose":"run the full test suite"}`,
		Description:   "run the full test suite",
	}})
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "shell",
		CallID:   "call_1",
		Output:   "ok",
	}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Description != "run the full test suite" {
		t.Fatalf("completed tool item should carry the purpose-derived Description, got %q", item.Description)
	}

	// The intent field is honored too, matching ToolIntentFromArguments.
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "grep",
		CallID:        "call_2",
		ArgumentsJSON: `{"query":"retry","intent":"trace the retry callers"}`,
	}})
	out = projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "grep",
		CallID:   "call_2",
		Output:   "3 matches",
	}})
	item = notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Description != "trace the retry callers" {
		t.Fatalf("completed tool item should derive Description from the intent field, got %q", item.Description)
	}

	// No purpose in the arguments: Description stays empty rather than
	// falling back to a raw command dump.
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "shell",
		CallID:        "call_3",
		ArgumentsJSON: `{"command":"ls -la"}`,
	}})
	out = projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "shell",
		CallID:   "call_3",
		Output:   "...",
	}})
	item = notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Description != "" {
		t.Fatalf("purpose-less tool call should have empty Description, got %q", item.Description)
	}
}

// notificationTurn reads the "turn" payload off either shape a producer might
// use: appwire.TurnStartedParams (turn/started, converted - kcb5) or a bare
// map[string]any (turn/completed, deliberately left unconverted - kcb5's own
// TurnCompletedParams declaration doesn't match what producers send, see
// appwire_projection.go's own comment on its turn/completed sites).
func notificationTurn(t *testing.T, items []AppNotification, method string) appwire.Turn {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		if p, ok := item.Params.(appwire.TurnStartedParams); ok {
			return p.Turn
		}
		params, ok := item.Params.(map[string]any)
		if !ok {
			t.Fatalf("params=%T", item.Params)
		}
		turn, ok := params["turn"].(appwire.Turn)
		if !ok {
			t.Fatalf("turn param=%T in %+v", params["turn"], params)
		}
		return turn
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return appwire.Turn{}
}

func notificationItemTurnID(t *testing.T, items []AppNotification, method string) string {
	t.Helper()
	return notificationThreadItem(t, items, method).TurnID
}

// notificationThreadItem reads the "item" payload off an item/started or
// item/completed notification, both of which now send appwire.ItemLifecycleParams
// (kcb5).
func notificationThreadItem(t *testing.T, items []AppNotification, method string) appwire.ThreadItem {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		p, ok := item.Params.(appwire.ItemLifecycleParams)
		if !ok {
			t.Fatalf("params=%T", item.Params)
		}
		return p.Item
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return appwire.ThreadItem{}
}

// notificationThread reads the "thread" payload off a thread/started
// notification, which now sends appwire.ThreadStartedParams (kcb5).
func notificationThread(t *testing.T, items []AppNotification, method string) appwire.Thread {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		p, ok := item.Params.(appwire.ThreadStartedParams)
		if !ok {
			t.Fatalf("params=%T", item.Params)
		}
		return p.Thread
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return appwire.Thread{}
}

func notificationThreadStatus(t *testing.T, items []AppNotification, method string) appwire.ThreadStatus {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		params, ok := item.Params.(appwire.ThreadStatusChangedParams)
		if !ok {
			t.Fatalf("params=%T", item.Params)
		}
		return params.Status
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return appwire.ThreadStatus{}
}

// TestAppEventProjectorStampsToolItemTiming (issue #37): the web transcript's
// per-tool hover meta (timestamp · runtime) must be built from REAL server
// times, not the browser's wall clock. The session event stream already
// records when each tool call started and ended (SessionEvent.Timestamp), so
// the projector stamps the started item with StartedAt and the completed item
// with StartedAt/CompletedAt/DurationMS. Zero timestamps stay unset rather
// than reporting the Unix epoch.
func TestAppEventProjectorStampsToolItemTiming(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	start := time.Unix(1_700_000_000, 0).UTC()
	end := start.Add(2500 * time.Millisecond)

	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Timestamp: start, Data: events.ToolCallStartData{
		ToolName: "shell",
		CallID:   "call_timed",
	}})
	item := notificationThreadItem(t, out, appwire.NotifyItemStarted)
	if item.StartedAt == nil || *item.StartedAt != start.UnixMilli() {
		t.Fatalf("started item StartedAt=%v, want %d", item.StartedAt, start.UnixMilli())
	}

	out = projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Timestamp: end, Data: events.ToolCallEndData{
		ToolName: "shell",
		CallID:   "call_timed",
		Output:   "ok",
	}})
	item = notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.StartedAt == nil || *item.StartedAt != start.UnixMilli() {
		t.Fatalf("completed item StartedAt=%v, want %d", item.StartedAt, start.UnixMilli())
	}
	if item.CompletedAt == nil || *item.CompletedAt != end.UnixMilli() {
		t.Fatalf("completed item CompletedAt=%v, want %d", item.CompletedAt, end.UnixMilli())
	}
	if item.DurationMS == nil || *item.DurationMS != 2500 {
		t.Fatalf("completed item DurationMS=%v, want 2500", item.DurationMS)
	}
}

// TestAppEventProjectorLeavesToolItemTimingUnsetWithoutClock: an event with a
// zero timestamp (no recorded server time) must NOT mint epoch-0 stamps — the
// client shows nothing rather than a fake time (issue #37).
func TestAppEventProjectorLeavesToolItemTimingUnsetWithoutClock(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName: "shell",
		CallID:   "call_clockless",
	}})
	item := notificationThreadItem(t, out, appwire.NotifyItemStarted)
	if item.StartedAt != nil {
		t.Fatalf("started item StartedAt=%v, want nil for zero event timestamp", *item.StartedAt)
	}
	out = projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "shell",
		CallID:   "call_clockless",
		Output:   "ok",
	}})
	item = notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.StartedAt != nil || item.CompletedAt != nil || item.DurationMS != nil {
		t.Fatalf("completed item timing=(%v,%v,%v), want all nil for zero event timestamps", item.StartedAt, item.CompletedAt, item.DurationMS)
	}
}

// TestAppEventProjectorToolCallEndCarriesArgumentsJSON (wire-honesty spec Part
// A): the settled commandExecution item must carry the same ArgumentsJSON the
// started item already had. The projector already resolves it (toolArgsByKey,
// falling back to the end event's own ArgumentsJSON) but previously dropped it
// from the completed item literal.
func TestAppEventProjectorToolCallEndCarriesArgumentsJSON(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "shell",
		CallID:        "call_args",
		ArgumentsJSON: `{"command":"ls"}`,
	}})
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "shell",
		CallID:   "call_args",
		Output:   "ok",
	}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.ArgumentsJSON != `{"command":"ls"}` {
		t.Fatalf("completed item ArgumentsJSON=%q, want the started item's args", item.ArgumentsJSON)
	}
}

// TestAppEventProjectorToolCallEndCarriesExitCode (wire-honesty spec Part A):
// the shell tool's exit code already rides ToolState.exit_code
// (agent/session_tools_shell.go:483 shellToolResult) end to end; the settled
// item promotes it onto the typed ExitCode field.
func TestAppEventProjectorToolCallEndCarriesExitCode(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{ToolName: "shell", CallID: "call_exit"}})
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName:  "shell",
		CallID:    "call_exit",
		Output:    "ok",
		ToolState: json.RawMessage(`{"type":"shell","status":"completed","exit_code":2}`),
	}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.ExitCode == nil || *item.ExitCode != 2 {
		t.Fatalf("completed item ExitCode=%v, want *2", item.ExitCode)
	}
}

// TestAppEventProjectorToolCallEndCarriesPrevalOnly (kata hgm1) pins that the
// projector copies events.ToolCallEndData.PrevalOnly onto the settled item's
// wire field verbatim - the client-side signal that this failure never
// reached the tool's real execution.
func TestAppEventProjectorToolCallEndCarriesPrevalOnly(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{ToolName: "ask_user", CallID: "call_bad"}})
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName:   "ask_user",
		CallID:     "call_bad",
		Error:      "missing required field: header",
		PrevalOnly: true,
	}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if !item.PrevalOnly {
		t.Fatal("expected PrevalOnly to carry through onto the settled item")
	}
}

// TestAppEventProjectorToolCallEndOmitsPrevalOnlyOnRealFailure pins the other
// direction: a real execution failure (no PrevalOnly on the wire event) must
// not somehow default true on the settled item.
func TestAppEventProjectorToolCallEndOmitsPrevalOnlyOnRealFailure(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{ToolName: "shell", CallID: "call_fail"}})
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "shell",
		CallID:   "call_fail",
		Error:    "command not found",
	}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.PrevalOnly {
		t.Fatal("a real execution failure must not carry PrevalOnly")
	}
}

// TestAppEventProjectorToolCallEndCarriesZeroExitCode (wire-honesty spec Part
// A, review Minor) pins the boundary that makes the pointer field honest: a
// successful shell run's ToolState literally contains "exit_code":0, which
// must produce a non-nil *int64 pointing at 0 — distinguishable from the
// "no ToolState at all" case (TestAppEventProjectorToolCallEndOmitsExitCode
// WithoutToolState below), which leaves it nil. Go's json only touches the
// pointer when the key is present, so this already holds by construction;
// pinned here so it can never silently regress.
func TestAppEventProjectorToolCallEndCarriesZeroExitCode(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{ToolName: "shell", CallID: "call_exit_zero"}})
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName:  "shell",
		CallID:    "call_exit_zero",
		Output:    "ok",
		ToolState: json.RawMessage(`{"type":"shell","status":"completed","exit_code":0}`),
	}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.ExitCode == nil {
		t.Fatalf("completed item ExitCode=nil, want a non-nil pointer to 0 (present-and-zero, not absent)")
	}
	if *item.ExitCode != 0 {
		t.Fatalf("completed item ExitCode=%v, want *0", *item.ExitCode)
	}
}

// TestAppEventProjectorToolCallEndOmitsExitCodeWithoutToolState (wire-honesty
// spec Part A): a tool whose ToolState carries no exit_code (or none at all,
// e.g. read_file) must leave ExitCode nil rather than fabricating zero.
func TestAppEventProjectorToolCallEndOmitsExitCodeWithoutToolState(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{ToolName: "read_file", CallID: "call_read"}})
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "read_file",
		CallID:   "call_read",
		Output:   "contents",
	}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.ExitCode != nil {
		t.Fatalf("completed item ExitCode=%v, want nil for a tool with no ToolState", *item.ExitCode)
	}
}

// TestAppEventProjectorToolCallEndStampsFailedStatusOnError (Go follow-up:
// the projector hardcoded Status:"completed" on every settled item
// regardless of Error, so clients had to infer error state by checking
// Error's presence instead of trusting Status). An IsError tool result must
// settle with TurnStatusFailed.
func TestAppEventProjectorToolCallEndStampsFailedStatusOnError(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{ToolName: "shell", CallID: "call_fail"}})
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "shell",
		CallID:   "call_fail",
		Error:    "boom",
	}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Status != appwire.TurnStatusFailed {
		t.Fatalf("errored tool item Status=%q, want %q", item.Status, appwire.TurnStatusFailed)
	}
	if item.Error != "boom" {
		t.Fatalf("errored tool item Error=%q, want %q", item.Error, "boom")
	}
}

// TestAppEventProjectorToolCallEndKeepsCompletedStatusWithoutError pins the
// non-error branch alongside the failed-status test above so both sides of
// the status decision are exercised.
func TestAppEventProjectorToolCallEndKeepsCompletedStatusWithoutError(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{ToolName: "shell", CallID: "call_ok"}})
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "shell",
		CallID:   "call_ok",
		Output:   "ok",
	}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("successful tool item Status=%q, want %q", item.Status, appwire.TurnStatusCompleted)
	}
}

// kata 4zn8: a rate-limited model call must reach the client as a thread-scoped
// retry notice. It is deliberately NOT an item — a four-hour rate limit
// produced 91 retries in one session, and 91 transcript items is noise, not
// signal. The client renders it as ephemeral liveness state instead.
func TestProjectModelRetryEmitsThreadScopedNotice(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hi"}})

	out := p.Project(events.SessionEvent{Kind: events.EventModelRetry, SessionID: "th_1", Data: events.ModelRetryData{
		Attempt:     9,
		MaxAttempts: 11,
		DelayMS:     60000,
		ErrorClass:  "rate_limit",
		StatusCode:  429,
		Message:     "rate limit exceeded",
		Model:       "k3",
	}})

	var got *appwire.ThreadModelRetryParams
	for i := range out {
		if out[i].Method == appwire.NotifySerfThreadModelRetry {
			pp, ok := out[i].Params.(appwire.ThreadModelRetryParams)
			if !ok {
				t.Fatalf("retry params=%T", out[i].Params)
			}
			got = &pp
		}
	}
	if got == nil {
		t.Fatalf("model retry did not emit NotifySerfThreadModelRetry: %+v", out)
		return
	}
	if got.ThreadID != "th_1" {
		t.Errorf("ThreadID = %q, want %q", got.ThreadID, "th_1")
	}
	if got.Attempt != 9 || got.MaxAttempts != 11 {
		t.Errorf("attempt = %d/%d, want 9/11", got.Attempt, got.MaxAttempts)
	}
	if got.DelayMS != 60000 {
		t.Errorf("DelayMS = %d, want 60000", got.DelayMS)
	}
	if got.ErrorClass != "rate_limit" || got.StatusCode != http.StatusTooManyRequests {
		t.Errorf("errorClass/status = %q/%d, want rate_limit/429", got.ErrorClass, got.StatusCode)
	}

	// A retry must not manufacture a transcript item.
	if hasAppNotification(out, appwire.NotifyItemStarted) || hasAppNotification(out, appwire.NotifyItemCompleted) {
		t.Errorf("model retry emitted an item lifecycle notification: %+v", out)
	}
}

// TestProjectToolCallEndCarriesOutputImagesToTheWire pins the middle link of
// the live tool-result image path (kata 2fxm): the agent describes the image on
// TOOL_CALL_END, and the item/completed frame a dashboard actually reads has to
// carry that description through unchanged.
//
// Which frame that is moved in kata v3dv. The description reaches the wire on
// the round's TOOL_RESULT_IMAGES_PERSISTED rather than on the call's own
// settle, because until the round is written nothing can serve the bytes it
// names; see TestToolResultImageDescriptorWaitsForItsBytes. What this test
// still pins is that it arrives, and arrives unchanged.
func TestProjectToolCallEndCarriesOutputImagesToTheWire(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "shoot"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName: "screenshot", CallID: "call_shot", ArgumentsJSON: `{}`,
	}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "screenshot", CallID: "call_shot", Output: "captured",
		OutputImages: []events.OutputImage{{
			Source: "tool-result", Name: "screenshot", MediaType: "image/png", Size: 12,
			SHA: strings.Repeat("a", 64),
		}},
	}})
	out := projector.Project(events.SessionEvent{Kind: events.EventToolResultImagesPersisted, SessionID: "th_1", Data: events.ToolResultImagesPersistedData{
		CallIDs: []string{"call_shot"},
	}})

	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	want := appwire.OutputImage{
		Source: "tool-result", Name: "screenshot", MediaType: "image/png", Size: 12,
		SHA: strings.Repeat("a", 64),
	}
	if len(item.OutputImages) != 1 || item.OutputImages[0] != want {
		t.Fatalf("item.OutputImages=%+v, want %+v", item.OutputImages, want)
	}
}

// TestProjectToolCallEndDropsAnUnaddressableOutputImage keeps a descriptor that
// names neither a sha nor a URL off the wire: nothing can fetch it, so it would
// render as a broken thumbnail.
func TestProjectToolCallEndDropsAnUnaddressableOutputImage(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "shoot"}})
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "screenshot", CallID: "call_shot",
		OutputImages: []events.OutputImage{{Source: "tool-result", Name: "screenshot"}},
	}})

	if item := notificationThreadItem(t, out, appwire.NotifyItemCompleted); len(item.OutputImages) != 0 {
		t.Fatalf("item.OutputImages=%+v, want the unaddressable descriptor dropped", item.OutputImages)
	}
}

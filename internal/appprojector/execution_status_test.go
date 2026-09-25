package appprojector

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func projectEvent(p *AppEventProjector, data events.EventData) []AppNotification {
	ev := events.New(data)
	ev.SessionID = "th_1"
	return p.Project(ev)
}

// An execution's start publishes the thread active naming its TurnID, and its
// end publishes it idle; an end for an execution another one replaced says
// nothing. A model retry names the running execution.
func TestProjectorPublishesExecutionsAsThreadStatus(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	status := func(out []AppNotification) appwire.ThreadStatusChangedParams {
		t.Helper()
		if len(out) != 1 || out[0].Method != appwire.NotifyThreadStatusChanged {
			t.Fatalf("notifications = %+v, want one thread/status/changed", out)
		}
		return out[0].Params.(appwire.ThreadStatusChangedParams)
	}

	if got := status(projectEvent(p, events.ExecutionStartedData{TurnID: "t_1"})); got.Status.Type != appwire.ThreadStatusActive || got.ActiveTurnID != "t_1" {
		t.Fatalf("execution start = %+v, want active naming t_1", got)
	}
	if p.RunningTurnID() != "t_1" {
		t.Fatalf("running turn = %q, want t_1", p.RunningTurnID())
	}
	retry := projectEvent(p, events.ModelRetryData{Attempt: 2})
	if len(retry) != 1 || retry[0].Params.(appwire.ThreadModelRetryParams).TurnID != "t_1" {
		t.Fatalf("model retry = %+v, want one naming t_1", retry)
	}
	if out := projectEvent(p, events.ExecutionEndedData{TurnID: "t_0", Status: "completed"}); len(out) != 0 {
		t.Fatalf("a replaced execution's end = %+v, want nothing", out)
	}
	if got := status(projectEvent(p, events.ExecutionEndedData{TurnID: "t_1", Status: "completed"})); got.Status.Type != appwire.ThreadStatusIdle || got.ActiveTurnID != "" {
		t.Fatalf("execution end = %+v, want idle naming no turn", got)
	}
	if p.RunningTurnID() != "" {
		t.Fatalf("running turn = %q after the end, want none", p.RunningTurnID())
	}

	projectEvent(p, events.ExecutionStartedData{TurnID: "t_2"})
	end := projectEvent(p, events.SessionEndData{State: appwire.ThreadStatusIdle})
	if got := status(end); got.Status.Type != appwire.ThreadStatusIdle || got.ActiveTurnID != "" || p.RunningTurnID() != "" {
		t.Fatalf("session end = %+v running %q, want idle and no running execution", got, p.RunningTurnID())
	}
}

// History reaches clients from recorded entries and live unrecorded state from
// the overlay: the projector emits neither, and mints no turn or item ids.
func TestProjectorEmitsNoHistory(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	projectEvent(p, events.ExecutionStartedData{TurnID: "t_1"})
	for _, data := range []events.EventData{
		events.UserInputData{Text: "hello"},
		events.TurnStartedData{TurnID: "t_1"},
		events.GoalContinuationData{Text: "continue"},
		events.EnvironmentData{Text: "environment"},
		events.RoundStartedData{RoundID: "r_1"},
		events.AssistantTextStartData{},
		events.AssistantTextDeltaData{Delta: "text"},
		events.ReasoningSummaryDeltaData{Delta: "thinking"},
		events.AssistantTextEndData{Text: "text"},
		events.AssistantTextResetData{},
		events.CommunicatePreviewStartData{CallID: "c"},
		events.CommunicatePreviewDeltaData{CallID: "c", Delta: "x"},
		events.CommunicateData{Message: "said"},
		events.ToolCallStartData{ToolName: "shell", CallID: "call"},
		events.ToolCallOutputDeltaData{CallID: "call", Delta: "out"},
		events.ToolCallEndData{ToolName: "shell", CallID: "call"},
		events.ToolResultImagesPersistedData{CallIDs: []string{"call"}},
		events.SteeringInjectedData{Text: "steer"},
		events.WarningData{Message: "warning"},
		events.ErrorData{Error: "failure"},
		events.HookEndData{},
		events.SkillActivatedData{Name: "skill"},
		events.TurnEndedData{TurnDurationMS: 5},
		events.RoundEndedData{RoundID: "r_1"},
	} {
		ev := events.New(data)
		ev.SessionID, ev.Timestamp = "th_1", time.Unix(100, 0)
		if out := p.Project(ev); len(out) != 0 {
			t.Fatalf("%s projected %+v, want nothing", ev.Kind, out)
		}
	}
}

package appprojector

import (
	"net/http"
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

	projectEvent(p, events.ExecutionStartedData{TurnID: "t_2"})
	end := projectEvent(p, events.SessionEndData{State: appwire.ThreadStatusIdle})
	if got := status(end); got.Status.Type != appwire.ThreadStatusIdle || got.ActiveTurnID != "" {
		t.Fatalf("session end = %+v, want idle and no running execution", got)
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

func TestProjectModelRetryEmitsThreadScopedNotice(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	projectEvent(p, events.ExecutionStartedData{TurnID: "t_retry"})

	out := projectEvent(p, events.ModelRetryData{
		Attempt:        9,
		MaxAttempts:    11,
		DelayMS:        60000,
		ErrorClass:     "rate_limit",
		StatusCode:     http.StatusTooManyRequests,
		Message:        "rate limit exceeded",
		Model:          "k3",
		GroupElapsedMS: 840000,
		AttemptCap:     4,
	})

	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerThreadModelRetry {
		t.Fatalf("model retry = %+v, want one %s", out, appwire.NotifyEvenerThreadModelRetry)
	}
	got, ok := out[0].Params.(appwire.ThreadModelRetryParams)
	if !ok {
		t.Fatalf("retry params=%T", out[0].Params)
	}
	if got.ThreadID != "th_1" || got.TurnID != "t_retry" {
		t.Errorf("thread/turn = %q/%q, want th_1/t_retry", got.ThreadID, got.TurnID)
	}
	if got.Attempt != 9 || got.MaxAttempts != 11 {
		t.Errorf("attempt = %d/%d, want 9/11", got.Attempt, got.MaxAttempts)
	}
	if got.DelayMS != 60000 {
		t.Errorf("DelayMS = %d, want 60000", got.DelayMS)
	}
	if got.ErrorClass != "rate_limit" || got.StatusCode != http.StatusTooManyRequests || got.Message != "rate limit exceeded" || got.Model != "k3" {
		t.Errorf("errorClass/status/message/model = %q/%d/%q/%q, want rate_limit/429/rate limit exceeded/k3", got.ErrorClass, got.StatusCode, got.Message, got.Model)
	}
	// The honest denominator (AttemptCap) and per-call elapsed time
	// (GroupElapsedMS) must ride the wire unchanged — clients cannot render
	// "9/11" against a budget the early-stop rule already cut to 4.
	if got.GroupElapsedMS != 840000 {
		t.Errorf("GroupElapsedMS = %d, want 840000", got.GroupElapsedMS)
	}
	if got.AttemptCap != 4 {
		t.Errorf("AttemptCap = %d, want 4", got.AttemptCap)
	}
}

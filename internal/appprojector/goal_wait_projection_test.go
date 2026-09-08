package appprojector

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// Slice-1 projection tests (Task 5, spec §7 wire): GoalStateData wait lists
// project to appwire.GoalState waiting fields, and EventGoalWaiting /
// EventGoalResumed project to announcements. TDD red phase: these fail until
// the payloads and projector cases exist.

// TestProject_GoalUpdatedCarriesWaitingState pins the §7 M2 wire fix: the wait
// list on GoalStateData reaches the wire GoalState (labels + deadlines only),
// with the nearest deadline resolved earliest-first, tie → smallest wait_id.
func TestProject_GoalUpdatedCarriesWaitingState(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventGoalUpdated,
		Data: events.GoalUpdatedData{Goal: &events.GoalStateData{
			Objective:  "ship it",
			Status:     "waiting",
			Iterations: 2,
			WaitingOn: []events.GoalWaitData{
				{WaitID: "wait_2", Label: "beta", DeadlineUnixMilli: 2000},
				{WaitID: "wait_1", Label: "alpha", DeadlineUnixMilli: 2000},
			},
			NearestDeadlineUnixMilli: 2000,
			NearestLabel:             "alpha",
			UsedContinuations:        3,
			MaxContinuations:         200,
		}},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerGoalUpdated {
		t.Fatalf("want one evener/goal/updated notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.GoalUpdatedParams)
	if !ok || params.Goal == nil {
		t.Fatalf("params = %+v, want goal state", out[0].Params)
	}
	if len(params.Goal.WaitingOn) != 2 {
		t.Fatalf("WaitingOn = %+v, want both waits", params.Goal.WaitingOn)
	}
	if params.Goal.WaitingOn[0].WaitID != "wait_2" || params.Goal.WaitingOn[0].Label != "beta" {
		t.Fatalf("WaitingOn[0] = %+v, want wait_2/beta", params.Goal.WaitingOn[0])
	}
	if params.Goal.NearestLabel != "alpha" || params.Goal.NearestDeadlineUnixMilli != 2000 {
		t.Fatalf("nearest = %+v, want alpha@2000", params.Goal)
	}
	if params.Goal.UsedContinuations != 3 || params.Goal.MaxContinuations != 200 {
		t.Fatalf("progress = %+v, want 3/200", params.Goal)
	}
}

// TestProject_GoalWaitingAnnounces pins §7: EventGoalWaiting projects to a
// goal_waiting system announcement naming the count and nearest label.
func TestProject_GoalWaitingAnnounces(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	p.Project(events.SessionEvent{
		Kind: events.EventUserInput,
		Data: events.UserInputData{Text: "hello"},
	})
	out := p.Project(events.SessionEvent{
		Kind: events.EventGoalWaiting,
		Data: events.GoalWaitingData{Count: 2, NearestLabel: "alpha", NearestDeadlineUnixMilli: 2000},
	})
	if len(out) == 0 {
		t.Fatal("EventGoalWaiting projected nothing, want a goal_waiting announcement")
	}
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.EventKind != appwire.ThreadItemEventKindGoalWaiting {
		t.Fatalf("EventKind = %q, want goal_waiting", item.EventKind)
	}
}

// TestProject_GoalResumedAnnounces pins §7: EventGoalResumed projects to a
// goal_resumed system announcement naming the fired waits.
func TestProject_GoalResumedAnnounces(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	p.Project(events.SessionEvent{
		Kind: events.EventUserInput,
		Data: events.UserInputData{Text: "hello"},
	})
	out := p.Project(events.SessionEvent{
		Kind: events.EventGoalResumed,
		Data: events.GoalResumedData{WaitIDs: []string{"wait_1"}},
	})
	if len(out) == 0 {
		t.Fatal("EventGoalResumed projected nothing, want a goal_resumed announcement")
	}
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.EventKind != appwire.ThreadItemEventKindGoalResumed {
		t.Fatalf("EventKind = %q, want goal_resumed", item.EventKind)
	}
}

// TestGoalWaitingChipText pins the §6 chip aggregation: "waiting on <n> ·
// <nearest label> · <deadline>".
func TestGoalWaitingChipText(t *testing.T) {
	got := GoalWaitingChipText(&appwire.GoalState{
		Status:                   "waiting",
		WaitingOn:                []appwire.GoalWaitState{{WaitID: "wait_1", Label: "alpha"}, {WaitID: "wait_2", Label: "beta"}},
		NearestLabel:             "alpha",
		NearestDeadlineUnixMilli: 2000,
	})
	want := "waiting on 2 · alpha · 2000"
	if got != want {
		t.Fatalf("chip = %q, want %q", got, want)
	}
}

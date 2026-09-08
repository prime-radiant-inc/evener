package appprojector

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// Task-8 projection tests (spec §§6–7): EventGoalWatchdog projects to a
// goal_watchdog announcement, and AnnounceSilently suppresses the
// GoalWaiting announcement while keeping the emit.
func TestProject_GoalWatchdogAnnounces(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	p.Project(events.SessionEvent{
		Kind: events.EventUserInput,
		Data: events.UserInputData{Text: "hello"},
	})
	out := p.Project(events.SessionEvent{
		Kind: events.EventGoalWatchdog,
		Data: events.GoalWatchdogData{Kind: "park-start", NearestLabel: "alpha", NearestDeadlineUnixMilli: 2000},
	})
	if len(out) == 0 {
		t.Fatal("EventGoalWatchdog projected nothing, want a goal_watchdog announcement")
	}
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.EventKind != appwire.ThreadItemEventKindGoalWatchdog {
		t.Fatalf("EventKind = %q, want goal_watchdog", item.EventKind)
	}
}

func TestProject_GoalWaitingSilentSuppresses(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	p.Project(events.SessionEvent{
		Kind: events.EventUserInput,
		Data: events.UserInputData{Text: "hello"},
	})
	out := p.Project(events.SessionEvent{
		Kind: events.EventGoalWaiting,
		Data: events.GoalWaitingData{Count: 1, NearestLabel: "stall re-check", AnnounceSilently: true},
	})
	for _, n := range out {
		if n.Method == appwire.NotifyItemCompleted {
			if item := notificationThreadItem(t, out, appwire.NotifyItemCompleted); item.EventKind == appwire.ThreadItemEventKindGoalWaiting {
				t.Fatalf("silent auto-park must not announce goal_waiting: %+v", item)
			}
		}
	}
}

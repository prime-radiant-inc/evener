package appprojector

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// An idle fold takes its owner at stage time, when nothing is running, but
// emits its records later, in flush. Nothing orders that flush against a turn
// the client accepts meanwhile, so a gap-owned announcement can reach the
// projector while a different turn is active. The group is over either way --
// no turn ran under that id and none ever will -- so it has to complete
// regardless of what is running now, or the live store leaves it InProgress
// against the transcript projection's Completed.
func TestCompactionGapGroupCompletesWhileAnotherTurnRuns(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{
		Text: "a turn the client started after the fold staged", StableTurnID: "turn_m1",
	}})

	const gapOwner = "turn_compaction_01M293Z0FEZ4Q34KM6E9SGAGAW"
	out := p.Project(events.SessionEvent{Kind: events.EventCompactionTurn, SessionID: "th_1", Data: events.CompactionTurnData{
		Kind: "SUMMARY", Text: "[CONTEXT SUMMARY]", OwningTurnID: gapOwner,
	}})

	turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if turn.ID != gapOwner {
		t.Fatalf("completed turn = %q, want the fold's own group %q", turn.ID, gapOwner)
	}
	if turn.Status != appwire.TurnStatusCompleted {
		t.Fatalf("gap group status = %q, want %q", turn.Status, appwire.TurnStatusCompleted)
	}
	if len(turn.Items) != 1 || turn.Items[0].TurnID != gapOwner {
		t.Fatalf("gap group items = %#v, want the fold's record under its own owner", turn.Items)
	}

	// The running turn is untouched: nothing completed it, and it still owns
	// what it publishes next.
	for _, n := range out {
		if n.Method != appwire.NotifyTurnCompleted {
			continue
		}
		if completed, ok := n.Params.(map[string]any); ok {
			if named, ok := completed["turn"].(appwire.Turn); ok && named.ID == "turn_m1" {
				t.Fatal("the fold's announcement completed the running turn")
			}
		}
	}
	next := p.Project(events.SessionEvent{Kind: events.EventHookEnd, SessionID: "th_1", Data: events.HookEndData{
		Event: "PostToolUse", HookType: "command", ExitCode: 0,
	}})
	if got := notificationItemTurnID(t, next, appwire.NotifyItemCompleted); got != "turn_m1" {
		t.Fatalf("after the gap group closed, the next unowned announcement landed on %q, want the still-running turn_m1", got)
	}
}

package server

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appprojector"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// liveUserTurnID projects the events setup produces, then one user input that
// carries no durable identity of its own, and reports the turn id the live
// projector minted for it from its fallback sequence.
func liveUserTurnID(setup func(*appprojector.AppEventProjector)) string {
	p := appprojector.NewAppEventProjector("th_1", "local:th_1")
	setup(p)
	for _, n := range p.Project(events.SessionEvent{
		Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hi"},
	}) {
		if lp, ok := n.Params.(appwire.ItemLifecycleParams); ok && lp.Item.Type == "userMessage" {
			return lp.Item.TurnID
		}
	}
	return ""
}

// coldTurnIDs replays the same conversation from its transcript entries, which
// is what every reconciling client compares the live stream against.
func coldTurnIDs(t *testing.T, entries []transcript.Entry) []string {
	t.Helper()
	turns, err := apptranscript.ItemTurnsFromEntries(transcript.Header{SessionID: "th_1"}, entries,
		func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
			return apptranscript.ProjectTurn(turnID, turnIndex, turn, map[string]string{}, nil, apptranscript.ToolResultOutputImages)
		})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(turns))
	for _, turn := range turns {
		ids = append(ids, turn.ID)
	}
	return ids
}

// TestTurnSequenceAgreesAcrossLiveAndColdAfterAStableTurn: the two projections
// share one "turn_%d" namespace, and cold numbers a turn by its ENTRY INDEX --
// so every persisted turn consumes a number, including one the transcript names
// by its own durable id. The live projector has to spend that number too, or
// the next turn it names from the fallback sequence collides with an entry the
// replay already numbered: the same user input reads turn_1 live and turn_2
// cold, and a client reconciling the two shows a phantom split.
//
// Both stable-id shapes are here because they are one rule, not two: an
// environment entry (this series) and a client-mutation entry (turn/start)
// occupy an entry index the same way.
func TestTurnSequenceAgreesAcrossLiveAndColdAfterAStableTurn(t *testing.T) {
	plainUser := func(text string) schema.Turn { return schema.NewTurn(schema.TurnUserInput, llm.User(text)) }

	environmentEntry := schema.NewTurn(schema.TurnEnvironment, llm.User("ctx"))
	environmentEntry.StableTurnID = "turn_environment_1"
	mutationEntry := plainUser("first")
	mutationEntry.StableTurnID = "turn_client_1"

	cases := []struct {
		name    string
		live    func(*appprojector.AppEventProjector)
		entries []transcript.Entry
	}{
		{
			name: "environment entry",
			live: func(p *appprojector.AppEventProjector) {
				p.Project(events.SessionEvent{Kind: events.EventEnvironment, SessionID: "th_1", Data: events.EnvironmentData{TurnID: "turn_environment_1", Text: "ctx"}})
			},
			entries: []transcript.Entry{{Turn: environmentEntry}, {Turn: plainUser("hi")}},
		},
		{
			name: "client mutation entry",
			live: func(p *appprojector.AppEventProjector) {
				p.ReserveStableTurnID("turn_client_1")
				p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "first", StableTurnID: "turn_client_1"}})
			},
			entries: []transcript.Entry{{Turn: mutationEntry}, {Turn: plainUser("hi")}},
		},
		{
			name: "no stable ids",
			live: func(p *appprojector.AppEventProjector) {
				p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "first"}})
			},
			entries: []transcript.Entry{{Turn: plainUser("first")}, {Turn: plainUser("hi")}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cold := coldTurnIDs(t, tc.entries)
			if len(cold) != 2 {
				t.Fatalf("cold projection produced %v, want one turn per entry", cold)
			}
			if got := liveUserTurnID(tc.live); got != cold[1] {
				t.Fatalf("the trailing user input is %q live and %q cold; the two projections must name it the same turn", got, cold[1])
			}
		})
	}
}

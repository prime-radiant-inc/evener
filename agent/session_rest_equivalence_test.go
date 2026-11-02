package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// TestRestingStateLiveMatchesRestore replays every generated input history
// through the live settle and through a restore of the saved session, and
// requires the two to rest in the same state. Live settle decides from counters
// and the accepted communicate; restore re-derives from the transcript, so a
// divergence here is a session that rests differently after a daemon restart.
func TestRestingStateLiveMatchesRestore(t *testing.T) {
	t.Parallel()
	type move struct {
		name string
		step func(llm.Request) llm.Response
	}
	communicate := func(reason string) move {
		return move{name: "communicate " + reason, step: func(llm.Request) llm.Response { return endReasonResponse("answer", reason) }}
	}
	finals := []move{
		{name: "plain reply", step: func(llm.Request) llm.Response { return agenttest.FinalResponse("answer") }},
		communicate(""),
		communicate("done"),
		communicate("waiting_on_work"),
		communicate("needs_response"),
		{name: "ask_user", step: func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(askUserCall("ask1", askUserArgsValid()))
		}},
	}
	// Each prelude is a first input that leaves the session resting somewhere,
	// so the generated final input runs on top of a non-empty history.
	preludes := []move{
		{name: "fresh session"},
		{name: "after a needs_response rest", step: communicate("needs_response").step},
		{name: "after a done rest", step: communicate("done").step},
		{name: "after a pending question", step: finals[5].step},
	}
	for _, prelude := range preludes {
		for _, final := range finals {
			t.Run(prelude.name+"/"+final.name, func(t *testing.T) {
				t.Parallel()
				var steps []func(llm.Request) llm.Response
				if prelude.step != nil {
					steps = append(steps, prelude.step)
				}
				steps = append(steps, final.step)
				live, restored := restoreAfter(t, steps, func(ctx context.Context, sess *Session) {
					inputs := []string{"final input"}
					if prelude.step != nil {
						inputs = []string{"prelude input", "final input"}
					}
					for _, in := range inputs {
						if _, err := sess.ProcessInput(ctx, in, nil); err != nil {
							t.Fatal(err)
						}
					}
				})
				if live != restored {
					t.Fatalf("live settle rests %q but restore rests %q", live, restored)
				}
			})
		}
	}
}

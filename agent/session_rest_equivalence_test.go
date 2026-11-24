package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// TestRestingStateLiveMatchesRestore replays every generated input history
// through the live settle and through a restore of the saved session, and
// requires the two to rest in the same state. Live settle reads pending asks;
// restore re-derives them from the transcript, so a
// divergence here is a session that rests differently after a daemon restart.
func TestRestingStateLiveMatchesRestore(t *testing.T) {
	t.Parallel()
	type move struct {
		name string
		step func(llm.Request) llm.Response
	}
	finals := []move{
		{name: "plain reply", step: func(llm.Request) llm.Response { return agenttest.FinalResponse("answer") }},
		{name: "quiet wait", step: func(llm.Request) llm.Response {
			return quietYieldResponse("rest-wait", `{"message":"waiting on timer","next":"log_quietly_and_end_turn"}`)
		}},
		{name: "ask_user", step: func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(askUserCall("ask1", askUserArgsValid()))
		}},
	}
	// Each prelude is a first input that leaves the session resting somewhere,
	// so the generated final input runs on top of a non-empty history.
	preludes := []move{
		{name: "fresh session"},
		{name: "after a reply", step: finals[0].step},
		{name: "after a quiet wait", step: finals[1].step},
		{name: "after a pending question", step: finals[2].step},
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
					// A quiet wait on an initiating turn is legitimate only while
					// something can wake the session; arm a timer so every scripted
					// ending is admitted. The watch does not affect the settle state
					// this test compares.
					if _, err := sess.jobManager.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Source: "self", AfterSeconds: 3600}); err != nil {
						t.Fatal(err)
					}
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

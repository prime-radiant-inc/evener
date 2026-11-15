package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The unknown-window contract of issue #4564 at the session layer: a custom
// gateway serving a model nothing vouches for still gets context management.
// a real session folds and discloses the assumption through its own event
// wiring.

// TestUnknownWindowSessionCompactsAndDiscloses drives the real per-request
// path: a session on the unknown-window gateway model folds once its exact API
// measurement outgrows the assumed window, and the assumption is disclosed
// exactly once through the session's own event wiring.
func TestUnknownWindowSessionCompactsAndDiscloses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "gw",
		steps: []func(req llm.Request) llm.Response{
			// First round: a tool call whose reported usage already fills the
			// assumed window, the way a real gateway reports input tokens.
			func(req llm.Request) llm.Response {
				return llm.Response{
					Usage: llm.Usage{InputTokens: 150_000},
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID:        "c1",
								Name:      "read_file",
								Arguments: json.RawMessage(`{"file_path":"big.txt"}`),
							}},
						},
					},
				}
			},
			// A real fold launches auxiliary calls (session naming rides the
			// cheap caller) and then the closing round; every remaining step
			// ends the turn cleanly.
			func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") },
			func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") },
			func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") },
			func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") },
			func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") },
		},
	})
	env := execenv.NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("big.txt", strings.Repeat("line of content\n", 200)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	profile := newOpenAICompatProfile("gw", "deepseek-4.1-flash", 0)
	sess, err := NewSession(c, profile, env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// The fold must be real, not the turn-count no-op a bare tool round
	// produces under the default six preserved turns (the checkpoint layer
	// emits either way): seed a short filler prefix and preserve two recent
	// turns, the way the contextmgr tests size their folds.
	sess.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("earlier task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("earlier reply")),
		schema.NewTurn(schema.TurnUserInput, llm.User("follow-up")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("follow-up reply")),
	}
	sess.contextMgr.PreserveRecentTurns = 2

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	if _, err := sess.ProcessInput(context.Background(), "read the file", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	var compactions, disclosures int
	var firstFold *events.ContextCompactionData
	for _, ev := range evs {
		if ev.Kind == events.EventContextCompaction {
			compactions++
			if c, ok := ev.Data.(events.ContextCompactionData); ok && firstFold == nil {
				firstFold = &c
			}
		}
		if ev.Kind == events.EventWarning {
			if w, ok := ev.Data.(events.WarningData); ok && w.Code == events.WarningCodeContextWindowAssumed {
				disclosures++
			}
		}
	}
	if compactions == 0 {
		t.Fatal("unknown-window session never compacted under the assumed window")
	}
	if firstFold == nil || firstFold.TurnsAfter >= firstFold.TurnsBefore {
		t.Fatalf("compaction was a no-op fold: %+v", firstFold)
	}
	if disclosures != 1 {
		t.Fatalf("assumption disclosures = %d, want exactly 1", disclosures)
	}
}

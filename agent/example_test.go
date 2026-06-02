package agent_test

import (
	"fmt"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// NewSession wires together the four things a session needs: an [llm.Client]
// (the transport), a [agent.ProviderProfile] (selects the model and its
// provider-specific behavior), an [execenv.ExecutionEnvironment] (where tools
// run), and a [agent.SessionConfig].
//
// This example only builds and tears down the session; driving a real turn with
// [agent.Session.ProcessInput] requires a provider adapter registered on the
// client and a network connection.
func ExampleNewSession() {
	client := llm.NewClient()
	profile := agent.NewOpenAIProfile("gpt-5.2")
	env := execenv.NewLocalExecutionEnvironment("/path/to/project")

	cfg := agent.SessionConfig{
		MaxToolRoundsPerInput: 50,
		ReasoningEffort:       "medium",
	}

	sess, err := agent.NewSession(client, profile, env, cfg)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// A real caller would drive the session with sess.ProcessInput (see the doc
	// comment above for why this example stops at construction) and would defer
	// sess.Close() to release resources. Here we close it directly.
	sess.Close()
}

// Session.Events returns a channel of [events.SessionEvent] values reporting a
// turn as it happens. Range over it from a separate goroutine to render
// progress live; each event carries an [events.EventKind] and a typed payload.
// The channel closes when [agent.Session.Close] is called.
func ExampleSession_Events() {
	var sess *agent.Session // obtained from agent.NewSession

	go func() {
		for ev := range sess.Events() {
			switch ev.Kind {
			case events.EventAssistantTextDelta:
				// Incremental assistant text.
				if d, ok := ev.Data.(events.AssistantTextDeltaData); ok {
					fmt.Print(d.Delta)
				}
			case events.EventToolCallStart:
				if d, ok := ev.Data.(events.ToolCallStartData); ok {
					fmt.Printf("\n[tool] %s\n", d.ToolName)
				}
			case events.EventSessionEnd:
				return
			}
		}
	}()
}

// The transcript schema types are plain structs that describe the on-disk
// JSONL transcript. A [agent.TranscriptHeader] is the first line of the file,
// and each subsequent [agent.Turn] is a typed history item. [agent.NewTurn]
// stamps the current time.
func ExampleNewTurn() {
	header := agent.TranscriptHeader{
		SessionID: "sess-123",
		ProfileID: "openai/gpt-5.2",
		Model:     "gpt-5.2",
	}

	turn := agent.NewTurn(agent.TurnUserInput, llm.User("Summarize main.go."))

	fmt.Println(header.SessionID, turn.Kind)
	// Output: sess-123 USER_INPUT
}

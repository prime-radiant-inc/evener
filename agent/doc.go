// Package agent runs an AI agent: it drives a language model through tool-using
// turns until the model produces a final answer.
//
// A [Session] is the unit of work. It pairs three things:
//
//   - an [llm.Client] — the transport to one or more model providers;
//   - a [ProviderProfile] — selects the model and its provider-specific behavior
//     (construct one with, e.g., [NewOpenAIProfile]);
//   - an [ExecutionEnvironment] — where the agent's tools run, such as the local
//     filesystem and shell via [NewLocalExecutionEnvironment].
//
// Build a session with [NewSession] and run a turn with [Session.ProcessInput]:
//
//	sess, err := agent.NewSession(client, agent.NewOpenAIProfile("gpt-5.2"),
//		agent.NewLocalExecutionEnvironment(dir), agent.SessionConfig{})
//	if err != nil {
//		return err
//	}
//	defer sess.Close()
//	out, err := sess.ProcessInput(ctx, "List the Go files and summarize main.go.", nil)
//
// ProcessInput drives the model through as many tool-call rounds as it needs —
// the model requests a tool, the session runs it in the environment, feeds the
// result back, and repeats — and returns the final assistant text. Pass
// [ImageAttachment] values to include images alongside the text input.
//
// # Observing a turn
//
// [Session.Events] returns a channel of [SessionEvent] values that report a turn
// as it happens: assistant text deltas, tool-call starts and output, warnings,
// and a terminal session-end. Each event carries an [EventKind] and a payload;
// range over the channel from a separate goroutine to render progress live.
//
// # Concurrency
//
// One goroutine drives a session through [Session.ProcessInput]. While a turn is
// running, another goroutine may range over [Session.Events] to observe it and
// may call control methods such as [Session.SetModel] concurrently. [Session.Close]
// stops the session and closes the event stream; it is the caller's signal that
// no further turns will run.
package agent

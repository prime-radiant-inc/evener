// Package agent runs an AI agent: it drives a language model through tool-using
// turns until the model produces a final answer.
//
// A [Session] is the unit of work. It pairs three things:
//
//   - an [llm.Client] — the transport to one or more model providers;
//   - a [ProviderProfile] — selects the model and its provider-specific behavior
//     (construct one with, e.g., [NewOpenAIProfile]);
//   - an [execenv.ExecutionEnvironment] — where the agent's tools run, such as the
//     local filesystem and shell via [execenv.NewLocalExecutionEnvironment].
//
// Build a session with [NewSession] and run a turn with [Session.ProcessInput]:
//
//	sess, err := agent.NewSession(client, agent.NewOpenAIProfile("gpt-5.2"),
//		execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{})
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
// [Session.ProcessInput] is not re-entrant: drive each session from a single
// goroutine and run its turns one at a time. While a turn is running, other
// goroutines may observe and steer it. These methods are safe to call
// concurrently with a running turn: [Session.Events], [Session.State],
// [Session.Snapshot], [Session.QueueDepth], [Session.Steer], [Session.Enqueue],
// [Session.SetModel], [Session.SetReasoningEffort], [Session.SetTimeout], and
// [Session.Close].
//
// [Session.Events] returns a channel that [Session.Close] closes, so a range
// loop over it ends when the session closes. Close stops the session and is the
// caller's signal that no further turns will run.
package agent

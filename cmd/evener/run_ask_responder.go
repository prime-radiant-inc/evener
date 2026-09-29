package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
)

// askResponderMaxRounds bounds how many times one `evener run` invocation
// will shell out to --ask-responder for a single prompt: enough for a real
// back-and-forth, not an unbounded loop if the model (or the responder)
// never lets the turn settle.
const askResponderMaxRounds = 3

// askResponderTimeout bounds one responder invocation. A responder that
// never answers must not wedge the run forever.
const askResponderTimeout = 2 * time.Minute

// askCallCapture records each ask_user tool call's raw arguments
// (events.ToolCallStartData.ArgumentsJSON, exactly as the model issued them)
// off the session's event stream. Session.PendingQuestion() bounds its
// result to the wire's shape (one question, labels only) and drops each
// option's detail text, which is not enough for an external responder to
// answer well — this is "wherever the tool-call arguments are available"
// instead. The drain goroutine (teeAskUserCalls) records; the responder loop
// drains between rounds; both only touch it under mu.
type askCallCapture struct {
	mu    sync.Mutex
	calls []string
}

func newAskCallCapture() *askCallCapture { return &askCallCapture{} }

func (c *askCallCapture) record(argsJSON string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, argsJSON)
}

// drain returns every ask_user call recorded since the last drain and clears
// the capture, so the next round starts empty.
func (c *askCallCapture) drain() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.calls
	c.calls = nil
	return out
}

// teeAskUserCalls passes every event from in to the returned channel
// unchanged, recording each ask_user EventToolCallStart's raw arguments into
// capture along the way. run() interposes this between sess.Events() and the
// drain goroutines only when --ask-responder is set.
func teeAskUserCalls(in <-chan events.SessionEvent, capture *askCallCapture) <-chan events.SessionEvent {
	out := make(chan events.SessionEvent)
	go func() {
		defer close(out)
		for ev := range in {
			if ev.Kind == events.EventToolCallStart {
				if d, ok := ev.Data.(events.ToolCallStartData); ok && d.ToolName == "ask_user" {
					capture.record(d.ArgumentsJSON)
				}
			}
			out <- ev
		}
	}()
	return out
}

// runAskResponderLoop answers a session's pending ask_user questions by
// shelling out to cfg.askResponder, feeding it the pending questions as JSON
// on stdin and submitting its stdout as the next user input, until the
// session has no more pending questions or askResponderMaxRounds have run.
// A responder that fails (non-zero exit, empty output, or unparseable
// pending questions) stops the loop without failing the run: it ends exactly
// as it would today, with the question left unanswered.
func runAskResponderLoop(ctx context.Context, sess *agent.Session, cfg runConfig, capture *askCallCapture, result string) (string, error) {
	for round := 0; round < askResponderMaxRounds && sess.HasPendingAsk(); round++ {
		payload, err := askResponderPayload(capture.drain())
		if err != nil {
			fmt.Fprintf(cfg.stderr, "[ask-responder] %v\n", err) //nolint:errcheck
			return result, nil
		}
		answer, err := runAskResponderCommand(ctx, cfg.askResponder, payload)
		if err != nil {
			fmt.Fprintf(cfg.stderr, "[ask-responder] %v\n", err) //nolint:errcheck
			return result, nil
		}
		next, err := runProcessInput(sess, ctx, answer)
		if err != nil {
			return next, err
		}
		result = next
	}
	return result, nil
}

// askResponderPayload builds the responder's stdin: every ask_user call's
// arguments captured this round, parsed into their full questions (options
// and details included).
func askResponderPayload(argsJSONs []string) ([]byte, error) {
	var questions []agent.AskUserQuestion
	for _, raw := range argsJSONs {
		parsed, err := agent.ParseAskUserCallArguments([]byte(raw))
		if err != nil {
			return nil, fmt.Errorf("parsing pending ask_user questions: %w", err)
		}
		questions = append(questions, parsed...)
	}
	if len(questions) == 0 {
		return nil, errors.New("no pending ask_user questions to send the responder")
	}
	return json.Marshal(struct {
		Questions []agent.AskUserQuestion `json:"questions"`
	}{Questions: questions})
}

// runAskResponderCommand runs cfg.askResponder via `sh -c` — the shell form
// the repo's command hooks fall back to for a bare command string
// (agent/internal/hooks/command_runtime.go) — with stdin piped in and a
// bounded timeout, and returns its trimmed stdout. A non-zero exit or empty
// stdout is an error naming the responder's stderr.
func runAskResponderCommand(ctx context.Context, command string, stdin []byte) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, askResponderTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ask-responder command failed: %w\nstderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	answer := strings.TrimSpace(stdout.String())
	if answer == "" {
		return "", fmt.Errorf("ask-responder command produced no output\nstderr: %s", strings.TrimSpace(stderr.String()))
	}
	return answer, nil
}

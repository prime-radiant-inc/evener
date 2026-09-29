package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/execsupport/orphanpipe"
	"primeradiant.com/evener/execsupport/procgroup"
)

// rejectAskResponderWithResume rejects --ask-responder combined with
// --resume or --resume-last, in the style of
// rejectPluginSelectionWithResume (plugin_selection_flag.go): a restored
// session keeps NonInteractive from its persisted snapshot
// (RestoreSessionConfig, agent/session_init.go, carries no override), and a
// restored session's pending ask_user calls, if any, carry no
// askPendingCallArgs (askPendingCallArgs is live-only state, never
// persisted or rebuilt from the transcript on restore) — so the combination
// would silently do nothing rather than ever answer a question.
// --resume-with is deliberately not rejected here, matching
// rejectPluginSelectionWithResume's own scope: it builds a fresh child
// session config rather than reusing a fixed one.
func rejectAskResponderWithResume(askResponder, resume string, resumeLast bool) error {
	if askResponder == "" {
		return nil
	}
	if resume != "" {
		return errors.New("--ask-responder cannot be used with --resume")
	}
	if resumeLast {
		return errors.New("--ask-responder cannot be used with --resume-last")
	}
	return nil
}

// askResponderMaxRounds bounds how many times one `evener run` invocation
// will shell out to --ask-responder for a single prompt: enough for a real
// back-and-forth, not an unbounded loop if the model (or the responder)
// never lets the turn settle.
const askResponderMaxRounds = 3

// askResponderTimeout bounds one responder invocation. A responder that
// never answers must not wedge the run forever.
const askResponderTimeout = 2 * time.Minute

// askResponderWaitDelay bounds how long the responder's output pipes may
// stay open after it exits or its timeout ends, mirroring
// agent/internal/hooks/command_runtime.go's commandHookWaitDelay: a
// responder that backgrounds a job hands the job those pipes, and killing
// the responder on timeout does not by itself kill the job, so without this
// bound the timeout would not bound the responder (see execsupport/orphanpipe).
const askResponderWaitDelay = time.Second

// runAskResponderLoop answers a session's pending ask_user questions by
// shelling out to cfg.askResponder, feeding it the pending questions as JSON
// on stdin and submitting its stdout as the next user input, until the
// session has no more pending questions or askResponderMaxRounds have run.
// A responder that fails on its own (non-zero exit, empty output, or
// unparseable pending questions) stops the loop without failing the run: it
// ends exactly as it would today, with the question left unanswered. A
// responder command that fails because ctx itself ended (--timeout expiry,
// an interrupt) is different: that failure did not come from the responder,
// so it is returned as the run's own error rather than swallowed as a quiet
// stop — the caller asked this run to end, not to keep going unanswered.
// Reaching the round cap with a question still pending is logged the same
// way as an on-its-own responder failure.
func runAskResponderLoop(ctx context.Context, sess *agent.Session, cfg runConfig, result string) (string, error) {
	round := 0
	for ; round < askResponderMaxRounds && sess.HasPendingAsk(); round++ {
		payload, err := askResponderPayload(sess.PendingAskArguments())
		if err != nil {
			fmt.Fprintf(cfg.stderr, "[ask-responder] %v\n", err) //nolint:errcheck
			return result, nil
		}
		answer, err := runAskResponderCommand(ctx, cfg.askResponder, payload, askResponderTimeout)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			fmt.Fprintf(cfg.stderr, "[ask-responder] %v\n", err) //nolint:errcheck
			return result, nil
		}
		next, err := runProcessInput(sess, ctx, answer)
		if err != nil {
			return next, err
		}
		result = next
	}
	if round == askResponderMaxRounds && sess.HasPendingAsk() {
		fmt.Fprintf(cfg.stderr, "[ask-responder] stopping after %d rounds with a question still pending\n", askResponderMaxRounds) //nolint:errcheck
	}
	return result, nil
}

// askResponderPayload builds the responder's stdin: every pending ask_user
// call's own arguments (Session.PendingAskArguments — durable session
// state, not the best-effort event stream), parsed into their full
// questions (options and details included).
func askResponderPayload(pendingCallArgs [][]byte) ([]byte, error) {
	var questions []agent.AskUserQuestion
	for _, raw := range pendingCallArgs {
		parsed, err := agent.ParseAskUserCallArguments(raw)
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

// runAskResponderCommand runs command via `sh -c` — the shell form the
// repo's command hooks fall back to for a bare command string
// (agent/internal/hooks/command_runtime.go) — with stdin piped in and a
// bounded timeout, and returns its trimmed stdout. It owns the responder's
// whole process tree, not just the direct `sh`, and bounds how long its
// output pipes may stay open past the deadline, the same two-part pattern
// command_runtime.go's own command-hook launch uses for the same reason: a
// shell command from outside evener may background a job or run a pipeline
// that would otherwise outlive the timeout and keep this call blocked on
// its output. A non-zero exit or empty stdout is an error naming the
// responder's stderr.
func runAskResponderCommand(ctx context.Context, command string, stdin []byte, timeout time.Duration) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = askResponderWaitDelay

	// Place the responder in its own process group so cancellation can
	// signal the whole tree, not just `sh`: a backgrounded job or pipeline
	// stage CommandContext's default single-process kill would not reach.
	cmd.SysProcAttr = procgroup.SysProcAttr()
	// os/exec calls Cancel at the context deadline from the goroutine Start
	// spawns, so Process is set then; the nil check keeps the kill safe
	// without depending on that stdlib contract.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			procgroup.Kill(cmd.Process.Pid)
		}
		return nil
	}

	runErr := orphanpipe.ChildErr(cmd, cmd.Run())
	// Reap the responder's process group after the invocation ends: a
	// responder that exited 0 while a backgrounded descendant held its
	// pipes is cleaned up here too, not only on the timeout path.
	if cmd.Process != nil {
		procgroup.KillGroupAfterReap(cmd.Process.Pid)
	}
	if runErr != nil {
		return "", fmt.Errorf("ask-responder command failed: %w\nstderr: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	answer := strings.TrimSpace(stdout.String())
	if answer == "" {
		return "", fmt.Errorf("ask-responder command produced no output\nstderr: %s", strings.TrimSpace(stderr.String()))
	}
	return answer, nil
}

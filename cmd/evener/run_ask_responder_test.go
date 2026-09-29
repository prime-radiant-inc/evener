package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// writeFakeResponder writes an executable shell script at dir/name and
// returns its path, for use as --ask-responder. The tests build the fake
// responder as a real script and run it as a real subprocess (no mocked
// exec), matching the LLM-boundary-only faking the rest of this package uses.
func writeFakeResponder(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write fake responder: %v", err)
	}
	return path
}

// TestRunAskResponderFlagSetsInteractiveSession: --ask-responder makes the
// session interactive (NonInteractive false) so ask_user is offered;
// runProvisionSandbox is intercepted to read the config before the run
// aborts, the same seam TestRunPassesResolvedPluginDirsToSessionConfig uses.
func TestRunAskResponderFlagSetsInteractiveSession(t *testing.T) {
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
	oldProvision := runProvisionSandbox
	t.Cleanup(func() { runProvisionSandbox = oldProvision })
	var gotNonInteractive bool
	seen := false
	runProvisionSandbox = func(_ *execenv.LocalExecutionEnvironment, cfg *agent.SessionConfig, _ string) error {
		gotNonInteractive = cfg.NonInteractive
		seen = true
		return errors.New("stop after config")
	}
	err := run(context.Background(), runConfig{
		prompt: "hi", model: "openai/gpt-test", workDir: t.TempDir(), stateDir: t.TempDir(),
		noDefaultMarketplaces: true, askResponder: "/bin/true", stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "stop after config") {
		t.Fatalf("run error = %v", err)
	}
	if !seen {
		t.Fatal("runProvisionSandbox was never called")
	}
	if gotNonInteractive {
		t.Fatal("NonInteractive = true, want false when --ask-responder is set")
	}
}

// TestRunNoAskResponderKeepsNonInteractive: with no --ask-responder, session
// behavior is exactly as today (NonInteractive true).
func TestRunNoAskResponderKeepsNonInteractive(t *testing.T) {
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
	oldProvision := runProvisionSandbox
	t.Cleanup(func() { runProvisionSandbox = oldProvision })
	var gotNonInteractive bool
	seen := false
	runProvisionSandbox = func(_ *execenv.LocalExecutionEnvironment, cfg *agent.SessionConfig, _ string) error {
		gotNonInteractive = cfg.NonInteractive
		seen = true
		return errors.New("stop after config")
	}
	err := run(context.Background(), runConfig{
		prompt: "hi", model: "openai/gpt-test", workDir: t.TempDir(), stateDir: t.TempDir(),
		noDefaultMarketplaces: true, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "stop after config") {
		t.Fatalf("run error = %v", err)
	}
	if !seen {
		t.Fatal("runProvisionSandbox was never called")
	}
	if !gotNonInteractive {
		t.Fatal("NonInteractive = false, want true when --ask-responder is unset")
	}
}

// TestRunAskResponderAnswersQuestionAndContinues: the model asks a
// question, the fake responder answers it from real stdin JSON, the answer
// reaches the session as the next user input, and the turn continues to a
// final communicate.
func TestRunAskResponderAnswersQuestionAndContinues(t *testing.T) {
	adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return scriptedToolCalls(scriptedAskUserCall("ask1", "", "Which database?", serveAskOption{Label: "Postgres", Detail: "Postgres"}, serveAskOption{Label: "SQLite", Detail: "SQLite"}))
		},
		func(llm.Request) llm.Response { return scriptedCommunicate("used the answer") },
	}}
	installRunScriptedProvider(t, adapter)

	dir := t.TempDir()
	capturePath := filepath.Join(dir, "captured-stdin.json")
	responder := writeFakeResponder(t, dir, "responder.sh",
		"cat > "+capturePath+"\necho 'Go with SQLite, it is simpler.'\n")

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt: "help me pick a db", model: "openai/gpt-test", workDir: t.TempDir(), stateDir: t.TempDir(),
		noDefaultMarketplaces: true, askResponder: responder, stdout: &stdout, stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("run error = %v (stderr: %s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "used the answer") {
		t.Fatalf("stdout = %q, want the final communicate message", stdout.String())
	}

	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("responder never received stdin: %v", err)
	}
	if !strings.Contains(string(captured), "Which database?") {
		t.Fatalf("captured stdin = %q, want the full question text", captured)
	}
	if !strings.Contains(string(captured), "Postgres") || !strings.Contains(string(captured), "SQLite") {
		t.Fatalf("captured stdin = %q, want both option labels", captured)
	}

	reqs := adapter.Requests()
	if len(reqs) != 2 {
		t.Fatalf("provider saw %d requests, want 2 (ask round + answer round)", len(reqs))
	}
	last := reqs[1].Messages
	found := false
	for _, m := range last {
		if strings.Contains(m.Text(), "Go with SQLite") {
			found = true
		}
	}
	if !found {
		t.Fatalf("second request never carried the responder's answer: %+v", last)
	}
}

// TestRunAskResponderStopsAfterRoundCap: the model keeps asking forever; the
// responder loop stops after 3 rounds rather than looping indefinitely.
func TestRunAskResponderStopsAfterRoundCap(t *testing.T) {
	askStep := func(llm.Request) llm.Response {
		return scriptedToolCalls(scriptedAskUserCall("ask", "", "Continue?", serveAskOption{Label: "Yes", Detail: "Yes"}, serveAskOption{Label: "No", Detail: "No"}))
	}
	adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{
		askStep, askStep, askStep, askStep, askStep, askStep,
	}}
	installRunScriptedProvider(t, adapter)

	dir := t.TempDir()
	counterPath := filepath.Join(dir, "count")
	responder := writeFakeResponder(t, dir, "responder.sh",
		"n=$(cat "+counterPath+" 2>/dev/null || echo 0)\n"+
			"n=$((n+1))\n"+
			"echo \"$n\" > "+counterPath+"\n"+
			"cat >/dev/null\n"+
			"echo \"answer $n\"\n")

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt: "loop", model: "openai/gpt-test", workDir: t.TempDir(), stateDir: t.TempDir(),
		noDefaultMarketplaces: true, askResponder: responder, stdout: &stdout, stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("run error = %v (stderr: %s)", err, stderr.String())
	}
	data, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("responder counter file missing: %v", err)
	}
	got, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if convErr != nil {
		t.Fatalf("counter file %q is not a number: %v", data, convErr)
	}
	if got != 3 {
		t.Fatalf("responder ran %d times, want exactly 3 (the round cap)", got)
	}
	// 1 initial round + 3 capped rounds = 4 provider requests.
	if reqs := adapter.Requests(); len(reqs) != 4 {
		t.Fatalf("provider saw %d requests, want 4", len(reqs))
	}
	if !strings.Contains(stderr.String(), "[ask-responder]") {
		t.Fatalf("stderr = %q, want a logged [ask-responder] message when the round cap is reached with a question still pending", stderr.String())
	}
}

// TestRunAskResponderStopsOnNonZeroExit: a responder that exits non-zero
// stops the asking loop; the run ends as it would without a responder,
// with the question unanswered, and the responder's stderr is logged.
func TestRunAskResponderStopsOnNonZeroExit(t *testing.T) {
	adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return scriptedToolCalls(scriptedAskUserCall("ask1", "", "Ship today?", serveAskOption{Label: "Yes", Detail: "Yes"}, serveAskOption{Label: "No", Detail: "No"}))
		},
	}}
	installRunScriptedProvider(t, adapter)

	dir := t.TempDir()
	responder := writeFakeResponder(t, dir, "responder.sh",
		"cat >/dev/null\necho 'boom' >&2\nexit 3\n")

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt: "ship it", model: "openai/gpt-test", workDir: t.TempDir(), stateDir: t.TempDir(),
		noDefaultMarketplaces: true, askResponder: responder, stdout: &stdout, stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("run error = %v, want the run to end cleanly with the question unanswered", err)
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Fatalf("stderr = %q, want the responder's stderr logged", stderr.String())
	}
	if len(adapter.Requests()) != 1 {
		t.Fatalf("provider saw %d requests, want 1 (no answer round after the failure)", len(adapter.Requests()))
	}
}

// TestRunAskResponderStopsOnEmptyOutput: a responder that exits 0 but
// prints nothing also stops the loop.
func TestRunAskResponderStopsOnEmptyOutput(t *testing.T) {
	adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return scriptedToolCalls(scriptedAskUserCall("ask1", "", "Ship today?", serveAskOption{Label: "Yes", Detail: "Yes"}, serveAskOption{Label: "No", Detail: "No"}))
		},
	}}
	installRunScriptedProvider(t, adapter)

	dir := t.TempDir()
	responder := writeFakeResponder(t, dir, "responder.sh", "cat >/dev/null\n")

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt: "ship it", model: "openai/gpt-test", workDir: t.TempDir(), stateDir: t.TempDir(),
		noDefaultMarketplaces: true, askResponder: responder, stdout: &stdout, stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("run error = %v, want the run to end cleanly with the question unanswered", err)
	}
	if len(adapter.Requests()) != 1 {
		t.Fatalf("provider saw %d requests, want 1 (no answer round after empty output)", len(adapter.Requests()))
	}
}

// TestRunAskResponderLoopReturnsCancellationInsteadOfSwallowingIt: when the
// responder command fails because the caller's context was cancelled
// (--timeout expiry, an interrupt), the loop must return that cancellation
// as the run's error, not silently succeed the way an ordinary responder
// failure (non-zero exit, empty output) does.
func TestRunAskResponderLoopReturnsCancellationInsteadOfSwallowingIt(t *testing.T) {
	adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return scriptedToolCalls(scriptedAskUserCall("ask1", "", "Ship today?", serveAskOption{Label: "Yes", Detail: "Yes"}, serveAskOption{Label: "No", Detail: "No"}))
		},
	}}
	client := scriptedRegistryClient(t, adapter)
	sess, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-test"), execenv.NewLocalExecutionEnvironment(t.TempDir()), agent.SessionConfig{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer setupCancel()
	if _, err := sess.ProcessInput(setupCtx, "ship it", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !sess.HasPendingAsk() {
		t.Fatal("want a pending ask_user question before exercising the responder loop")
	}

	dir := t.TempDir()
	// A responder that blocks past the caller's own deadline.
	responder := writeFakeResponder(t, dir, "responder.sh", "cat >/dev/null\nsleep 5\necho too-late\n")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, loopErr := runAskResponderLoop(ctx, sess, runConfig{askResponder: responder, stderr: &bytes.Buffer{}}, "")
	<-ctx.Done() // the deadline has definitely passed by the time we assert below

	if loopErr == nil {
		t.Fatal("want the run's cancellation returned as an error, not swallowed as success")
	}
	if !errors.Is(loopErr, context.DeadlineExceeded) {
		t.Fatalf("loop error = %v, want context.DeadlineExceeded (or wrapping it)", loopErr)
	}
}

// TestRejectAskResponderWithResume mirrors
// TestPluginSelectionResumeConflicts's table: --ask-responder combined with
// --resume or --resume-last is rejected up front, because a restored
// session keeps NonInteractive from its persisted snapshot
// (RestoreSessionConfig carries no override) and its pending ask_user calls
// (if any) carry no askPendingCallArgs — the flag would silently do
// nothing rather than ever ask.
func TestRejectAskResponderWithResume(t *testing.T) {
	for _, test := range []struct {
		name       string
		resume     string
		resumeLast bool
		wantErr    bool
	}{
		{name: "resume", resume: "session", wantErr: true},
		{name: "resume-last", resumeLast: true, wantErr: true},
		{name: "neither", wantErr: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := rejectAskResponderWithResume("./responder.sh", test.resume, test.resumeLast)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
	if err := rejectAskResponderWithResume("", "session", true); err != nil {
		t.Fatalf("omitted --ask-responder rejected: %v", err)
	}
}

// TestRunRejectsAskResponderWithResume: run() itself refuses the
// combination before doing anything else (no provider load, no session),
// the same place it already refuses --enabled-plugins with --resume.
func TestRunRejectsAskResponderWithResume(t *testing.T) {
	err := run(context.Background(), runConfig{
		askResponder: "./responder.sh", resume: "some-session-id",
		workDir: t.TempDir(), stateDir: t.TempDir(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "--ask-responder") {
		t.Fatalf("run error = %v, want a clear --ask-responder/--resume rejection", err)
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// serveAskOption is one {label, detail} option for a scripted ask_user call
// (spec §4.2's questions[].options[] shape).
type serveAskOption struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// scriptedAskUserCall builds a single-question ask_user tool call. There is
// no shared scriptedAskUserCall helper yet in scripted_provider_test.go
// (unlike scriptedWriteFileCall/scriptedUpdateGoalCall); this file scripts
// ask_user in all three tests below, so it earns its own local helper rather
// than three inline JSON-string literals.
func scriptedAskUserCall(id, header, question string, options ...serveAskOption) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"header":   header,
				"question": question,
				"options":  options,
			},
		},
	})
	return llm.ToolCallData{ID: id, Name: "ask_user", Arguments: args, Type: "function"}
}

// scriptedBackgroundShellCall builds a shell tool call with background:true
// (agent/internal/tool/definitions.go's DefShell schema), matching the shape
// scripted throughout agent/job_watch_observer_test.go.
func scriptedBackgroundShellCall(id, command string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{"command": command, "background": true})
	return llm.ToolCallData{ID: id, Name: "shell", Arguments: args, Type: "function"}
}

// serveStatusState is the one field of server.StatusInfo (server/server.go's
// GET /status response, server/server.go:79-90) these tests assert on.
type serveStatusState struct {
	State string `json:"state"`
}

// getServeAskStatus performs one GET /status and decodes the response,
// failing the test on any transport or decode error. Use
// waitForServeAskStatusUp instead when the daemon may not be listening yet
// (e.g. immediately after a restart).
func getServeAskStatus(t *testing.T, addr string) serveStatusState {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()
	var status serveStatusState
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode /status response: %v", err)
	}
	return status
}

// waitForServeAskStatusUp retries GET /status, tolerating connection-refused
// while the HTTP server finishes binding after a restart, and returns the
// FIRST successfully decoded response WITHOUT retrying on its state value.
// Callers assert on that first response directly: spec §5.4 requires the
// serve-level SetState-after-restore fix to make /status report the restored
// state immediately, not idle-until-next-turn, so retrying past an unwanted
// state here would hide a regression instead of proving its absence.
func waitForServeAskStatusUp(t *testing.T, addr string, timeout time.Duration) serveStatusState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/status")
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		var status serveStatusState
		decodeErr := json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()
		if decodeErr != nil {
			t.Fatalf("decode /status response: %v", decodeErr)
		}
		return status
	}
	t.Fatalf("daemon at %s never accepted a /status connection within %s (last dial error: %v)", addr, timeout, lastErr)
	return serveStatusState{}
}

// pollServeAskStatusUntil polls GET /status every interval until it reports
// want or timeout elapses, failing with the last observed state otherwise.
func pollServeAskStatusUntil(t *testing.T, addr, want string, timeout, interval time.Duration) serveStatusState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last serveStatusState
	for time.Now().Before(deadline) {
		last = getServeAskStatus(t, addr)
		if last.State == want {
			return last
		}
		time.Sleep(interval)
	}
	t.Fatalf("status did not reach %q within %s; last observed state %q", want, timeout, last.State)
	return serveStatusState{}
}

// TestServeAsk_StatusAwaitingAtRest proves spec §8's serve-level claim: GET
// /status — the endpoint the hub prober actually polls, not just the
// in-process appStatus function — reports "awaiting" while a question is
// pending and "idle" once the reply resolves it. This drives the real serve
// daemon (installServeScriptedProvider + runServe, the
// TestServeGoal_TUIPathEndToEnd harness) rather than the Session directly,
// because the serve-level turn-end SetState wiring (cmd/serf/serve.go) is a
// seam agenttest cannot reach.
func TestServeAsk_StatusAwaitingAtRest(t *testing.T) {
	workDir := t.TempDir()
	stateDir := t.TempDir()
	runDir := t.TempDir()

	installServeScriptedProvider(t, &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return scriptedToolCalls(scriptedAskUserCall("ask1", "DB", "Which datastore for the ingest path?",
					serveAskOption{Label: "Postgres", Detail: "matches prod; heavier local setup"},
					serveAskOption{Label: "SQLite", Detail: "zero setup; diverges from prod"}))
			},
			func(llm.Request) llm.Response {
				return scriptedCommunicate("answered")
			},
		},
	})

	args := []string{
		"--model", "openai/gpt-test",
		"--addr", "127.0.0.1:0",
		"--dir", workDir,
		"--state-dir", stateDir,
		"--run-dir", runDir,
	}

	done := make(chan error, 1)
	go func() {
		done <- runServe(args)
	}()

	entry := waitForServeTestRendezvous(t, runDir)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	defer client.Close()

	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serve-ask-at-rest-test", Version: "test"},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()

	if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
		Ref:   ref,
		Input: []appwire.InputItem{{Type: "text", Text: "which db should we use?"}},
	}); err != nil {
		t.Fatalf("TurnStart (question): %v", err)
	}

	// The asking round ends the turn at its boundary (spec §5.1); /status must
	// report "awaiting" at rest.
	pollServeAskStatusUntil(t, entry.Address, "awaiting", 10*time.Second, 100*time.Millisecond)

	// The reply is the next user message on the SAME input path (spec §5.2): an
	// ordinary turn/start, not a new wire method.
	if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
		Ref:   ref,
		Input: []appwire.InputItem{{Type: "text", Text: "[answers]\n1. [DB] → \"Postgres\""}},
	}); err != nil {
		t.Fatalf("TurnStart (reply): %v", err)
	}

	pollServeAskStatusUntil(t, entry.Address, "idle", 10*time.Second, 100*time.Millisecond)

	httpResp, err := http.Post("http://"+entry.Address+"/shutdown", "", nil)
	if err != nil {
		t.Fatalf("post /shutdown: %v", err)
	}
	httpResp.Body.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not exit after /shutdown")
	}
}

// TestServeAsk_NoFlickerOnJobCompletion proves spec §5.3's entry gate holds at
// the serve level: a background job that completes WHILE the session rests
// awaiting must never cause GET /status to read anything but "awaiting" — no
// "active" turn, no "idle" collapse — because the entry gate refuses the
// resulting notification wake before any state transition
// (agent/session_lifecycle.go's ProcessInputKind). The job is started in the
// SAME round as the ask_user call so it is genuinely still running in the
// background by the time the round's boundary rests the session awaiting,
// putting its completion (and the notification that follows) inside the
// observation window below.
func TestServeAsk_NoFlickerOnJobCompletion(t *testing.T) {
	workDir := t.TempDir()
	stateDir := t.TempDir()
	runDir := t.TempDir()
	markerPath := filepath.Join(workDir, "job-done.marker")

	installServeScriptedProvider(t, &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return scriptedToolCalls(
					scriptedBackgroundShellCall("job1", "sleep 0.3 && echo done > '"+markerPath+"'"),
					scriptedAskUserCall("ask1", "DB", "Which datastore for the ingest path?",
						serveAskOption{Label: "Postgres", Detail: "matches prod; heavier local setup"},
						serveAskOption{Label: "SQLite", Detail: "zero setup; diverges from prod"}),
				)
			},
		},
	})

	args := []string{
		"--model", "openai/gpt-test",
		"--addr", "127.0.0.1:0",
		"--dir", workDir,
		"--state-dir", stateDir,
		"--run-dir", runDir,
	}

	done := make(chan error, 1)
	go func() {
		done <- runServe(args)
	}()

	entry := waitForServeTestRendezvous(t, runDir)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	defer client.Close()

	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serve-ask-no-flicker-test", Version: "test"},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()

	if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
		Ref:   ref,
		Input: []appwire.InputItem{{Type: "text", Text: "which db should we use? kick off the background prep too"}},
	}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}

	// The round posted the ask alongside the still-running background job; the
	// turn ends at that round's boundary (spec §5.1) with the job still in
	// flight.
	pollServeAskStatusUntil(t, entry.Address, "awaiting", 10*time.Second, 100*time.Millisecond)

	// Poll through the job's completion + notification window: every read must
	// stay "awaiting". The marker file's appearance proves the job actually
	// completed during this window, so a pass here is not vacuous (the job
	// never running would make "it stayed awaiting" meaningless).
	const pollWindow = 2 * time.Second
	const pollInterval = 50 * time.Millisecond
	deadline := time.Now().Add(pollWindow)
	sawJobDone := false
	reads := 0
	for time.Now().Before(deadline) {
		st := getServeAskStatus(t, entry.Address)
		reads++
		if st.State != "awaiting" {
			t.Fatalf("status flickered to %q on read #%d during the job-completion window (job marker present=%v); want it to stay %q throughout", st.State, reads, sawJobDone, "awaiting")
		}
		if _, statErr := os.Stat(markerPath); statErr == nil {
			sawJobDone = true
		}
		time.Sleep(pollInterval)
	}
	if !sawJobDone {
		t.Fatalf("background job marker %s never appeared during the %s poll window; the job may not have run, which would make the no-flicker assertion vacuous", markerPath, pollWindow)
	}
	t.Logf("status stayed %q across %d reads over %s; job completion observed mid-window", "awaiting", reads, pollWindow)

	httpResp, err := http.Post("http://"+entry.Address+"/shutdown", "", nil)
	if err != nil {
		t.Fatalf("post /shutdown: %v", err)
	}
	httpResp.Body.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not exit after /shutdown")
	}
}

// TestServeAsk_RestoreReportsAwaitingImmediately proves spec §5.4's restore
// contract at the serve level: a daemon restarted with a pending ask_user
// question at the transcript tail must report "awaiting" on the very FIRST
// successful GET /status after restart — never an idle-until-next-turn
// window. This is Task 8's two-touchpoint fix
// (RestoreSessionFromMetaWithConfig's deriveRestoredState +
// cmd/serf/serve.go's post-restore srv.SetState belt-and-suspenders write),
// exercised here through the real daemon rather than the Session or Bridge
// directly — the seam agenttest-level tests cannot reach.
func TestServeAsk_RestoreReportsAwaitingImmediately(t *testing.T) {
	workDir := t.TempDir()
	stateDir := t.TempDir()
	runDir := t.TempDir()

	installServeScriptedProvider(t, &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return scriptedToolCalls(scriptedAskUserCall("ask1", "DB", "Which datastore for the ingest path?",
					serveAskOption{Label: "Postgres", Detail: "matches prod; heavier local setup"},
					serveAskOption{Label: "SQLite", Detail: "zero setup; diverges from prod"}))
			},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	firstArgs := []string{
		"--model", "openai/gpt-test",
		"--addr", "127.0.0.1:0",
		"--dir", workDir,
		"--state-dir", stateDir,
		"--run-dir", runDir,
	}

	done1 := make(chan error, 1)
	go func() {
		done1 <- runServe(firstArgs)
	}()

	entry1 := waitForServeTestRendezvous(t, runDir)

	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry1.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))

	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serve-ask-restore-test", Version: "test"},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: entry1.SessionID}.String()

	if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
		Ref:   ref,
		Input: []appwire.InputItem{{Type: "text", Text: "which db should we use?"}},
	}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}

	pollServeAskStatusUntil(t, entry1.Address, "awaiting", 10*time.Second, 100*time.Millisecond)
	client.Close()

	// Kill the daemon with the ask still pending at the transcript tail.
	httpResp, err := http.Post("http://"+entry1.Address+"/shutdown", "", nil)
	if err != nil {
		t.Fatalf("post /shutdown: %v", err)
	}
	httpResp.Body.Close()
	select {
	case err := <-done1:
		if err != nil {
			t.Fatalf("first runServe returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first runServe did not exit after /shutdown")
	}

	// Restart, resuming the SAME session by ID from the SAME state dir.
	secondArgs := []string{
		"--model", "openai/gpt-test",
		"--addr", "127.0.0.1:0",
		"--resume", entry1.SessionID,
		"--dir", workDir,
		"--state-dir", stateDir,
		"--run-dir", runDir,
	}

	done2 := make(chan error, 1)
	go func() {
		done2 <- runServe(secondArgs)
	}()

	entry2 := waitForServeTestRendezvous(t, runDir)
	if entry2.SessionID != entry1.SessionID {
		t.Fatalf("resumed session id = %q, want %q (same session restored)", entry2.SessionID, entry1.SessionID)
	}

	// The FIRST successful /status read after restart must already say
	// awaiting — connection-refused retries are fine while the daemon boots,
	// but no idle-until-next-turn window is allowed once it answers.
	first := waitForServeAskStatusUp(t, entry2.Address, 10*time.Second)
	if first.State != "awaiting" {
		t.Fatalf("first /status read after restore = %q, want %q (no idle-until-next-turn window)", first.State, "awaiting")
	}

	httpResp2, err := http.Post("http://"+entry2.Address+"/shutdown", "", nil)
	if err != nil {
		t.Fatalf("post /shutdown (second daemon): %v", err)
	}
	httpResp2.Body.Close()
	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("second runServe returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second runServe did not exit after /shutdown")
	}
}

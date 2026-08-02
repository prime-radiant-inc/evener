package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

func scriptedForegroundShellCall(id, command string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{
		"command":    command,
		"background": false,
	})
	return llm.ToolCallData{ID: id, Name: "shell", Arguments: args, Type: "function"}
}

func waitForTranscriptKind(path, kind string, timeout time.Duration) ([]byte, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 && bytes.Contains(data, []byte(`"kind":"`+kind+`"`)) {
			return data, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, false
}

type foregroundShellServeMode string

const (
	foregroundShellNoSubscriber foregroundShellServeMode = "no-subscriber"
	foregroundShellSubscriber   foregroundShellServeMode = "subscriber"
	foregroundShellColdReattach foregroundShellServeMode = "cold-reattach"
)

type serveShellNotificationSignals struct {
	started       chan struct{}
	completed     chan struct{}
	turnComplete  chan struct{}
	startedOnce   sync.Once
	completedOnce sync.Once
	turnOnce      sync.Once
}

func watchServeShellNotifications(client *appwire.Client, callID string) *serveShellNotificationSignals {
	signals := &serveShellNotificationSignals{
		started:      make(chan struct{}),
		completed:    make(chan struct{}),
		turnComplete: make(chan struct{}),
	}
	go func() {
		for notification := range client.Notifications() {
			switch notification.Method {
			case appwire.NotifyItemStarted, appwire.NotifyItemCompleted:
				var params appwire.ItemLifecycleParams
				if json.Unmarshal(notification.Params, &params) != nil || params.Item.CallID != callID {
					continue
				}
				if notification.Method == appwire.NotifyItemStarted {
					signals.startedOnce.Do(func() { close(signals.started) })
				} else {
					signals.completedOnce.Do(func() { close(signals.completed) })
				}
			case appwire.NotifyTurnCompleted:
				signals.turnOnce.Do(func() { close(signals.turnComplete) })
			}
		}
	}()
	return signals
}

func requestContainsToolResult(req llm.Request, callID string) bool {
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				return true
			}
		}
	}
	return false
}

func waitForServeState(addr, want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/status")
		if err == nil {
			var status struct {
				State string `json:"state"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&status)
			resp.Body.Close()
			if decodeErr == nil && status.State == want {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func dialServeToolResultClient(ctx context.Context, t *testing.T, addr, name, ref string) (*appwire.Client, *serveShellNotificationSignals) {
	t.Helper()
	transport, err := appwire.DialWebSocket(ctx, "ws://"+addr+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: name, Version: "test"},
	}); err != nil {
		client.Close()
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
		client.Close()
		t.Fatalf("ThreadRead subscribe: %v", err)
	}
	return client, watchServeShellNotifications(client, "shell_1")
}

// runServeForegroundShellPersistenceCase exercises one production Hub path
// variant. The second scripted model request is only possible after the
// tool-result round has been appended, and the transcript assertion proves the
// durable record exists rather than relying on the live projection alone.
func runServeForegroundShellPersistenceCase(t *testing.T, mode foregroundShellServeMode) {
	t.Helper()
	workDir := t.TempDir()
	stateDir := t.TempDir()
	runDir := t.TempDir()
	transcriptPath := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(transcriptPath, 0o700); err != nil {
		t.Fatalf("mkdir transcript directory: %v", err)
	}

	gatePath := filepath.Join(workDir, "shell-release")
	shellCommand := fmt.Sprintf("while [ ! -f %s ]; do sleep 0.01; done; printf shell-complete", strconv.Quote(gatePath))
	secondRequest := make(chan struct{})
	var secondOnce sync.Once
	secondRequestHasToolResult := false
	installServeScriptedProvider(t, &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return scriptedToolCalls(scriptedForegroundShellCall("shell_1", shellCommand))
			},
			func(req llm.Request) llm.Response {
				secondRequestHasToolResult = requestContainsToolResult(req, "shell_1")
				secondOnce.Do(func() { close(secondRequest) })
				return scriptedCommunicate("shell persisted")
			},
		},
	})

	done := make(chan error, 1)
	go func() {
		done <- runServe([]string{
			"--model", "openai/gpt-test",
			"--addr", "127.0.0.1:0",
			"--dir", workDir,
			"--state-dir", stateDir,
			"--run-dir", runDir,
			"--no-project-prompts",
		})
	}()

	entry := waitForServeTestRendezvous(t, runDir)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()
	var client *appwire.Client
	var signals *serveShellNotificationSignals
	if mode != foregroundShellNoSubscriber {
		client, signals = dialServeToolResultClient(ctx, t, entry.Address, string(mode), ref)
		defer client.Close()
		if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
			ClientMutationID: "foreground-shell-turn",
			Ref:              ref,
			Input:            []appwire.InputItem{{Type: "text", Text: "run the foreground shell"}},
		}); err != nil {
			t.Fatalf("TurnStart: %v", err)
		}
	} else {
		body := strings.NewReader(`{"text":"run the foreground shell"}`)
		resp, err := http.Post("http://"+entry.Address+"/input", "application/json", body)
		if err != nil {
			t.Fatalf("POST /input: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("POST /input status = %d, want %d", resp.StatusCode, http.StatusAccepted)
		}
	}

	if mode == foregroundShellColdReattach {
		select {
		case <-signals.started:
		case <-ctx.Done():
			t.Fatalf("foreground shell never emitted item/started: %v", ctx.Err())
		}
		client.Close()
		client, signals = dialServeToolResultClient(ctx, t, entry.Address, "cold-reattach", ref)
		defer client.Close()
	}
	if err := os.WriteFile(gatePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release shell gate: %v", err)
	}

	select {
	case <-secondRequest:
	case <-ctx.Done():
		t.Fatalf("second provider request after foreground shell: %v", ctx.Err())
	}
	if !secondRequestHasToolResult {
		t.Fatal("second provider request did not contain the foreground shell tool result")
	}

	path := filepath.Join(transcriptPath, entry.SessionID+".transcript.jsonl")
	data, ok := waitForTranscriptKind(path, "TOOL_RESULTS", time.Second)
	if !ok {
		t.Fatalf("transcript %s never contained TOOL_RESULTS", path)
	}
	if !bytes.Contains(data, []byte(`"kind":"TOOL_RESULTS"`)) {
		t.Fatalf("transcript data lost TOOL_RESULTS after the wait")
	}
	if mode != foregroundShellNoSubscriber {
		select {
		case <-signals.completed:
		case <-ctx.Done():
			t.Fatalf("foreground shell never emitted item/completed: %v", ctx.Err())
		}
		select {
		case <-signals.turnComplete:
		case <-ctx.Done():
			t.Fatalf("foreground shell turn never completed: %v", ctx.Err())
		}
	} else if !waitForServeState(entry.Address, "awaiting", time.Second) {
		t.Fatal("no-subscriber foreground shell turn never settled to awaiting")
	}

	shutdownResp, err := http.Post("http://"+entry.Address+"/shutdown", "", nil)
	if err != nil {
		t.Fatalf("POST /shutdown: %v", err)
	}
	shutdownResp.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not exit after shutdown")
	}
}

// TestRunServeForegroundShellPersistsToolResults exercises the production Hub
// path with no subscriber, an active subscriber, and a subscriber that is
// replaced while the foreground process is still running. Each variant proves
// the same four checkpoints: TOOL_CALL_END reaches AppWire when observed,
// TOOL_RESULTS is durable, the next model request carries it, and the turn
// settles instead of remaining active.
func TestRunServeForegroundShellPersistsToolResults(t *testing.T) {
	for _, mode := range []foregroundShellServeMode{
		foregroundShellNoSubscriber,
		foregroundShellSubscriber,
		foregroundShellColdReattach,
	} {
		t.Run(string(mode), func(t *testing.T) {
			runServeForegroundShellPersistenceCase(t, mode)
		})
	}
}

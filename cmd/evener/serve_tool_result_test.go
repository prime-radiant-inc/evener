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

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func scriptedForegroundShellCall(id, command string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{
		"command": command,
		"mode":    "foreground",
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

// The fixture shell and its deterministic child each publish their PID under
// these names in the fixture working directory. They are shared with the Unix
// cleanup regression, which derives the child's path from the shell's.
const (
	foregroundShellPIDFilename      = "shell.pid"
	foregroundShellChildPIDFilename = "shell-child.pid"
)

type serveShellNotificationSignals struct {
	started       chan struct{}
	completed     chan struct{}
	turnComplete  chan struct{}
	rested        chan struct{}
	startedOnce   sync.Once
	completedOnce sync.Once
	turnOnce      sync.Once
	restedOnce    sync.Once
}

func watchServeShellNotifications(client *appwire.Client, callID string) *serveShellNotificationSignals {
	signals := &serveShellNotificationSignals{
		started:      make(chan struct{}),
		completed:    make(chan struct{}),
		turnComplete: make(chan struct{}),
		rested:       make(chan struct{}),
	}
	go func() {
		for notification := range client.Notifications() {
			switch notification.Method {
			case appwire.NotifyOverlayUpserted:
				// The running call's tool state in the overlay.
				var params appwire.OverlayUpsertedParams
				if json.Unmarshal(notification.Params, &params) == nil && params.Item.CallID == callID {
					signals.startedOnce.Do(func() { close(signals.started) })
				}
			case appwire.NotifyHistoryUpdated:
				// The recorded call item settled by its TOOL_RESULTS entry, and
				// the recorded completion of the turn.
				var params appwire.HistoryUpdatedParams
				if json.Unmarshal(notification.Params, &params) != nil {
					continue
				}
				for _, item := range params.Items {
					if item.CallID == callID && item.Status == appwire.TurnStatusCompleted {
						signals.completedOnce.Do(func() { close(signals.completed) })
					}
				}
				for _, turn := range params.Turns {
					if turn.ID != appwire.SystemPreludeTurnID && turn.Status == appwire.TurnStatusCompleted {
						signals.turnOnce.Do(func() { close(signals.turnComplete) })
					}
				}
			case appwire.NotifyThreadStatusChanged:
				var params appwire.ThreadStatusChangedParams
				if json.Unmarshal(notification.Params, &params) == nil && params.Status.Type == appwire.ThreadStatusIdle {
					signals.restedOnce.Do(func() { close(signals.rested) })
				}
			}
		}
	}()
	return signals
}

func requestToolResult(req llm.Request, callID string) (*llm.ToolResultData, bool) {
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				return part.ToolResult, true
			}
		}
	}
	return nil, false
}

func transcriptToolResult(data []byte, callID string) (*llm.ToolResultData, bool) {
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		entry, err := transcript.DecodeEntry(bytes.TrimSpace(line))
		if err != nil {
			continue
		}
		for _, part := range entry.Turn.Message.Content {
			if part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				return part.ToolResult, true
			}
		}
	}
	return nil, false
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
	runServeForegroundShellPersistenceCaseWithCheckpoint(t, mode, nil)
}

// foregroundCleanupJoinDeadline bounds the fixture's own join during cleanup,
// the backstop for a case that fails before its explicit shutdown.
const foregroundCleanupJoinDeadline = 15 * time.Second

// runServeForegroundShellPersistenceCaseWithCheckpoint is
// runServeForegroundShellPersistenceCase with a test-only observation seam. The
// checkpoint reports lifecycle facts — the fixture shell writing its PID before
// it waits, the second provider request, and the serve join — and never performs
// cleanup. A nil checkpoint is the wrapper's path; the outer cleanup regression
// drives the failure checkpoints through a re-executed child instead.
func runServeForegroundShellPersistenceCaseWithCheckpoint(t *testing.T, mode foregroundShellServeMode, checkpoint func(stage, shellPIDPath string)) {
	t.Helper()
	if checkpoint == nil {
		checkpoint = func(string, string) {}
	}
	workDir := t.TempDir()
	stateDir := t.TempDir()
	runDir := t.TempDir()
	transcriptPath := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(transcriptPath, 0o700); err != nil {
		t.Fatalf("mkdir transcript directory: %v", err)
	}

	// The fixture shell publishes its own PID and its deterministic long-lived
	// child's PID before it waits, so the seam and the outer regression can
	// identify the exact processes a failing run must not outlive. The shell owns
	// and reaps the child itself on both the gate-release (normal) path and the
	// TERM path, so descendant reaping never depends on host init or a subreaper.
	// The PID files are private to the working directory.
	shellPIDPath := filepath.Join(workDir, foregroundShellPIDFilename)
	shellChildPIDPath := filepath.Join(workDir, foregroundShellChildPIDFilename)
	gatePath := filepath.Join(workDir, "shell-release")
	shellCommand := fmt.Sprintf(
		"sleep 600 & child=$!; "+
			"trap 'kill \"$child\" 2>/dev/null; wait \"$child\" 2>/dev/null; exit 0' TERM; "+
			"printf '%%s' \"$child\" > %s; printf '%%s' $$ > %s; "+
			"while [ ! -f %s ]; do sleep 0.01; done; "+
			"kill \"$child\" 2>/dev/null; wait \"$child\" 2>/dev/null; printf shell-complete",
		strconv.Quote(shellChildPIDPath), strconv.Quote(shellPIDPath), strconv.Quote(gatePath))
	secondRequest := make(chan struct{})
	secondResponseRelease := make(chan struct{})
	releaseSecondResponse := sync.OnceFunc(func() { close(secondResponseRelease) })
	defer releaseSecondResponse()
	var secondOnce sync.Once
	var secondRequestResult *llm.ToolResultData
	installServeScriptedProvider(t, &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return scriptedToolCalls(scriptedForegroundShellCall("shell_1", shellCommand))
			},
			func(req llm.Request) llm.Response {
				secondRequestResult, _ = requestToolResult(req, "shell_1")
				secondOnce.Do(func() { close(secondRequest) })
				<-secondResponseRelease
				return scriptedCommunicate("shell persisted")
			},
		},
	})

	// The fixture owns a cancel function for the serve context, supplied through
	// the production notifyContext seam. It is the same cancellation an interrupt
	// delivers, so a case that returns before its own shutdown still ends serve.
	fixtureCtx, cancelFixture := context.WithCancel(context.Background())
	deps := defaultServeDeps()
	deps.notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		return fixtureCtx, cancelFixture
	}

	serveResult := make(chan error, 1)

	// One idempotent cleanup, registered before serve can fail. Its body releases
	// the shell gate and the second provider gate, cancels the fixture context,
	// then joins serve under a bounded deadline. It runs ahead of the temporary
	// directories' removal because it is registered after they are created. It is
	// the single reader of serveResult, and the normal path waits on it by calling
	// cleanup() below, so the join is never consumed twice.
	var serveErr error
	cleanup := sync.OnceFunc(func() {
		_ = os.WriteFile(gatePath, []byte("release"), 0o600)
		releaseSecondResponse()
		cancelFixture()
		select {
		case serveErr = <-serveResult:
			checkpoint("joined", shellPIDPath)
		case <-time.After(foregroundCleanupJoinDeadline):
			t.Errorf("foreground fixture's serve did not join within %s", foregroundCleanupJoinDeadline)
		}
	})
	t.Cleanup(cleanup)

	go func() {
		serveResult <- runServeWithDeps([]string{
			"--model", "openai/gpt-test",
			"--addr", "127.0.0.1:0",
			"--dir", workDir,
			"--state-dir", stateDir,
			"--run-dir", runDir,
		}, deps)
	}()

	// Emitted before the rendezvous: the serve-started failure case must cancel
	// and join a serve that has not yet registered, the earliest failure point.
	checkpoint("serve-started", shellPIDPath)
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
			ClientMutationID:   "foreground-shell-turn",
			ExpectedInstanceID: entry.SessionID,
			Ref:                ref,
			Input:              []appwire.InputItem{{Type: "text", Text: "run the foreground shell"}},
		}); err != nil {
			t.Fatalf("TurnStart: %v", err)
		}
	} else {
		client, _ = dialServeToolResultClient(ctx, t, entry.Address, string(mode), ref)
		defer client.Close()
		if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
			ClientMutationID:   "foreground-shell-no-subscriber-turn",
			ExpectedInstanceID: entry.SessionID,
			Ref:                ref,
			Input:              []appwire.InputItem{{Type: "text", Text: "run the foreground shell"}},
		}); err != nil {
			t.Fatalf("TurnStart: %v", err)
		}
	}

	if mode == foregroundShellColdReattach {
		select {
		case <-signals.started:
		case <-ctx.Done():
			t.Fatalf("foreground shell never showed its running tool state: %v", ctx.Err())
		}
		client.Close()
		client, signals = dialServeToolResultClient(ctx, t, entry.Address, "cold-reattach", ref)
		defer client.Close()
	}
	if _, ok := waitForFileContent(shellPIDPath, 10*time.Second); !ok {
		t.Fatalf("foreground shell never wrote its PID file %s", shellPIDPath)
	}
	if _, ok := waitForFileContent(shellChildPIDPath, 10*time.Second); !ok {
		t.Fatalf("foreground shell never wrote its child PID file %s", shellChildPIDPath)
	}
	checkpoint("shell-started", shellPIDPath)
	if err := os.WriteFile(gatePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release shell gate: %v", err)
	}

	select {
	case <-secondRequest:
	case <-ctx.Done():
		t.Fatalf("second provider request after foreground shell: %v", ctx.Err())
	}
	checkpoint("provider-waiting", shellPIDPath)
	if mode == foregroundShellNoSubscriber {
		// Reaching the second provider request proves the shell result crossed the
		// full tool boundary with zero subscribers. Attach only after that proof,
		// while the response is still gated, so the terminal status event is an
		// exact settle barrier instead of a wall-clock guess.
		client, signals = dialServeToolResultClient(ctx, t, entry.Address, "settle-observer", ref)
		defer client.Close()
	}
	releaseSecondResponse()
	if secondRequestResult == nil {
		t.Fatal("second provider request did not contain the foreground shell tool result")
	}
	if secondRequestResult.IsError {
		t.Fatalf("second provider request carried an error result: %#v", secondRequestResult)
	}
	if !strings.Contains(fmt.Sprint(secondRequestResult.Content), "shell-complete") {
		t.Fatalf("second provider request result = %#v, want shell-complete output", secondRequestResult.Content)
	}

	path := filepath.Join(transcriptPath, entry.SessionID+".transcript.jsonl")
	data, ok := waitForTranscriptKind(path, "TOOL_RESULTS", time.Second)
	if !ok {
		t.Fatalf("transcript %s never contained TOOL_RESULTS", path)
	}
	transcriptResult, ok := transcriptToolResult(data, "shell_1")
	if !ok {
		t.Fatalf("transcript data lost the foreground shell tool result")
	}
	if transcriptResult.IsError {
		t.Fatalf("transcript recorded an error result: %#v", transcriptResult)
	}
	if !strings.Contains(fmt.Sprint(transcriptResult.Content), "shell-complete") {
		t.Fatalf("transcript result = %#v, want shell-complete output", transcriptResult.Content)
	}
	if mode != foregroundShellNoSubscriber {
		select {
		case <-signals.completed:
		case <-ctx.Done():
			t.Fatalf("foreground shell call never settled in history: %v", ctx.Err())
		}
	}
	select {
	case <-signals.turnComplete:
	case <-ctx.Done():
		t.Fatalf("foreground shell turn never completed: %v", ctx.Err())
	}
	select {
	case <-signals.rested:
	case <-ctx.Done():
		t.Fatalf("foreground shell turn never settled idle: %v", ctx.Err())
	}

	if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}
	// The explicit call proves the registered cleanup is idempotent; t.Cleanup
	// invokes it a second time.
	cleanup()
	if serveErr != nil {
		t.Fatalf("runServe: %v", serveErr)
	}
}

// TestRunServeForegroundShellPersistsToolResults exercises the production Hub
// path with no subscriber, an active subscriber, and a subscriber that is
// replaced while the foreground process is still running. Each variant proves
// the same four checkpoints: the settled call reaches AppWire when observed,
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

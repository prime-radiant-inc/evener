package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

// TestServeGoal_TUIPathEndToEnd is the proof that /goal works through the REAL
// daemon glue — the exact path the TUI drives — end to end:
//
//	appwire goal/set  →  Server.goalFunc  →  Session.SetGoal
//	                  →  kick (SetKickFunc)  →  Server.SubmitContinuation
//	                  →  inputCh (InputCh)   →  serve loop ProcessInputKind(EntryContinuation)
//	                  →  armGoalContinuation gate  →  next continuation … until terminal.
//
// None of that wiring (cmd/serf/serve.go's SetGoalFunc / SetKickFunc callbacks
// and the InputCh→ProcessInputKind loop) is exercised by the unit tests, which
// stub the goalFunc or drive the Session directly. This test starts the real
// `serve` daemon with a scripted provider at the LLM boundary, connects an
// appwire client over the websocket RPC exactly like the TUI's hubstart does,
// and sets a goal whose
// second step is gated on an external file that only appears AFTER the first
// goal turn has ended and a continuation has begun. That makes a second turn —
// driven through SubmitContinuation → inputCh → ProcessInputKind — structurally
// mandatory: the model cannot finish in a single turn because the input it needs
// for b.txt does not exist until the continuation loop has already run once.
//
// It then asserts the real on-disk outcome (a.txt and b.txt with the gated
// content) plus a completed goal whose Iterations>=1 — the projection the TUI
// reads to render /goal status.
func TestServeGoal_TUIPathEndToEnd(t *testing.T) {
	workDir := t.TempDir()
	stateDir := t.TempDir()
	runDir := t.TempDir()
	aPath := filepath.Join(workDir, "a.txt")
	bPath := filepath.Join(workDir, "b.txt")
	gatePath := filepath.Join(workDir, "gate.txt")
	const gateToken = "go-2718281828"

	installServeScriptedProvider(t, &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return scriptedToolCalls(scriptedWriteFileCall("write_a", "a.txt", "seed"))
			},
			func(llm.Request) llm.Response {
				return scriptedCommunicate("step 1 complete; waiting for gate")
			},
			func(llm.Request) llm.Response {
				token, ok := waitForFileContent(gatePath, 5*time.Second)
				if !ok {
					return scriptedCommunicate("gate.txt was not released")
				}
				return scriptedToolCalls(scriptedWriteFileCall("write_b", "b.txt", strings.TrimSpace(token)))
			},
			func(llm.Request) llm.Response {
				return scriptedToolCalls(scriptedUpdateGoalCall("goal_1", "complete"))
			},
			func(llm.Request) llm.Response {
				return scriptedCommunicate("goal complete")
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

	// Connect an appwire client to the daemon over the websocket RPC, the same
	// way cmd/serf-tui/internal/hubstart dials the hub: DialWebSocket → NewClient
	// → Start → Initialize.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	defer client.Close()

	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serve-goal-live-test", Version: "test"},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// The daemon is single-session; its app identity is ("local", sessionID).
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()

	// The objective: step 1 (a.txt) can be done immediately; step 2 (b.txt) needs
	// the contents of gate.txt, which the harness withholds until the goal loop
	// has already taken at least one continuation turn. So the first goal turn
	// must end with the goal still active (gate.txt missing), forcing a real
	// continuation through the daemon's inputCh loop before b.txt is even knowable.
	objective := "This goal has two ordered steps. Step 1: create the file a.txt in the current " +
		"working directory containing exactly the text `seed` and nothing else. Step 2: the file " +
		"gate.txt in the same directory holds a token that does not exist yet but will appear on a " +
		"later turn; once gate.txt exists, read it and create the file b.txt containing exactly that " +
		"token and nothing else. If gate.txt does not exist yet, do step 1, report progress, and end " +
		"your turn so you are asked to continue — do NOT mark the goal complete and do NOT mark it " +
		"blocked; just keep going on the next turn. The goal is complete only once both a.txt and " +
		"b.txt exist with the correct contents."

	// Release the gate exactly when the daemon's continuation loop has run: poll
	// the same SerfThread.Goal projection the TUI reads, and the moment Iterations
	// crosses 1 (a continuation turn happened, fed via SubmitContinuation →
	// inputCh → ProcessInputKind), write gate.txt so the continuation can finish
	// step 2. This ties b.txt's existence to a real cross-turn continuation.
	gateCtx, gateCancel := context.WithCancel(ctx)
	defer gateCancel()
	var gateOnce sync.Once
	gateReleased := make(chan struct{})
	go func() {
		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-gateCtx.Done():
				return
			case <-ticker.C:
				readResp, err := client.ThreadRead(gateCtx, appwire.ThreadReadParams{Ref: ref})
				if err != nil {
					continue
				}
				if g := readResp.Thread.Serf.Goal; g != nil && g.Iterations >= 1 {
					gateOnce.Do(func() {
						_ = os.WriteFile(gatePath, []byte(gateToken), 0o644)
						close(gateReleased)
					})
					return
				}
			}
		}
	}()

	resp, err := client.GoalSet(ctx, appwire.GoalSetParams{Ref: ref, Objective: objective})
	if err != nil {
		t.Fatalf("GoalSet: %v", err)
	}
	// The session is idle at startup, so the idle kick should start the loop
	// immediately. We don't hard-fail on started=false (a turn-tail race could
	// hand the goal to the gate instead), but we record it for the proof log.
	if !resp.Started {
		t.Logf("GoalSet returned started=false (goal armed; gate/settle will kick it)")
	}

	// Poll the thread read for the goal reaching a terminal status. This is the
	// same SerfThread.Goal projection the TUI reads to render /goal status.
	goalState := waitForTerminalGoal(ctx, t, client, ref, 130*time.Second)
	gateCancel()

	// The gate must have been released, which only happens after a continuation
	// turn ran through the daemon glue. If it never fired, the loop never
	// continued — the exact gap this test exists to close.
	select {
	case <-gateReleased:
	default:
		t.Fatalf("gate.txt was never released: the goal reached %q (iterations=%d) without the daemon continuation loop ever running",
			goalState.Status, goalState.Iterations)
	}

	if goalState.Status != "complete" {
		t.Fatalf("goal terminal status = %q, want %q (iterations=%d)",
			goalState.Status, "complete", goalState.Iterations)
	}
	// Iterations counts continuation turns, which increment only inside
	// armGoalContinuation after a continuation fed back through
	// SubmitContinuation → inputCh → ProcessInputKind(EntryContinuation). A
	// completed goal with iterations>=1 is the direct proof the daemon
	// continuation loop ran across turns.
	if goalState.Iterations < 1 {
		t.Fatalf("goal completed with iterations=%d, want >= 1 (the daemon loop must have continued)", goalState.Iterations)
	}

	// Assert the real on-disk outcome: step 1 done, and step 2 produced from the
	// gate token that only became available on a continuation turn.
	aData, err := os.ReadFile(aPath)
	if err != nil {
		t.Fatalf("a.txt not created: %v", err)
	}
	if strings.TrimRight(string(aData), "\r\n") != "seed" {
		t.Fatalf("a.txt content = %q, want %q", string(aData), "seed")
	}
	bData, err := os.ReadFile(bPath)
	if err != nil {
		t.Fatalf("b.txt not created: %v", err)
	}
	// b.txt must carry the gate token, which only existed on disk after a
	// continuation turn ran. Assert containment (trimmed) rather than byte-exact
	// equality: the unique token proves the causal dependency and trims the same
	// way a real tool call would tolerate line endings around file content.
	if strings.TrimSpace(string(bData)) != gateToken {
		t.Fatalf("b.txt content = %q, want %q (the gate token released only on a continuation turn)", string(bData), gateToken)
	}

	// Cross-check: the persisted transcript exists for this session (the loop
	// ran turns through the daemon and recorded them).
	transcriptPath := filepath.Join(stateDir, "sessions", entry.SessionID+".transcript.jsonl")
	if _, err := os.Stat(transcriptPath); err != nil {
		t.Fatalf("transcript %s not written: %v", transcriptPath, err)
	}

	t.Logf("TUI PATH PROOF: GoalSet.Started=%v; SerfThread.Goal{Status:%q Iterations:%d}; a.txt=%q b.txt=%q",
		resp.Started, goalState.Status, goalState.Iterations, string(aData), string(bData))

	// Shut the daemon down and assert a clean exit.
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

	// The rendezvous entry is removed on clean shutdown.
	if entries, _ := rendezvous.List(runDir); len(entries) != 0 {
		t.Fatalf("rendezvous entries should be removed after shutdown, got %d", len(entries))
	}
}

// waitForTerminalGoal polls thread/read until SerfThread.Goal reaches a terminal
// status (anything other than "active") or the deadline elapses. It returns the
// terminal GoalState. The read targets the session by its ("local", sessionID)
// ref, the same projection the TUI consumes.
func waitForTerminalGoal(ctx context.Context, t *testing.T, client *appwire.Client, ref string, timeout time.Duration) appwire.GoalState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last *appwire.GoalState
	for time.Now().Before(deadline) {
		readResp, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
		if g := readResp.Thread.Serf.Goal; g != nil {
			last = g
			if g.Status != "active" {
				return *g
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled while waiting for terminal goal: %v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	if last != nil {
		t.Fatalf("goal did not reach terminal status within %s; last status=%q iterations=%d", timeout, last.Status, last.Iterations)
	}
	t.Fatalf("goal did not reach terminal status within %s; SerfThread.Goal was never populated", timeout)
	return appwire.GoalState{}
}

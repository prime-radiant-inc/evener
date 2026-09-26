package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// The memory harness is opt-in: it runs only when -memharness-out names a
// directory for its heap profiles and report. See "Memory profiling" in
// docs/developing-evener/performance-profiling.md.
var (
	memHarnessOut       = flag.String("memharness-out", "", "directory for the memory harness's heap profiles and report.txt; empty skips TestMemHarness")
	memHarnessTurns     = flag.Int("memharness-turns", 200, "memory harness: user turns to drive")
	memHarnessRounds    = flag.Int("memharness-rounds", 5, "memory harness: tool rounds per turn before the turn ends")
	memHarnessEvery     = flag.Int("memharness-every", 25, "memory harness: write a heap profile every N turns")
	memHarnessDelegates = flag.Bool("memharness-delegates", false, "memory harness: open each turn with a delegate that runs its own tool rounds")
	memHarnessState     = flag.String("memharness-state", "", "memory harness: state directory to serve from (a COPY of real state for -memharness-resume); empty uses a temp dir")
	memHarnessResume    = flag.String("memharness-resume", "", "memory harness: session id to resume from -memharness-state")
	memHarnessIdle      = flag.Duration("memharness-idle", 0, "memory harness: wall-clock idle time before the final profile, e.g. 45s to outlast the delegate idle release")
)

// The harness tags its own turn inputs and delegate tasks so the adapter can
// count a turn's tool rounds from its latest input (user-role reminders the
// session injects do not restart the count) and tell a child's requests from
// the root's.
const (
	memHarnessTurnMarker     = "please do more work"
	memHarnessDelegateMarker = "delegated unit"
)

// memHarnessAdapter answers every model round from the request alone: a turn
// runs its tool rounds (shell commands with a realistic spread of output
// sizes, optionally opening with a delegate) and then ends with communicate.
// Tool-less requests other than the namer are compaction summaries.
type memHarnessAdapter struct {
	roundsPerTurn int
	delegates     bool
	calls         atomic.Int64
}

func (a *memHarnessAdapter) Name() string { return "openai" }

func (a *memHarnessAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *memHarnessAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	n := a.calls.Add(1)
	if resp, ok := scriptedSessionNamerResponse(a.Name(), req); ok {
		return resp, nil
	}
	resp := a.respond(n, req)
	resp.Provider, resp.Model = req.Provider, req.Model
	// Input tokens track the request's size, so context pressure, and with
	// it compaction, behaves as it would live.
	tokens := llm.EstimateInputTokens(req).Tokens
	resp.Usage = llm.Usage{InputTokens: tokens, OutputTokens: 50, TotalTokens: tokens + 50}
	return resp, nil
}

func (a *memHarnessAdapter) respond(n int64, req llm.Request) llm.Response {
	if len(req.Tools) == 0 {
		return llm.Response{Message: llm.Assistant(strings.Repeat("summary of prior work. ", 200)), Finish: llm.FinishReason{Reason: llm.FinishReasonStop}}
	}
	rounds := 0
	for _, m := range slices.Backward(req.Messages) {
		if m.Role == llm.RoleUser && strings.Contains(m.Text(), memHarnessTurnMarker) {
			break
		}
		if m.Role == llm.RoleAssistant {
			rounds++
		}
	}
	if rounds >= a.roundsPerTurn {
		return scriptedCommunicate("done with this turn. " + strings.Repeat("explanation ", 100))
	}
	callID := "call_" + strconv.FormatInt(n, 10)
	var call llm.ToolCallData
	if a.delegates && rounds == 0 && !strings.Contains(memHarnessTaskText(req), memHarnessDelegateMarker) {
		args, _ := json.Marshal(map[string]any{
			"intent": "delegating",
			"prompt": memHarnessDelegateMarker + ": " + memHarnessTurnMarker + " " + strings.Repeat("context ", 200),
		})
		call = llm.ToolCallData{ID: callID, Name: "delegate", Arguments: args, Type: "function"}
	} else {
		sizes := []int{2_000, 8_000, 4_000, 30_000, 1_000, 120_000, 6_000, 16_000}
		call = scriptedShellCall(callID, fmt.Sprintf("head -c %d /dev/zero | tr '\\0' 'x' | fold -w 100", sizes[int(n)%len(sizes)]), "")
	}
	resp := scriptedToolCalls(call)
	// Models narrate as they work; that text is part of what a session holds.
	resp.Message.Content = append([]llm.ContentPart{{Kind: llm.ContentText, Text: "running a command " + strings.Repeat("thinking ", 30)}}, resp.Message.Content...)
	return resp
}

// memHarnessTaskText is the first harness-tagged user message: a turn input
// for the root session, the delegated task for a child.
func memHarnessTaskText(req llm.Request) string {
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser {
			if text := m.Text(); strings.Contains(text, memHarnessTurnMarker) {
				return text
			}
		}
	}
	return ""
}

// TestMemHarness drives a real in-process serve daemon through many turns with
// a scripted provider at the LLM boundary and writes heap profiles (inuse and
// alloc) plus a one-line-per-sample report to -memharness-out. It measures;
// it asserts nothing but that the turns complete.
func TestMemHarness(t *testing.T) {
	if *memHarnessOut == "" {
		t.Skip("pass -memharness-out=<dir> to run the memory harness")
	}
	if *memHarnessResume != "" && *memHarnessState == "" {
		t.Fatal("-memharness-resume needs -memharness-state naming a copy of the state that holds the session")
	}
	if *memHarnessResume != "" {
		// The adapter ends every turn with communicate; a session persisted
		// with another result tool would never see its turns complete.
		meta, err := schema.LoadSessionMeta(*memHarnessState, *memHarnessResume)
		if err != nil {
			t.Fatal(err)
		}
		if name := meta.Config.ResultToolName; name != "" && name != "communicate" {
			t.Fatalf("session %s uses result tool %q; the memory harness only scripts communicate", *memHarnessResume, name)
		}
	}
	if *memHarnessEvery <= 0 {
		t.Fatalf("-memharness-every=%d: want a positive number of turns between heap profiles", *memHarnessEvery)
	}
	if err := os.MkdirAll(*memHarnessOut, 0o755); err != nil {
		t.Fatal(err)
	}
	runtime.MemProfileRate = 4096
	adapter := &memHarnessAdapter{roundsPerTurn: *memHarnessRounds, delegates: *memHarnessDelegates}
	oldLoadClient := serveLoadClient
	serveLoadClient = func(stateDir string) (*llm.Client, error) {
		// The production client, registry and all, with the scripted adapter
		// overriding the openai instance.
		client, err := oldLoadClient(stateDir)
		if err != nil {
			return nil, err
		}
		client.Register(adapter)
		return client, nil
	}
	t.Cleanup(func() { serveLoadClient = oldLoadClient })

	runDir := t.TempDir()
	stateDir := *memHarnessState
	if stateDir == "" {
		stateDir = t.TempDir()
	}
	args := []string{
		"--model", "openai/gpt-5.2",
		"--addr", "127.0.0.1:0",
		"--dir", t.TempDir(),
		"--state-dir", stateDir,
		"--run-dir", runDir,
		"--context-strategy", "compact",
	}
	if *memHarnessResume != "" {
		args = append(args, "--resume", *memHarnessResume)
	}
	done := make(chan error, 1)
	go func() { done <- runServe(args) }()
	// Resuming a large real transcript takes far longer than a fresh start.
	entry := waitForServeTestRendezvousWithin(t, runDir, 10*time.Minute, done)

	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	client.Start(ctx)
	defer client.Close()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "memharness", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	completed := make(chan struct{}, 1024)
	go func() {
		for n := range client.Notifications() {
			if n.Method != appwire.NotifyThreadStatusChanged {
				continue
			}
			var params appwire.ThreadStatusChangedParams
			if json.Unmarshal(n.Params, &params) == nil && params.Status.Type == appwire.ThreadStatusIdle {
				completed <- struct{}{}
			}
		}
	}()
	awaitTurn := func(what string) {
		select {
		case <-completed:
		case <-time.After(5 * time.Minute):
			t.Fatalf("%s: no thread/status/changed(idle)", what)
		}
	}

	transcriptPath := filepath.Join(stateDir, "sessions", entry.SessionID+".transcript.jsonl")
	reportFile, err := os.OpenFile(filepath.Join(*memHarnessOut, "report.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer reportFile.Close()
	report := func(label string) {
		var beforeGC runtime.MemStats
		runtime.ReadMemStats(&beforeGC)
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		var transcriptBytes int64
		if fi, err := os.Stat(transcriptPath); err == nil {
			transcriptBytes = fi.Size()
		}
		profile, err := os.Create(filepath.Join(*memHarnessOut, "heap-"+label+".pprof"))
		if err != nil {
			t.Fatal(err)
		}
		defer profile.Close()
		if err := pprof.Lookup("heap").WriteTo(profile, 0); err != nil {
			t.Fatal(err)
		}
		line := fmt.Sprintf("%s live=%.1fMB unreleased=%.1fMB beforeGC(live=%.1fMB unreleased=%.1fMB) totalAlloc=%.1fMB transcript=%.1fMB modelCalls=%d\n",
			label, memHarnessMB(ms.HeapAlloc), memHarnessMB(ms.HeapSys-ms.HeapReleased),
			memHarnessMB(beforeGC.HeapAlloc), memHarnessMB(beforeGC.HeapSys-beforeGC.HeapReleased),
			memHarnessMB(ms.TotalAlloc), memHarnessMB(uint64(transcriptBytes)), adapter.calls.Load())
		t.Log(strings.TrimSpace(line))
		if _, err := reportFile.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}

	report("t000")
	// Mutation ids are unique per run: a resumed transcript already holds
	// the ids an earlier run sent, and reusing them changes how the resumed
	// run's turns are admitted.
	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	for i := 1; i <= *memHarnessTurns; i++ {
		input := []appwire.InputItem{{Type: "text", Text: fmt.Sprintf("turn %d: %s", i, memHarnessTurnMarker)}}
		_, err := client.TurnStart(ctx, appwire.TurnStartParams{
			ClientMutationID: fmt.Sprintf("memharness-%s-start-%d", runID, i), ExpectedInstanceID: entry.SessionID, Ref: ref, Input: input,
		})
		// A turn is still running -- one the session started for itself (a
		// delegate's result arriving) or one whose completion was already
		// counted -- so queue the input behind it, as the web composer does.
		if err != nil && strings.Contains(err.Error(), "already active") {
			err = client.TurnQueue(ctx, appwire.TurnQueueParams{
				ClientMutationID: fmt.Sprintf("memharness-%s-queue-%d", runID, i), ExpectedInstanceID: entry.SessionID, Ref: ref, Input: input,
			})
		}
		if err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		awaitTurn(fmt.Sprintf("turn %d", i))
		if i%*memHarnessEvery == 0 {
			report(fmt.Sprintf("t%03d", i))
		}
	}
	if *memHarnessIdle > 0 {
		// Wall-clock on purpose: the delegate idle release runs on the real
		// clock, and what is measured here is what an idle daemon keeps.
		time.Sleep(*memHarnessIdle)
		report("idle")
	}
	if err := shutdownServeTestDaemon(ctx, entry.Address, entry.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("runServe: %v", err)
	}
}

func memHarnessMB(b uint64) float64 { return float64(b) / (1 << 20) }

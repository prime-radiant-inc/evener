package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// The memory harness is opt-in: it runs only when -memharness-out names a
// directory for its heap profiles and report. See "Memory profiling" in
// docs/developing-evener/performance-profiling.md.
var (
	memHarnessOut        = flag.String("memharness-out", "", "directory for the memory harness's heap profiles and report.txt; empty skips TestMemHarness")
	memHarnessTurns      = flag.Int("memharness-turns", 200, "memory harness: user turns to drive")
	memHarnessRounds     = flag.Int("memharness-rounds", 5, "memory harness: tool rounds per turn before the turn ends")
	memHarnessEvery      = flag.Int("memharness-every", 25, "memory harness: write a heap profile every N turns")
	memHarnessDelegates  = flag.Bool("memharness-delegates", false, "memory harness: open each turn with a delegate that runs its own tool rounds")
	memHarnessState      = flag.String("memharness-state", "", "memory harness: state directory to serve from (a COPY of real state for -memharness-resume); empty uses a temp dir")
	memHarnessResume     = flag.String("memharness-resume", "", "memory harness: session id to resume from -memharness-state")
	memHarnessIdle       = flag.Duration("memharness-idle", 0, "memory harness: wall-clock idle time before the final profile, e.g. 45s to outlast the delegate idle release")
	memHarnessProvenance = flag.String("memharness-provenance", memHarnessProvenanceModern,
		"memory harness: recorded protocol provenance variant: modern calls llm.NoteAPIAttemptProtocol, legacy records none")
)

// The harness tags its own turn inputs and delegate tasks so the adapter can
// place a request at (turn, round) from its latest input (user-role reminders
// the session injects do not restart the count) and tell a child's requests
// from the root's.
const (
	memHarnessTurnMarker     = "please do more work"
	memHarnessDelegateMarker = "delegated unit"

	// The harness's one provider identity. The adapter answers as this
	// instance, so the transcript's ResponseProvider/ResponseModel must retain
	// both even when the legacy variant records no protocol.
	memHarnessProvider = "openai"
	memHarnessModel    = "gpt-5.2"

	memHarnessProvenanceModern = "modern"
	memHarnessProvenanceLegacy = "legacy"
)

// memHarnessScriptedProtocol is the wire protocol the modern variant records
// through llm.NoteAPIAttemptProtocol: a real registry protocol identity, not a
// synthetic string.
const memHarnessScriptedProtocol = registry.ProtocolOpenAIResponses

// memHarnessOutputSizes is the scripted spread of shell output sizes. Outputs
// above the shell ride-whole budget (8 KiB) become windowed results whose full
// bytes live in the durable job journal, which the post-exit verifier reads.
// The list is indexed by (turn+round) so the first turn always exercises a
// windowed result.
var memHarnessOutputSizes = []int{2_000, 30_000, 8_000, 4_000, 120_000, 1_000, 6_000, 16_000}

// memHarnessRoundScript is everything the adapter chooses for one tool round,
// as a pure function of (turn ordinal, round ordinal). The harness builds the
// same values when it authors its expectation, so the verifier never reads the
// adapter's own state.
type memHarnessRoundScript struct {
	Command     string
	OutputBytes int
}

func memHarnessRoundScriptFor(turn, round int) memHarnessRoundScript {
	size := memHarnessOutputSizes[(turn+round)%len(memHarnessOutputSizes)]
	return memHarnessRoundScript{
		Command:     fmt.Sprintf("head -c %d /dev/zero | tr '\\0' 'x' | fold -w 100", size),
		OutputBytes: size,
	}
}

// memHarnessShellOutput is the exact bytes that command writes: 100 'x'
// characters per line, with a newline after every line but the last.
func memHarnessShellOutput(size int) []byte {
	var b strings.Builder
	b.Grow(size + size/100 + 1)
	for start := 0; start < size; start += 100 {
		end := min(start+100, size)
		b.WriteString(strings.Repeat("x", end-start))
		if end < size {
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
}

func memHarnessInputText(turn int) string {
	return fmt.Sprintf("turn %d: %s", turn, memHarnessTurnMarker)
}

func memHarnessFinalMessage(turn int) string {
	// No trailing space: the result tool stores the message with its edges
	// trimmed, and the verifier compares the recorded text exactly.
	return fmt.Sprintf("done with turn %d. ", turn) + strings.Repeat("explanation ", 99) + "explanation"
}

func memHarnessDelegatePrompt(turn int) string {
	return fmt.Sprintf("%s: turn %d: %s %s", memHarnessDelegateMarker, turn, memHarnessTurnMarker, strings.Repeat("context ", 200))
}

func memHarnessShellCallID(turn, round int) string {
	return fmt.Sprintf("call_t%d_r%d", turn, round)
}

func memHarnessDelegateCallID(turn int) string {
	return fmt.Sprintf("call_t%d_delegate", turn)
}

// memHarnessParseTurnOrdinal reads the authored "turn N:" prefix out of a
// harness input or delegate task. The adapter gets its turn ordinal from the
// request text alone, exactly as the harness authored it.
func memHarnessParseTurnOrdinal(text string) (int, bool) {
	const prefix = "turn "
	rest := text
	for {
		i := strings.Index(rest, prefix)
		if i < 0 {
			return 0, false
		}
		rest = rest[i+len(prefix):]
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end == 0 || end >= len(rest) || rest[end] != ':' {
			continue
		}
		n, err := strconv.Atoi(rest[:end])
		if err != nil {
			continue
		}
		return n, true
	}
}

// memHarnessAuthoredTurnOrdinal reads the turn ordinal out of a message the
// harness itself authored: a root input begins "turn N:", a delegate task
// begins "delegated unit: turn N:". A delegate notification delivered mid-turn
// embeds the child's task text (including "turn N:") in its metadata, so only
// an authored prefix may name the turn.
func memHarnessAuthoredTurnOrdinal(text string) (int, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "turn ") && !strings.HasPrefix(trimmed, memHarnessDelegateMarker+": turn ") {
		return 0, false
	}
	return memHarnessParseTurnOrdinal(trimmed)
}

// memHarnessRequestPosition places a model request at the harness's (turn,
// round) coordinates. It counts assistant messages back to the turn's latest
// authored input, as the harness always has, and reads the turn ordinal from
// that input's text.
func memHarnessRequestPosition(req llm.Request) (turn, rounds int) {
	turn = 1
	found := false
	for _, m := range slices.Backward(req.Messages) {
		if m.Role == llm.RoleUser && strings.Contains(m.Text(), memHarnessTurnMarker) {
			if parsed, ok := memHarnessAuthoredTurnOrdinal(m.Text()); ok {
				turn = parsed
				found = true
				break
			}
		}
		if m.Role == llm.RoleAssistant {
			rounds++
		}
	}
	if !found {
		// A mid-turn compaction can fold the authored input out of the window it
		// retains. The scripted call ids it keeps still name the last round that
		// ran, so this request is that round's successor in the same turn.
		if callTurn, lastRound, ok := memHarnessLastScriptedCall(req); ok {
			return callTurn, lastRound + 1
		}
	}
	return turn, rounds
}

// memHarnessScriptedCallID reads one of the harness's authored tool-call ids
// back into the turn it belongs to and the round it ran.
func memHarnessScriptedCallID(id string) (turn, round int, ok bool) {
	rest, found := strings.CutPrefix(id, "call_t")
	if !found {
		return 0, 0, false
	}
	turnText, tail, found := strings.Cut(rest, "_")
	if !found {
		return 0, 0, false
	}
	turn, err := strconv.Atoi(turnText)
	if err != nil || turn < 1 {
		return 0, 0, false
	}
	if tail == "delegate" {
		return turn, 0, true
	}
	roundText, found := strings.CutPrefix(tail, "r")
	if !found {
		return 0, 0, false
	}
	round, err = strconv.Atoi(roundText)
	if err != nil || round < 0 {
		return 0, 0, false
	}
	return turn, round, true
}

// memHarnessLastScriptedCall finds the highest scripted (turn, round) a request
// still carries, in its assistant tool calls or its tool results.
func memHarnessLastScriptedCall(req llm.Request) (turn, round int, ok bool) {
	consider := func(id string) {
		if callTurn, callRound, valid := memHarnessScriptedCallID(id); valid && (callTurn > turn || (callTurn == turn && callRound >= round)) {
			turn, round, ok = callTurn, callRound, true
		}
	}
	for _, m := range req.Messages {
		for _, part := range m.Content {
			if part.ToolCall != nil {
				consider(part.ToolCall.ID)
			}
			if part.ToolResult != nil {
				consider(part.ToolResult.ToolCallID)
			}
		}
	}
	return turn, round, ok
}

// memHarnessAdapter answers every model round from the request alone: a turn
// runs its tool rounds (shell commands with a realistic spread of output
// sizes, optionally opening with a delegate) and then ends with communicate.
// Tool-less requests other than the namer are compaction summaries.
type memHarnessAdapter struct {
	roundsPerTurn int
	delegates     bool
	// protocol is the identity the modern variant records on the attempt group;
	// empty means the legacy variant, which records none.
	protocol string
	calls    atomic.Int64
}

func (a *memHarnessAdapter) Name() string { return memHarnessProvider }

func (a *memHarnessAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *memHarnessAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.calls.Add(1)
	if resp, ok := scriptedSessionNamerResponse(a.Name(), req); ok {
		return resp, nil
	}
	// An override serves the request without a transport attempt, so nothing
	// else stamps the round's protocol. The modern variant notes a real wire
	// protocol itself; the legacy variant leaves it empty.
	if a.protocol != "" {
		protocol := a.protocol
		llm.NoteAPIAttemptProtocol(ctx, func() string { return protocol })
	}
	turn, rounds := memHarnessRequestPosition(req)
	resp := a.respond(turn, rounds, req)
	resp.Provider, resp.Model = req.Provider, req.Model
	// Input tokens track the request's size, so context pressure, and with
	// it compaction, behaves as it would live.
	tokens := llm.EstimateInputTokens(req).Tokens
	resp.Usage = llm.Usage{InputTokens: tokens, OutputTokens: 50, TotalTokens: tokens + 50}
	return resp, nil
}

func (a *memHarnessAdapter) respond(turn, rounds int, req llm.Request) llm.Response {
	if len(req.Tools) == 0 {
		// A summary section, or the summarizer rejects the reply and stores none.
		return llm.Response{Message: llm.Assistant("## Progress\n" + strings.Repeat("summary of prior work. ", 200)), Finish: llm.FinishReason{Reason: llm.FinishReasonStop}}
	}
	if rounds >= a.roundsPerTurn {
		return scriptedCommunicate(memHarnessFinalMessage(turn))
	}
	var call llm.ToolCallData
	if a.delegates && rounds == 0 && !strings.Contains(memHarnessTaskText(req), memHarnessDelegateMarker) {
		args, _ := json.Marshal(map[string]any{
			"intent": "delegating",
			"prompt": memHarnessDelegatePrompt(turn),
		})
		call = llm.ToolCallData{ID: memHarnessDelegateCallID(turn), Name: "delegate", Arguments: args, Type: "function"}
	} else {
		script := memHarnessRoundScriptFor(turn, rounds)
		call = scriptedShellCall(memHarnessShellCallID(turn, rounds), script.Command, "")
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

// memHarnessTurnStates records the terminal status each turn settles at, from
// history/updated pushes; the harness only ever consults the ids this run
// started (a queued input's id is learned from the transcript). An idle thread
// push only pokes the wait loop: it can never satisfy a turn by itself.
type memHarnessTurnStates struct {
	mu       sync.Mutex
	statuses map[string]string
	wake     chan struct{}
}

func newMemHarnessTurnStates() *memHarnessTurnStates {
	return &memHarnessTurnStates{statuses: make(map[string]string), wake: make(chan struct{}, 1)}
}

func (s *memHarnessTurnStates) note(turnID, status string) {
	s.mu.Lock()
	s.statuses[turnID] = status
	s.mu.Unlock()
	s.poke()
}

// noteTerminalTurns records every terminal turn in a push or read snapshot;
// non-terminal turns stay unsettled.
func (s *memHarnessTurnStates) noteTerminalTurns(turns []appwire.Turn) {
	for _, turn := range turns {
		switch turn.Status {
		case appwire.TurnStatusCompleted, appwire.TurnStatusFailed, appwire.TurnStatusInterrupted:
			s.note(turn.ID, turn.Status)
		}
	}
}

func (s *memHarnessTurnStates) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *memHarnessTurnStates) settled(turnID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status, ok := s.statuses[turnID]
	return status, ok
}

func (s *memHarnessTurnStates) wait(d time.Duration) {
	select {
	case <-s.wake:
	case <-time.After(d):
	}
}

// memHarnessAuthoritativeRead reads the root thread from the live in-process
// daemon. The daemon's root snapshot carries MutationStateAuthoritative; the
// hub's ThreadReadResponse.Authoritative flag is stamped by hub transports and
// is not part of the daemon's own reply, so this also requires a live-daemon
// boot generation and rejects a daemonless/saved read. Absence is only trusted
// from a read that passes both.
func memHarnessAuthoritativeRead(ctx context.Context, t *testing.T, client *appwire.Client, ref string, includeTurns bool, itemLimit int) appwire.ThreadReadResponse {
	t.Helper()
	resp, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: includeTurns, ItemLimit: itemLimit})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if !resp.Thread.Evener.MutationStateAuthoritative {
		t.Fatalf("ThreadRead cannot settle mutations (boot generation %q)", resp.BootGeneration)
	}
	if resp.BootGeneration == "" || resp.BootGeneration == appwire.DaemonlessBootGeneration {
		t.Fatalf("ThreadRead did not come from the live daemon (boot generation %q)", resp.BootGeneration)
	}
	return resp
}

var errMemHarnessTranscriptHit = errors.New("memharness transcript hit")

// memHarnessTurnActive reports the typed turn/start refusal for a turn that is
// already running, so the harness queues behind it without matching copy.
func memHarnessTurnActive(err error) bool {
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		return false
	}
	data, _ := appwire.DecodedErrorData(wire.Data)
	return data.EvenerErrorInfo == appwire.ErrorConflict
}

// memHarnessTranscriptMutationTurn reads the recorded turn id for one client
// mutation. The queued path has no receipt turn id, so it learns the identity
// the transcript recorded (an entry is persisted before it is announced). A
// transcript that cannot be read is an error, not absence.
func memHarnessTranscriptMutationTurn(path, mutationID string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer f.Close()
	found := ""
	_, err = transcript.Scan(context.Background(), f, 0, func(_ []byte, entry transcript.Entry) error {
		if entry.Turn.Kind == schema.TurnUserInput && entry.Turn.ClientMutationID == mutationID {
			found = entry.Turn.TurnID
			return errMemHarnessTranscriptHit
		}
		return nil
	})
	if errors.Is(err, errMemHarnessTranscriptHit) {
		return found, found != "", nil
	}
	if err != nil {
		return "", false, err
	}
	return "", false, nil
}

// TestMemHarness drives a real in-process serve daemon through many turns with
// a scripted provider at the LLM boundary and writes heap profiles (inuse and
// alloc) plus a one-line-per-sample report to -memharness-out.
//
// Every turn is correlated to the root turn/mutation the daemon's receipt
// named; a sample is only taken once an authoritative read shows no run-owned
// pending mutation or queue entry; and after the daemon exits the recorded
// transcript and the shell output journal are verified against an expectation
// the harness authored independently from the adapter's script.
func TestMemHarness(t *testing.T) {
	if *memHarnessOut == "" {
		t.Skip("pass -memharness-out=<dir> to run the memory harness")
	}
	if *memHarnessResume != "" && *memHarnessState == "" {
		t.Fatal("-memharness-resume needs -memharness-state naming a copy of the state that holds the session")
	}
	if *memHarnessProvenance != memHarnessProvenanceModern && *memHarnessProvenance != memHarnessProvenanceLegacy {
		t.Fatalf("-memharness-provenance=%q: want %q or %q", *memHarnessProvenance, memHarnessProvenanceModern, memHarnessProvenanceLegacy)
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
	if *memHarnessRounds < 0 {
		t.Fatalf("-memharness-rounds=%d: want a non-negative number of tool rounds", *memHarnessRounds)
	}
	if err := os.MkdirAll(*memHarnessOut, 0o755); err != nil {
		t.Fatal(err)
	}
	runtime.MemProfileRate = 4096
	// The expected protocol comes from the chosen variant, never from the
	// adapter: otherwise deleting the modern variant's note would also empty
	// what the verifier expects.
	scriptedProtocol := ""
	if *memHarnessProvenance == memHarnessProvenanceModern {
		scriptedProtocol = memHarnessScriptedProtocol
	}
	adapter := &memHarnessAdapter{roundsPerTurn: *memHarnessRounds, delegates: *memHarnessDelegates, protocol: scriptedProtocol}
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
		"--model", memHarnessProvider + "/" + memHarnessModel,
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
	states := newMemHarnessTurnStates()
	go func() {
		for n := range client.Notifications() {
			switch n.Method {
			case appwire.NotifyHistoryUpdated:
				var params appwire.HistoryUpdatedParams
				if json.Unmarshal(n.Params, &params) != nil {
					continue
				}
				states.noteTerminalTurns(params.Turns)
			case appwire.NotifyThreadStatusChanged:
				var params appwire.ThreadStatusChangedParams
				if json.Unmarshal(n.Params, &params) == nil && params.Status.Type == appwire.ThreadStatusIdle {
					// Only a wake-up hint: without a recorded terminal status
					// for the awaited turn, an idle push proves nothing.
					states.poke()
				}
			}
		}
	}()

	transcriptPath := schema.SessionTranscriptPath(stateDir, entry.SessionID)
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

	// ownedMutations names every mutation this run sent, start or queue.
	ownedMutations := make(map[string]bool)
	// ownedQueueEntries names every queue entry this run's receipts named.
	ownedQueueEntries := make(map[string]bool)
	awaitTurn := func(turnID, what string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Minute)
		// The push usually settles the turn; the read is recovery, so keep it
		// out of the measured window unless pushes stay silent.
		nextRead := time.Now().Add(3 * time.Second)
		for {
			if status, ok := states.settled(turnID); ok {
				if status != appwire.TurnStatusCompleted {
					t.Fatalf("%s: turn %s settled as %s", what, turnID, status)
				}
				return
			}
			if !time.Now().Before(nextRead) {
				// Push recovery: a missing history/updated still settles from
				// the live daemon's authoritative snapshot.
				resp := memHarnessAuthoritativeRead(ctx, t, client, ref, true, appwire.TranscriptItemPageLimit)
				states.noteTerminalTurns(resp.Thread.Turns)
				nextRead = time.Now().Add(3 * time.Second)
				continue
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: no recorded completion for turn %s", what, turnID)
			}
			wait := min(time.Until(deadline), 500*time.Millisecond)
			states.wait(wait)
		}
	}
	queuedTurnID := func(mutationID string) string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Minute)
		for {
			turnID, found, err := memHarnessTranscriptMutationTurn(transcriptPath, mutationID)
			if err != nil {
				t.Fatalf("queued input %s transcript read: %v", mutationID, err)
			}
			if found {
				return turnID
			}
			if time.Now().After(deadline) {
				t.Fatalf("queued input %s was never recorded in the transcript", mutationID)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// requireNoRunOwnedPending believes absence only from an authoritative live
	// read, and only for the mutations this run sent. The mutation can leave the
	// pending set a moment after its turn settles, so poll rather than fail a
	// healthy run on one read; another producer's queue entry is not this run's.
	requireNoRunOwnedPending := func(what string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			resp := memHarnessAuthoritativeRead(ctx, t, client, ref, false, 0)
			owned := ""
			for _, id := range resp.Thread.Evener.Queue.ClientMutationIDs {
				if ownedMutations[id] {
					owned = "mutation " + id
				}
			}
			for _, id := range resp.Thread.Evener.Queue.IDs {
				if ownedQueueEntries[id] {
					owned = "queue entry " + id
				}
			}
			for _, pending := range resp.Thread.Evener.PendingMutations {
				if ownedMutations[pending.ClientMutationID] {
					owned = "mutation " + pending.ClientMutationID
				}
				for _, id := range pending.QueueEntryIDs {
					if ownedQueueEntries[id] {
						owned = "queue entry " + id
					}
				}
			}
			if owned == "" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: run-owned %s is still queued or pending", what, owned)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	sample := func(label string) {
		requireNoRunOwnedPending(label)
		report(label)
	}

	sample("t000")
	// Mutation ids are unique per run: a resumed transcript already holds
	// the ids an earlier run sent, and reusing them changes how the resumed
	// run's turns are admitted.
	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	type sentTurn struct {
		ClientMutationID string
		TurnID           string
	}
	sent := make([]sentTurn, 0, *memHarnessTurns)
	for i := 1; i <= *memHarnessTurns; i++ {
		input := []appwire.InputItem{{Type: "text", Text: memHarnessInputText(i)}}
		startMutation := fmt.Sprintf("memharness-%s-start-%d", runID, i)
		start, err := client.TurnStart(ctx, appwire.TurnStartParams{
			ClientMutationID: startMutation, ExpectedInstanceID: entry.SessionID, Ref: ref, Input: input,
		})
		var mutationID, turnID string
		switch {
		case err == nil:
			// A turn is still running -- one the session started for itself (a
			// delegate's result arriving) -- so the input is queued behind it,
			// as the web composer does. Otherwise the receipt names the turn.
			mutationID = start.Receipt.ClientMutationID
			if mutationID != startMutation {
				t.Fatalf("turn %d: start receipt named mutation %q, want %q", i, mutationID, startMutation)
			}
			if start.Receipt.ThreadID != "" && start.Receipt.ThreadID != entry.SessionID {
				t.Fatalf("turn %d: start receipt named thread %q, want %q", i, start.Receipt.ThreadID, entry.SessionID)
			}
			turnID = start.Receipt.TurnID
			if turnID == "" {
				t.Fatalf("turn %d: start receipt named no turn", i)
			}
			ownedMutations[mutationID] = true
			for _, id := range start.Receipt.QueueEntryIDs {
				ownedQueueEntries[id] = true
			}
			awaitTurn(turnID, fmt.Sprintf("turn %d", i))
		case memHarnessTurnActive(err):
			// The refused attempt still names a mutation the daemon may have
			// looked at; count it so a stale pending entry is not invisible.
			ownedMutations[startMutation] = true
			t.Logf("turn %d: a turn is active, queued behind it", i)
			mutationID = fmt.Sprintf("memharness-%s-queue-%d", runID, i)
			var queued appwire.TurnQueueResponse
			if err := client.Request(ctx, appwire.MethodTurnQueue, appwire.TurnQueueParams{
				ClientMutationID: mutationID, ExpectedInstanceID: entry.SessionID, Ref: ref, Input: input,
			}, &queued); err != nil {
				t.Fatalf("turn %d: queue: %v", i, err)
			}
			if queued.Receipt.ClientMutationID != mutationID {
				t.Fatalf("turn %d: queue receipt named mutation %q, want %q", i, queued.Receipt.ClientMutationID, mutationID)
			}
			if len(queued.Receipt.QueueEntryIDs) == 0 {
				t.Fatalf("turn %d: queue receipt named no queue entry", i)
			}
			ownedMutations[mutationID] = true
			for _, id := range queued.Receipt.QueueEntryIDs {
				ownedQueueEntries[id] = true
			}
			turnID = queuedTurnID(mutationID)
			awaitTurn(turnID, fmt.Sprintf("queued turn %d", i))
		default:
			t.Fatalf("turn %d: %v", i, err)
		}
		sent = append(sent, sentTurn{ClientMutationID: mutationID, TurnID: turnID})
		if i%*memHarnessEvery == 0 {
			sample(fmt.Sprintf("t%03d", i))
		}
	}
	// The final sample may not count work this run still owes: require the
	// authoritative queue and pending-mutation state to be clear first.
	requireNoRunOwnedPending("before final sample")
	if *memHarnessIdle > 0 {
		// Wall-clock on purpose: the delegate idle release runs on the real
		// clock, and what is measured here is what an idle daemon keeps.
		time.Sleep(*memHarnessIdle)
		requireNoRunOwnedPending("before idle sample")
		report("idle")
	}
	if err := shutdownServeTestDaemon(ctx, entry.Address, entry.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("runServe: %v", err)
	}

	// Everything below is verification, deliberately after process exit and
	// outside every CPU/heap sample. It reads the recorded transcript and the
	// durable shell output journal, not any harness bookkeeping.
	want := memHarnessTranscriptExpectation{
		StateDir:                stateDir,
		SessionID:               entry.SessionID,
		Protocol:                scriptedProtocol,
		Provider:                memHarnessProvider,
		Model:                   memHarnessModel,
		RequireUniqueInputText:  *memHarnessResume == "",
		TolerateExtraToolRounds: *memHarnessDelegates,
	}
	for i, turn := range sent {
		ordinal := i + 1
		expected := memHarnessExpectedTurn{
			ClientMutationID: turn.ClientMutationID,
			InputText:        memHarnessInputText(ordinal),
			TurnID:           turn.TurnID,
			FinalMessage:     memHarnessFinalMessage(ordinal),
		}
		for round := 0; round < *memHarnessRounds; round++ {
			if *memHarnessDelegates && round == 0 {
				expected.Rounds = append(expected.Rounds, memHarnessExpectedRound{Tool: "delegate"})
				continue
			}
			script := memHarnessRoundScriptFor(ordinal, round)
			expected.Rounds = append(expected.Rounds, memHarnessExpectedRound{Tool: "shell", ShellOutput: memHarnessShellOutput(script.OutputBytes)})
		}
		want.Turns = append(want.Turns, expected)
	}
	if err := memHarnessVerifyTranscript(ctx, transcriptPath, want); err != nil {
		t.Fatal(err)
	}
}

func memHarnessMB(b uint64) float64 { return float64(b) / (1 << 20) }

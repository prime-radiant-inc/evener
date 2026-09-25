package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// The transcript read model's phase 1 parity harness
// (docs/superpowers/specs/2026-09-25-transcript-read-model-design.md,
// "Migration"). It drives one scripted session through the real session and
// server code, as `evener serve` wires them, and compares the server's live
// view of the thread with the projection of its transcript file. Today they
// disagree; each known disagreement is a row below, tagged with the phase
// that removes it. The test fails on a disagreement no row lists, and on a row
// that no longer happens.

// parityChildTask marks the delegate's prompt, so the scripted provider can
// tell the child's requests from the root's.
const parityChildTask = "CHILD-TASK parity delegate"

// parityProvider answers the session's model requests from a script. The
// session namer, the compaction summarizer and the delegate child are
// answered by request shape; every other request takes the next step.
type parityProvider struct {
	mu           sync.Mutex
	steps        []func(llm.Request) (llm.Response, error)
	childRelease chan struct{}
}

func (p *parityProvider) Name() string { return "openai" }

func (p *parityProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (p *parityProvider) script(steps ...func(llm.Request) (llm.Response, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps = append(p.steps, steps...)
}

func (p *parityProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	resp, err := p.respond(ctx, req)
	if resp.Provider == "" {
		resp.Provider = p.Name()
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	if resp.Finish.Reason == "" {
		resp.Finish = llm.FinishReason{Reason: llm.FinishReasonStop}
	}
	return resp, err
}

func (p *parityProvider) respond(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.ResponseFormat != nil {
		return llm.Response{Message: llm.Assistant(`{"name":"Parity"}`)}, nil
	}
	if len(req.Tools) == 0 {
		return llm.Response{Message: llm.Assistant("Summary: the parity session so far.")}, nil
	}
	if firstUserText(req) == parityChildTask {
		select {
		case <-p.childRelease:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return parityCommunicate("child-done", "the child finished", true), nil
	}
	p.mu.Lock()
	if len(p.steps) == 0 {
		p.mu.Unlock()
		return parityCommunicate("unscripted", "unscripted request", true), nil
	}
	step := p.steps[0]
	p.steps = p.steps[1:]
	p.mu.Unlock()
	return step(req)
}

func (p *parityProvider) remaining() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.steps)
}

func firstUserText(req llm.Request) string {
	for _, message := range req.Messages {
		if message.Role == llm.RoleUser {
			return strings.TrimSpace(message.Text())
		}
	}
	return ""
}

func parityCall(id, name string, args any) llm.ContentPart {
	data, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: id, Name: name, Arguments: data, Type: "function"}}
}

func parityCommunicateCall(id, message string, endTurn bool) llm.ContentPart {
	return parityCall(id, "communicate", map[string]any{
		"message":  message,
		"end_turn": endTurn,
		"output":   map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}},
	})
}

func parityResponse(parts ...llm.ContentPart) llm.Response {
	return llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Content: parts},
		Finish:  llm.FinishReason{Reason: llm.FinishReasonToolCalls},
		Usage:   llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}
}

func parityCommunicate(id, message string, endTurn bool) llm.Response {
	return parityResponse(parityCommunicateCall(id, message, endTurn))
}

func step(resp llm.Response) func(llm.Request) (llm.Response, error) {
	return func(llm.Request) (llm.Response, error) { return resp, nil }
}

// paritySession is one session bridged into its own server, as serve does.
type paritySession struct {
	sess    *agent.Session
	srv     *Server
	drained chan struct{}

	mu   sync.Mutex
	cond *sync.Cond
	seen []events.SessionEvent
	next int
}

func bridgeParitySession(t *testing.T, sess *agent.Session, prepared PreparedAppIdentity, stateDir string) *paritySession {
	t.Helper()
	ps := &paritySession{sess: sess, srv: NewServer(ServerConfig{StateDir: stateDir}), drained: make(chan struct{})}
	ps.cond = sync.NewCond(&ps.mu)
	ps.srv.ReplaceAppIdentity(prepared, nil)
	sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
		BridgeEvent(ps.srv, ev, nil)
		// Recorded after the projection commit: an event awaited below has
		// already reached the live view.
		ps.mu.Lock()
		ps.seen = append(ps.seen, ev)
		ps.mu.Unlock()
		ps.cond.Broadcast()
	}, func() { close(ps.drained) })
	return ps
}

// await returns the first event of kind after the previous await that match
// accepts.
func (ps *paritySession) await(t *testing.T, kind events.EventKind, match func(events.SessionEvent) bool) events.SessionEvent {
	t.Helper()
	// A tripwire, not the mechanism: every awaited event is one the scenario
	// makes the session emit.
	timer := time.AfterFunc(20*time.Second, func() {
		ps.mu.Lock()
		ps.next = -1
		ps.mu.Unlock()
		ps.cond.Broadcast()
	})
	defer timer.Stop()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for {
		if ps.next < 0 {
			t.Fatalf("timed out awaiting %s", kind)
		}
		for ; ps.next < len(ps.seen); ps.next++ {
			ev := ps.seen[ps.next]
			if ev.Kind == kind && (match == nil || match(ev)) {
				ps.next++
				return ev
			}
		}
		ps.cond.Wait()
	}
}

func (ps *paritySession) sawKind(kind events.EventKind) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, ev := range ps.seen {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

func (ps *paritySession) liveTurns(t *testing.T) []appwire.Turn {
	t.Helper()
	snapshot := ps.srv.appTurnSnapshotForID(ps.sess.ID())
	if snapshot == nil {
		t.Fatal("the server holds no turn snapshot for the session")
	}
	return snapshot.Snapshot()
}

func (ps *paritySession) close(t *testing.T) {
	t.Helper()
	ps.sess.Close()
	<-ps.drained
}

func noSleep(context.Context, time.Duration) error { return nil }

// writeStopHookPlugin writes a plugin whose Stop hook runs at every turn end,
// so the transcript records HOOK_COMPLETED entries.
func writeStopHookPlugin(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "parity-plugin")
	for path, body := range map[string]string{
		".claude-plugin/plugin.json": `{"name":"parity-hooks","version":"1.0.0"}`,
		"hooks/hooks.json":           `{"hooks":{"Stop":[{"matcher":"*","hooks":[{"type":"command","command":"exit 0"}]}]}}`,
	} {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writePixel(t *testing.T, path string) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func transcriptTurns(t *testing.T, path string) []schema.Turn {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only
	reader := bufio.NewReaderSize(f, 1<<20)
	var turns []schema.Turn
	for first := true; ; first = false {
		line, complete, _, err := transcript.ReadLine(reader, appTranscriptMaxLineBytes)
		if err != nil {
			t.Fatal(err)
		}
		if !complete {
			return turns
		}
		if first || len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatal(err)
		}
		turns = append(turns, entry.Turn)
	}
}

func assertParity(t *testing.T, label string, ps *paritySession, table []knownDivergence) {
	t.Helper()
	file, _, err := appTurnsFromTranscriptFile(ps.sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	observed := diffParity(ps.liveTurns(t), file)
	t.Run(label, func(t *testing.T) { checkParity(t, observed, table) })
}

func TestTranscriptParity(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	workDir := filepath.Join(root, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePixel(t, filepath.Join(workDir, "pixel.png"))
	plugin := writeStopHookPlugin(t, root)
	script := &parityProvider{childRelease: make(chan struct{})}
	client := llm.NewClient()
	client.Register(script)
	retry := &llm.RetryPolicy{MaxRetries: 2}
	sess, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{
		StateDir:       stateDir,
		VisionModel:    "off",
		LLMRetryPolicy: retry,
		LLMSleep:       noSleep,
		PluginDirs:     []string{plugin},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess.SetClientMutationStartWakeFunc(func() {})
	notified := make(chan struct{}, 1)
	sess.SetNotifyFunc(func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	})
	prepared, err := PrepareAppIdentity("local", sess.ID(), sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	ps := bridgeParitySession(t, sess, prepared, stateDir)
	ctx := context.Background()
	endOfInput := func() { ps.await(t, events.EventSessionEnd, nil) }
	processInput := func(text string) {
		t.Helper()
		if _, err := sess.ProcessInput(ctx, text, nil); err != nil {
			t.Fatalf("ProcessInput(%q): %v", text, err)
		}
		endOfInput()
	}

	// 1. A client-mutation turn: reasoning, text, a real read_file of a PNG
	// (an image-bearing result), then communicate. The Stop hook runs.
	script.script(
		step(parityResponse(
			llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "look at the pixel"}},
			llm.ContentPart{Kind: llm.ContentText, Text: "Reading the image."},
			parityCall("read-1", "read_file", map[string]any{"file_path": "pixel.png"}),
		)),
		step(parityCommunicate("comm-1", "The image is one pixel.", true)),
	)
	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "parity-start", ExpectedInstanceID: sess.ID(),
		Input: []appwire.InputItem{{Type: "text", Text: "start the parity session"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sess.ProcessClientMutationStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	endOfInput()

	// 2. Steering mid-turn, then a mid-turn communicate and a final one.
	script.script(
		func(llm.Request) (llm.Response, error) {
			if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
				ClientMutationID: "parity-steer", ExpectedInstanceID: sess.ID(),
				Input: []appwire.InputItem{{Type: "text", Text: "also check the size"}},
			}); err != nil {
				return llm.Response{}, err
			}
			return parityCommunicate("comm-2", "Working on it.", false), nil
		},
		step(parityCommunicate("comm-3", "The size is 1x1.", true)),
	)
	processInput("describe the image")

	// 3. A model switch while idle.
	if err := sess.SetModel("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	ps.await(t, events.EventModelChanged, nil)

	// 4. Enough history to compact, then compaction.
	script.script(
		step(parityCommunicate("comm-4", "First filler answer.", true)),
		step(parityCommunicate("comm-5", "Second filler answer.", true)),
	)
	processInput("first filler question")
	processInput("second filler question")
	if err := sess.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	ps.await(t, events.EventContextCompaction, func(ev events.SessionEvent) bool {
		data, ok := ev.Data.(events.ContextCompactionData)
		return ok && data.Layer == "summarize"
	})

	// 5. A retried provider error, then success.
	script.script(
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 503, "overloaded", nil, nil)
		},
		step(parityCommunicate("comm-6", "Recovered after a retry.", true)),
	)
	processInput("a request that is retried")
	if !ps.sawKind(events.EventModelRetry) {
		t.Fatal("the retried request emitted no retry event")
	}

	// 6. A terminal provider error fails the turn, and a new turn retries it.
	script.script(func(llm.Request) (llm.Response, error) {
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 401, "bad key", nil, nil)
	})
	if _, err := sess.ProcessInput(ctx, "a request that fails", nil); err == nil {
		t.Fatal("the failing request succeeded")
	}
	endOfInput()
	// The user retries the failed work as a new turn.
	script.script(step(parityCommunicate("comm-retry", "Done on the second try.", true)))
	processInput("retry the request that failed")

	// 7. Goal continuation: the user turn ends, the goal continues it, and the
	// continuation completes the goal.
	if _, err := sess.SetGoal(ctx, "finish the parity check"); err != nil {
		t.Fatal(err)
	}
	script.script(
		step(parityCommunicate("comm-7", "Starting on the goal.", true)),
		step(parityResponse(parityCall("goal-1", "update_goal", map[string]any{"status": "complete"}))),
		step(parityCommunicate("comm-8", "The goal is complete.", true)),
	)
	processInput("work toward the goal")
	// GOAL_ENDED precedes the input's SESSION_END, which processInput awaited.
	if !ps.sawKind(events.EventGoalEnded) {
		t.Fatal("the goal did not end")
	}

	// 8. Delegate attention: a delegate reports back after the parent's input
	// ends, and a notification input answers it.
	script.script(
		step(parityResponse(parityCall("delegate-1", "delegate", map[string]any{"prompt": parityChildTask}))),
		step(parityCommunicate("comm-9", "Waiting on the delegate.", true)),
		step(parityCommunicate("comm-10", "The delegate reported back.", true)),
	)
	processInput("delegate a task")
	// The session notifies for more than attention. Only a notify that finds
	// the delegate's report recorded starts the notification input.
	for drained := false; !drained; {
		select {
		case <-notified:
		default:
			drained = true
		}
	}
	close(script.childRelease)
	for !hasDelegateAttention(transcriptTurns(t, sess.TranscriptPath())) {
		select {
		case <-notified:
		case <-time.After(20 * time.Second): // a tripwire; the notify is the signal
			t.Fatal("the delegate's report never reached the transcript")
		}
	}
	if _, err := sess.ProcessInputKind(ctx, "", nil, agent.EntryNotification); err != nil {
		t.Fatal(err)
	}
	endOfInput()
	if left := script.remaining(); left != 0 {
		t.Fatalf("%d scripted steps were never requested", left)
	}
	assertParity(t, "before restart", ps, parityBeforeRestart)

	// 9. Restart: resume the session from its transcript into a new server,
	// as a new daemon would, and run one more turn.
	ps.close(t)
	meta, err := schema.LoadSessionMeta(stateDir, sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	var header transcript.Header
	var entries []transcript.Entry
	resumed, err := agent.RestoreSessionFromMetaWithConfig(client, provider.NewOpenAIProfile(meta.Model), execenv.NewLocalExecutionEnvironment(workDir), meta, agent.RestoreSessionConfig{
		StateDir:       stateDir,
		LLMRetryPolicy: retry,
		LLMSleep:       noSleep,
		OnRestoredTranscript: func(h transcript.Header, e []transcript.Entry, _ bool) {
			header, entries = h, e
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed.SetClientMutationStartWakeFunc(func() {})
	prepared, err = PrepareAppIdentityFromEntriesForPath("local", resumed.ID(), "local:"+resumed.ID(), resumed.TranscriptPath(), header, entries)
	if err != nil {
		t.Fatal(err)
	}
	restarted := bridgeParitySession(t, resumed, prepared, stateDir)
	t.Cleanup(func() { restarted.close(t) })
	script.script(step(parityCommunicate("comm-11", "Back after the restart.", true)))
	if _, err := resumed.ProcessInput(ctx, "after the restart", nil); err != nil {
		t.Fatal(err)
	}
	restarted.await(t, events.EventSessionEnd, nil)
	assertParity(t, "after restart", restarted, parityAfterRestart)

	assertParityScenarioCoverage(t, transcriptTurns(t, resumed.TranscriptPath()))
}

func hasDelegateAttention(turns []schema.Turn) bool {
	for _, turn := range turns {
		if turn.Kind == schema.TurnSteering && turn.AttentionID != "" {
			return true
		}
	}
	return false
}

// assertParityScenarioCoverage fails when the scenario stops producing one of
// the entry shapes the harness exists to compare.
func assertParityScenarioCoverage(t *testing.T, turns []schema.Turn) {
	t.Helper()
	covered := map[string]bool{}
	for _, turn := range turns {
		covered[string(turn.Kind)] = true
		switch {
		case turn.Kind == schema.TurnSteering && turn.GoalContinuation != nil:
			covered["goal continuation"] = true
		case turn.Kind == schema.TurnSteering && turn.AttentionID != "":
			covered["delegate attention"] = true
		case turn.Kind == schema.TurnSteering && turn.ClientMutationID != "":
			covered["client steering"] = true
		}
		for _, part := range turn.Message.Content {
			if part.Kind == llm.ContentThinking {
				covered["reasoning"] = true
			}
			if part.ToolResult != nil && len(part.ToolResult.ImageData) > 0 {
				covered["image result"] = true
			}
		}
	}
	for _, want := range []string{
		string(schema.TurnUserInput), string(schema.TurnAssistant), string(schema.TurnToolResults),
		string(schema.TurnHookCompleted), string(schema.TurnModelSwitch), string(schema.TurnCheckpoint),
		string(schema.TurnSummary), string(schema.TurnFailure), string(schema.TurnAttentionResolution),
		"goal continuation", "delegate attention", "client steering", "reasoning", "image result",
	} {
		if !covered[want] {
			t.Errorf("the parity scenario no longer records %s", want)
		}
	}
}

// Reasons the rows below cite. Every row today is removed in phase 3, when
// one projector turns recorded entries into both the live history and the
// file projection; phase 2 only writes the fields phase 3 projects from.
const (
	whyIdentity = "live ids, positions, keys and turn ids come from the live projector's counters and the snapshot's allocation; the file's from entry indexes and grouping. Phase 3 projects live history from recorded entries with persisted TurnIDs and entry-ordinal keys."
	whyGrouping = "live keeps Stop-hook and compaction notices, and the notification answer, in the running turn; the file makes each standalone entry its own turn and hangs attention steering on its owner. Phase 3 groups by persisted TurnID."
	whyTurnID   = "live turn ids and file turn ids are minted differently for daemon turns. Phase 3 persists TurnID on every entry."
	whyTiming   = "live stamps timings from events, the file from entries, and not for the same items. Phase 2 persists turn and tool timing; phase 3 projects it."
	whyNotices  = "ephemeral live notices (prompt and plugin loads, round timings, compaction) have no entry. Phase 3 moves them to the overlay, out of history."
	whyGoalEnd  = "goal_ended is a live-only notice today. Phase 2 records it as a presentational entry; phase 3 projects it."
	whyFileOnly = "the file projects entries live never announces: TURN_FAILURE, delegate attention delivered as STEERING, and reasoning from a non-streamed response (live builds reasoning only from summary deltas). Phase 3 announces history from recorded entries."
)

// parityRows expands one reason over several fields of a subject.
func parityRows(why, class, subject string, fields ...string) []knownDivergence {
	if len(fields) == 0 {
		fields = []string{""}
	}
	rows := make([]knownDivergence, 0, len(fields))
	for _, field := range fields {
		rows = append(rows, knownDivergence{Class: class, Subject: subject, Field: field, Phase: 3, Why: why})
	}
	return rows
}

func parityTable(groups ...[]knownDivergence) []knownDivergence {
	var table []knownDivergence
	for _, group := range groups {
		table = append(table, group...)
	}
	return table
}

// parityBeforeRestart lists today's divergences between the live view and the
// transcript projection of one session, each with the phase that removes it.
var parityBeforeRestart = parityTable(
	parityRows(whyIdentity, "item-field", "agentMessage", "id", "position", "transcriptKey", "turnId"),
	parityRows(whyIdentity, "item-field", "commandExecution", "id", "position", "transcriptKey", "turnId"),
	parityRows(whyIdentity, "item-field", "steering", "id", "position", "transcriptKey", "turnId"),
	parityRows(whyIdentity, "item-field", "systemMessage", "id", "position", "transcriptEntryIndex", "transcriptKey"),
	parityRows(whyIdentity, "item-field", "systemMessage/compaction", "id", "position", "transcriptEntryIndex", "transcriptKey", "turnId"),
	parityRows(whyIdentity, "item-field", "systemMessage/environment", "id", "position", "transcriptEntryIndex", "transcriptKey"),
	parityRows(whyIdentity, "item-field", "systemMessage/hook_completed", "id", "position", "transcriptEntryIndex", "transcriptKey", "turnId"),
	parityRows(whyIdentity, "item-field", "systemMessage/model_switch", "id", "position", "transcriptEntryIndex", "transcriptKey", "turnId"),
	parityRows(whyIdentity, "item-field", "userMessage", "id", "position", "transcriptKey", "turnId"),
	parityRows(whyTiming, "item-field", "commandExecution", "durationMs"),
	parityRows(whyTiming, "item-field", "steering", "startedAt"),
	parityRows(whyTiming, "turn-field", "systemMessage/model_switch", "startedAt"),
	parityRows(whyTurnID, "turn-field", "systemMessage/model_switch", "id"),
	parityRows(whyTurnID, "turn-field", "userMessage", "id"),
	parityRows(whyGrouping, "turn-split", "live turn spans file turns"),
	parityRows(whyFileOnly, "file-only-item", "reasoning"),
	parityRows(whyFileOnly, "file-only-item", "steering"),
	parityRows(whyFileOnly, "file-only-item", "systemMessage/error"),
	parityRows(whyNotices, "live-only-item", "systemMessage/context_compaction"),
	parityRows(whyNotices, "live-only-item", "systemMessage/plugin_loaded"),
	parityRows(whyNotices, "live-only-item", "systemMessage/prompt_loaded"),
	parityRows(whyNotices, "live-only-item", "systemMessage/round_timings"),
	parityRows(whyGoalEnd, "live-only-item", "systemMessage/goal_ended"),
)

// parityAfterRestart lists them for a daemon restarted over the transcript:
// the restarted server seeds its snapshot from the file, so only the new
// turn's live identity and the restart's own notices diverge.
var parityAfterRestart = parityTable(
	parityRows(whyIdentity, "item-field", "agentMessage", "id", "position", "transcriptKey", "turnId"),
	parityRows(whyIdentity, "item-field", "systemMessage/hook_completed", "id", "position", "transcriptEntryIndex", "transcriptKey", "turnId"),
	parityRows(whyIdentity, "item-field", "userMessage", "id", "position", "transcriptKey", "turnId"),
	parityRows(whyGrouping, "turn-split", "live turn spans file turns"),
	parityRows(whyNotices, "live-only-item", "systemMessage/plugin_loaded"),
	parityRows(whyNotices, "live-only-item", "systemMessage/prompt_loaded"),
	parityRows(whyNotices, "live-only-item", "systemMessage/round_timings"),
)

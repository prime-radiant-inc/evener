package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
)

// Only the frontier and session metadata are relevant to these journeys. They
// come from the real session, including after its running execution is cleared.
// No test goroutine mutates the embedded source while the bridge samples it.
type rootFrontierSource struct {
	stubThreadEnvelopeSource
	sess *agent.Session
}

func (s *rootFrontierSource) LatestExecutionTurnID() string         { return s.sess.LatestExecutionTurnID() }
func (s *rootFrontierSource) SessionMeta() schema.SessionMeta       { return s.sess.Meta() }
func (s *rootFrontierSource) TaskAggregate() *appwire.TaskAggregate { return nil }

type rootFrontierHarness struct {
	sess    *agent.Session
	srv     *Server
	conn    *appserver.Connection
	ctx     context.Context
	cancel  context.CancelFunc
	drained chan struct{}
	changed chan struct{}

	mu        sync.Mutex
	holdFirst bool
	held      []events.SessionEvent
	routed    []events.SessionEvent
	started   []string
}

func newRootFrontierHarness(t *testing.T, sess *agent.Session, stateDir string) *rootFrontierHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // Tripwire only.
	h := &rootFrontierHarness{
		sess: sess, srv: NewServer(ServerConfig{StateDir: stateDir}),
		ctx: ctx, cancel: cancel, drained: make(chan struct{}), changed: make(chan struct{}), holdFirst: true,
	}
	wireTranscriptHistory(t, sess, h.srv)
	// A delayed previous TurnEnded must not sample the NEXT execution's stamp
	// and accidentally mask a missing UserInput/SteeringInjected/Goal facet.
	// The public start callback runs before the next execution's first record.
	sess.SetExecutionStartedFunc(func(turnID string) {
		h.mu.Lock()
		h.started = append(h.started, turnID)
		prior := len(h.started) - 1
		for {
			ended := 0
			for _, ev := range h.routed {
				if ev.Kind == events.EventTurnEnded {
					ended++
				}
			}
			if ended >= prior {
				break
			}
			changed := h.changed
			h.mu.Unlock()
			select {
			case <-changed:
			case <-ctx.Done():
				return
			}
			h.mu.Lock()
		}
		h.mu.Unlock()
		h.srv.SetProcessingTurn(turnID)
	})
	h.srv.SetThreadEnvelopeSource(&rootFrontierSource{sess: sess})
	h.srv.RefreshThreadEnvelope()
	h.conn = h.srv.AppServer().NewConnection("root-frontier")
	initialized := h.conn.HandleMessage(ctx, appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if initialized.Kind() != appwire.MessageResponse {
		t.Fatalf("initialize: %+v", initialized)
	}
	sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
		if ev.Kind == events.EventExecutionFirstRecorded {
			h.mu.Lock()
			if h.holdFirst {
				h.held = append(h.held, ev)
				h.wakeLocked()
				h.mu.Unlock()
				return
			}
			h.mu.Unlock()
		}
		h.bridge(ev)
	}, func() { close(h.drained) })
	t.Cleanup(func() {
		cancel()
		h.releaseFirstRecorded()
		sess.Close()
		select {
		case <-h.drained:
		case <-time.After(20 * time.Second): // Tripwire, the consumer's completion is the mechanism.
			t.Error("root event consumer did not finish")
		}
		h.srv.Close()
	})
	// SessionStart resamples every facet. Finish it before opening an execution
	// so it cannot accidentally stand in for the write-then-emit row under test.
	h.await(t, events.EventSessionStart, nil)
	return h
}

func (h *rootFrontierHarness) wakeLocked() {
	close(h.changed)
	h.changed = make(chan struct{})
}

func (h *rootFrontierHarness) bridge(ev events.SessionEvent) {
	BridgeEvent(h.srv, ev, nil)
	h.mu.Lock()
	h.routed = append(h.routed, ev)
	h.wakeLocked() // A waiter observes completion only AFTER BridgeEvent returned.
	h.mu.Unlock()
}

func (h *rootFrontierHarness) await(t *testing.T, kind events.EventKind, match func(events.SessionEvent) bool) events.SessionEvent {
	t.Helper()
	return h.awaitAfter(t, 0, kind, match)
}

func (h *rootFrontierHarness) awaitAfter(t *testing.T, after int, kind events.EventKind, match func(events.SessionEvent) bool) events.SessionEvent {
	t.Helper()
	for {
		h.mu.Lock()
		for _, ev := range h.routed[after:] {
			if ev.Kind == kind && (match == nil || match(ev)) {
				h.mu.Unlock()
				return ev
			}
		}
		changed := h.changed
		h.mu.Unlock()
		select {
		case <-changed:
		case <-h.ctx.Done():
			t.Fatalf("await bridged %s: %v", kind, h.ctx.Err())
		}
	}
}

func (h *rootFrontierHarness) awaitHeldFirst(t *testing.T, turnID string) {
	t.Helper()
	for {
		h.mu.Lock()
		for _, ev := range h.held {
			if ev.Data.(events.ExecutionFirstRecordedData).TurnID == turnID {
				h.mu.Unlock()
				return
			}
		}
		changed := h.changed
		h.mu.Unlock()
		select {
		case <-changed:
		case <-h.ctx.Done():
			h.mu.Lock()
			var held []string
			for _, ev := range h.held {
				held = append(held, ev.Data.(events.ExecutionFirstRecordedData).TurnID)
			}
			started := append([]string(nil), h.started...)
			h.mu.Unlock()
			type record struct {
				Kind      schema.TurnKind
				Span      schema.TurnSpanKind
				ID        string
				Attention bool
			}
			var records []record
			for _, turn := range transcriptTurns(t, h.sess.TranscriptPath()) {
				records = append(records, record{turn.Kind, turn.TurnKind, turn.TurnID, turn.AttentionID != ""})
			}
			t.Fatalf("await withheld first record for %s: %v, held=%v, started=%v, runtime=%q, records=%+v", turnID, h.ctx.Err(), held, started, h.sess.LatestExecutionTurnID(), records)
		}
	}
}

func (h *rootFrontierHarness) releaseFirstRecorded() {
	h.mu.Lock()
	h.holdFirst = false
	held := h.held
	h.held = nil
	h.mu.Unlock()
	for _, ev := range held {
		h.bridge(ev)
	}
}

type rootFrontierGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newRootFrontierGate(t *testing.T) *rootFrontierGate {
	g := &rootFrontierGate{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(g.open)
	return g
}

func (g *rootFrontierGate) open() { g.once.Do(func() { close(g.release) }) }

func (g *rootFrontierGate) step(ctx context.Context, response llm.Response) func(llm.Request) (llm.Response, error) {
	return func(llm.Request) (llm.Response, error) {
		close(g.entered)
		select {
		case <-g.release:
			return response, nil
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}
}

func (g *rootFrontierGate) await(ctx context.Context, t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-ctx.Done():
		t.Fatalf("provider hold was not reached: %v", ctx.Err())
	}
}

type rootFrontierInput struct {
	done chan struct{}
	err  error
}

func (h *rootFrontierHarness) start(t *testing.T, input string, kind agent.EntryKind) *rootFrontierInput {
	t.Helper()
	run := &rootFrontierInput{done: make(chan struct{})}
	go func() {
		_, run.err = h.sess.ProcessInputKind(h.ctx, input, nil, kind)
		close(run.done)
	}()
	t.Cleanup(func() {
		h.cancel()
		select {
		case <-run.done:
		case <-time.After(20 * time.Second): // Tripwire, not pacing.
			t.Error("ProcessInput did not unwind")
		}
	})
	return run
}

func (run *rootFrontierInput) await(ctx context.Context, t *testing.T) {
	t.Helper()
	select {
	case <-run.done:
		if run.err != nil {
			t.Fatalf("ProcessInput: %v", run.err)
		}
	case <-ctx.Done():
		t.Fatalf("ProcessInput did not finish: %v", ctx.Err())
	}
}

// Marshal the actual routed response, not the internal handler's Go value:
// legacy methods intentionally strip some fields from their wire responses.
func rootFrontierWire[T any](t *testing.T, h *rootFrontierHarness, method string, params any) T {
	t.Helper()
	response := h.conn.HandleMessage(h.ctx, appwire.RequestMessage(appwire.NewIntID(2), method, params))
	if response.Kind() != appwire.MessageResponse {
		t.Fatalf("%s: %+v", method, response.Error)
	}
	data, err := json.Marshal(response.Response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *rootFrontierHarness) assertShapes(t *testing.T, want string, requireCursor bool) appwire.ThreadHistoryReadResponse {
	t.Helper()
	if want == "" {
		t.Fatal("execution has no identity")
	}
	metadata := rootFrontierWire[appwire.ThreadHistoryReadResponse](t, h, appwire.MethodThreadHistoryRead, appwire.ThreadReadParams{ThreadID: h.sess.ID()})
	if len(metadata.Thread.Turns) != 0 {
		t.Fatal("metadata read unexpectedly projected turns")
	}
	if metadata.LatestExecutionTurnID != want || metadata.Thread.Evener.LatestExecutionTurnID != want {
		t.Fatalf("metadata frontier = %q, row = %q, want recorded execution %q", metadata.LatestExecutionTurnID, metadata.Thread.Evener.LatestExecutionTurnID, want)
	}
	full := rootFrontierWire[appwire.ThreadHistoryReadResponse](t, h, appwire.MethodThreadHistoryRead, appwire.ThreadReadParams{ThreadID: h.sess.ID(), IncludeTurns: true, ItemLimit: appwire.TranscriptItemPageLimit})
	if full.LatestExecutionTurnID != want {
		t.Fatalf("full-history frontier = %q, want %q", full.LatestExecutionTurnID, want)
	}
	found := false
	for _, turn := range full.Thread.Turns {
		if turn.ID == want && len(turn.Items) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("full history contains no recorded items for execution %q", want)
	}
	// The list API requires a cursor or Before naming an existing item.
	// Backfill before the latest real item under the current snapshot.
	last := full.Thread.Turns[len(full.Thread.Turns)-1]
	position := last.Items[len(last.Items)-1].Position
	if position == nil {
		t.Fatal("recorded latest item has no position")
	}
	page := rootFrontierWire[appwire.ThreadHistoryTurnsListResponse](t, h, appwire.MethodThreadHistoryTurnsList, appwire.ThreadTurnsListParams{ThreadID: h.sess.ID(), Before: position, ItemLimit: 1})
	if page.LatestExecutionTurnID != want || (requireCursor && len(page.Data) == 0) {
		t.Fatalf("turns/list frontier = %q, data = %d turns, want %q", page.LatestExecutionTurnID, len(page.Data), want)
	}
	if requireCursor && page.NextCursor == "" {
		t.Fatal("scenario did not produce a cursor page")
	}
	if page.NextCursor != "" {
		older := rootFrontierWire[appwire.ThreadHistoryTurnsListResponse](t, h, appwire.MethodThreadHistoryTurnsList, appwire.ThreadTurnsListParams{ThreadID: h.sess.ID(), Cursor: page.NextCursor, ItemLimit: 1})
		if older.LatestExecutionTurnID != want || len(older.Data) == 0 {
			t.Fatalf("cursor frontier = %q, data = %d turns, want current-view execution %q", older.LatestExecutionTurnID, len(older.Data), want)
		}
	}
	if got := h.sess.LatestExecutionTurnID(); got != want {
		t.Fatalf("runtime frontier = %q, want %q", got, want)
	}
	return full
}

func TestRootFrontierUserInputAndBlockedSteeringCarrier(t *testing.T) {
	t.Parallel()
	sess, script, _, stateDir := newScriptedSession(t)
	h := newRootFrontierHarness(t, sess, stateDir)
	if err := sess.SetModel("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	h.await(t, events.EventModelChanged, nil)
	prelude := false
	for _, turn := range transcriptTurns(t, sess.TranscriptPath()) {
		if turn.Kind == schema.TurnModelSwitch && turn.TurnKind == schema.TurnSpanPrelude {
			prelude = true
		}
	}
	if !prelude || sess.LatestExecutionTurnID() != "" {
		t.Fatalf("real prelude present = %t, runtime execution frontier = %q", prelude, sess.LatestExecutionTurnID())
	}
	for _, includeTurns := range []bool{false, true} {
		read := rootFrontierWire[appwire.ThreadHistoryReadResponse](t, h, appwire.MethodThreadHistoryRead, appwire.ThreadReadParams{ThreadID: sess.ID(), IncludeTurns: includeTurns})
		if read.LatestExecutionTurnID != "" || read.Thread.Evener.LatestExecutionTurnID != "" {
			t.Fatalf("prelude-only read includeTurns=%t has execution frontier %q, row %q", includeTurns, read.LatestExecutionTurnID, read.Thread.Evener.LatestExecutionTurnID)
		}
	}
	first, carrier := newRootFrontierGate(t), newRootFrontierGate(t)
	script.script(
		first.step(h.ctx, parityCommunicate("frontier-first", "FIRST_RESULT", true)),
		carrier.step(h.ctx, parityCommunicate("frontier-carrier", "CARRIER_RESULT", true)),
	)
	run := h.start(t, "ROOT_FRONTIER_INPUT", agent.EntryUserInput)
	first.await(h.ctx, t)
	h.await(t, events.EventUserInput, nil)
	userTurnID := sess.LatestExecutionTurnID()
	h.awaitHeldFirst(t, userTurnID)
	// The first-record event has NOT reached BridgeEvent. Only the real
	// write-then-emit UserInput row can have moved the materialized frontier.
	h.assertShapes(t, userTurnID, false)

	_, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "frontier-queue", ExpectedInstanceID: sess.ID(),
		Input: []appwire.InputItem{{Type: "text", Text: "ROOT_FRONTIER_CARRIER"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	queue, _ := sess.ClientMutationProjection()
	drain, err := sess.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{
		ClientMutationID: "frontier-drain", ExpectedInstanceID: sess.ID(), ExpectedQueueRevision: queue.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := drain.Receipt.TurnID
	if want == "" || want == userTurnID {
		t.Fatalf("drain reserved %q, original execution %q", want, userTurnID)
	}
	first.open()
	carrier.await(h.ctx, t)
	h.await(t, events.EventSteeringInjected, func(ev events.SessionEvent) bool {
		return ev.Data.(events.SteeringInjectedData).StableTurnID == want
	})
	h.awaitHeldFirst(t, want)
	h.assertShapes(t, want, true)
	assertRootFrontierOpening(t, sess, want, schema.TurnSteering)
	h.releaseFirstRecorded()
	carrier.open()
	run.await(h.ctx, t)
	h.await(t, events.EventSessionEnd, nil)
	h.assertShapes(t, want, true) // SessionEnd's facetAll must not clear the completed execution.
	if script.remaining() != 0 {
		t.Fatalf("%d scripted provider calls did not run", script.remaining())
	}
	t.Log("real UserInput and drain-as-steer opening rows agree with metadata, full history, turns/list, and cursor reads while providers are held and after completion")
}

func assertRootFrontierOpening(t *testing.T, sess *agent.Session, want string, kind schema.TurnKind) {
	t.Helper()
	var entries []schema.Turn
	for _, turn := range transcriptTurns(t, sess.TranscriptPath()) {
		if turn.TurnID == want {
			entries = append(entries, turn)
		}
	}
	if len(entries) != 1 || entries[0].Kind != kind || entries[0].TurnKind != schema.TurnSpanExecution {
		t.Fatalf("held execution %q entries = %+v, want only its %s execution-opening record", want, entries, kind)
	}
}

func TestRootFrontierCleanGoalContinuationWhileProviderHeld(t *testing.T) {
	t.Parallel()
	sess, script, workDir, stateDir := newScriptedSession(t)
	h := newRootFrontierHarness(t, sess, stateDir)
	if _, err := sess.SetGoal(h.ctx, "ROOT_FRONTIER_GOAL"); err != nil {
		t.Fatal(err)
	}
	continuation := newRootFrontierGate(t)
	script.script(
		step(parityResponse(parityCall("frontier-progress", "write_file", map[string]any{"file_path": "progress.txt", "content": "ROOT_FRONTIER_PROGRESS"}))),
		step(parityCommunicate("frontier-progress-report", "PROGRESS_RESULT", true)),
		continuation.step(h.ctx, parityResponse(parityCall("frontier-goal-complete", "update_goal", map[string]any{"status": "complete"}))),
		step(parityCommunicate("frontier-goal-report", "GOAL_RESULT", true)),
	)
	run := h.start(t, "ROOT_FRONTIER_BEGIN_GOAL", agent.EntryUserInput)
	continuation.await(h.ctx, t) // Third provider call, the clean continuation's first.
	h.await(t, events.EventUserInput, nil)
	h.mu.Lock()
	userTurnID := h.started[0]
	h.mu.Unlock()
	goal := h.await(t, events.EventGoalContinuation, nil).Data.(events.GoalContinuationData)
	if goal.StableTurnID == "" || goal.StableTurnID == userTurnID {
		t.Fatalf("goal continuation identity = %q, original = %q", goal.StableTurnID, userTurnID)
	}
	h.awaitHeldFirst(t, goal.StableTurnID)
	// No pending steering and no model response in this execution: the goal
	// marker's own frontier facet must work without the generic first-record row.
	h.assertShapes(t, goal.StableTurnID, true)
	assertRootFrontierOpening(t, sess, goal.StableTurnID, schema.TurnSteering)
	if content, err := os.ReadFile(filepath.Join(workDir, "progress.txt")); err != nil || string(content) != "ROOT_FRONTIER_PROGRESS" {
		t.Fatalf("real progress tool result = %q, error = %v", content, err)
	}
	h.mu.Lock()
	for _, ev := range h.routed {
		if ev.Kind == events.EventSteeringInjected {
			h.mu.Unlock()
			t.Fatal("goal scenario was not a clean continuation")
		}
	}
	h.mu.Unlock()
	h.releaseFirstRecorded()
	continuation.open()
	run.await(h.ctx, t)
	h.await(t, events.EventGoalEnded, nil)
	h.await(t, events.EventSessionEnd, nil)
	h.assertShapes(t, goal.StableTurnID, true)
	if script.remaining() != 0 {
		t.Fatalf("%d scripted provider calls did not run", script.remaining())
	}
	t.Log("real clean goal continuation agrees across every read shape during its first provider hold and after update_goal completion")
}

func TestRootFrontierAttentionWakeOpensOnAssistantRecord(t *testing.T) {
	t.Parallel()
	sess, script, _, stateDir := newScriptedSession(t)
	h := newRootFrontierHarness(t, sess, stateDir)
	t.Cleanup(func() { close(script.childRelease) })
	sess.SetDescendantEventFunc(func(ev events.SessionEvent) { h.srv.RecordDescendantAppEvent(sess.ID(), ev) })
	attention, followup := newRootFrontierGate(t), newRootFrontierGate(t)
	script.script(
		step(parityResponse(parityCall("frontier-child", "delegate", map[string]any{"prompt": parityChildTask}))),
		step(parityCommunicate("frontier-child-started", "CHILD_STARTED", true)),
		attention.step(h.ctx, parityCommunicate("frontier-attention-progress", "ATTENTION_PROGRESS", false)),
		followup.step(h.ctx, parityCommunicate("frontier-attention-done", "ATTENTION_DONE", true)),
	)
	initial := h.start(t, "ROOT_FRONTIER_CHILD", agent.EntryUserInput)
	initial.await(h.ctx, t)
	h.await(t, events.EventSessionEnd, nil)
	previous := sess.LatestExecutionTurnID()
	h.awaitHeldFirst(t, previous)
	h.releaseFirstRecorded()
	h.mu.Lock()
	h.holdFirst = true
	h.mu.Unlock()
	// Install after the initial input ends, while the actual child is held.
	// Its delivery's notify comes AFTER the attention rail has armed the wake.
	notified := make(chan struct{}, 1)
	sess.SetNotifyFunc(func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	})

	select {
	case script.childRelease <- struct{}{}:
	case <-h.ctx.Done():
		t.Fatalf("real child did not reach its provider hold: %v", h.ctx.Err())
	}
	awaitRootFrontierAttention(t, h, notified, 1)
	// This child report arrived while idle and made a newer delivery turn. It is
	// visible content, but it is not the newest execution.
	delivery := false
	for _, turn := range transcriptTurns(t, sess.TranscriptPath()) {
		if turn.AttentionID != "" && turn.TurnKind == schema.TurnSpanDelivery && turn.TurnID != previous {
			delivery = true
		}
	}
	if !delivery {
		t.Fatal("idle child delivery did not record an attention delivery turn")
	}
	h.assertShapes(t, previous, true)
	// A session-owned idle write uses a gap instead of an asynchronous delivery.
	// Both newer non-execution spans leave the frontier on the completed turn.
	if err := sess.SetModel("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	h.await(t, events.EventModelChanged, nil)
	gap := false
	for _, turn := range transcriptTurns(t, sess.TranscriptPath()) {
		if turn.Kind == schema.TurnModelSwitch && turn.TurnKind == schema.TurnSpanGap && turn.TurnID != previous {
			gap = true
		}
	}
	if !gap {
		t.Fatal("idle model switch did not record a gap")
	}
	h.assertShapes(t, previous, true)

	run := h.start(t, "", agent.EntryNotification)
	attention.await(h.ctx, t)
	h.mu.Lock()
	want := h.started[len(h.started)-1]
	h.mu.Unlock()
	if want == previous {
		t.Fatal("attention-only notification did not open a new execution")
	}
	h.await(t, events.EventTurnStarted, func(ev events.SessionEvent) bool {
		return ev.Data.(events.TurnStartedData).TurnID == want
	})
	for _, turn := range transcriptTurns(t, sess.TranscriptPath()) {
		if turn.TurnID == want {
			t.Fatalf("attention-only wake recorded an entry before its provider response: %+v", turn)
		}
	}
	h.assertShapes(t, previous, true) // An announced but unrecorded execution is not the frontier.

	h.mu.Lock()
	after := len(h.routed)
	h.mu.Unlock()
	attention.open()
	followup.await(h.ctx, t)
	h.awaitAfter(t, after, events.EventAssistantTextEnd, nil)
	h.awaitHeldFirst(t, want)
	var first *schema.Turn
	for _, turn := range transcriptTurns(t, sess.TranscriptPath()) {
		if turn.TurnID == want {
			first = &turn
			break
		}
	}
	if first == nil || first.Kind != schema.TurnAssistant || first.TurnKind != schema.TurnSpanExecution {
		t.Fatalf("attention wake's first recorded entry = %+v, want execution-opening assistant", first)
	}
	// The first-record backstop is still withheld. The assistant's own
	// post-persist mode row must move metadata while its next provider is held.
	h.assertShapes(t, want, true)
	h.releaseFirstRecorded()
	h.await(t, events.EventExecutionFirstRecorded, func(ev events.SessionEvent) bool {
		return ev.Data.(events.ExecutionFirstRecordedData).TurnID == want
	})
	h.assertShapes(t, want, true) // The typed backstop resamples the same recorded stamp idempotently.
	followup.open()
	run.await(h.ctx, t)
	h.awaitAfter(t, after, events.EventSessionEnd, nil)
	h.assertShapes(t, want, true)
	if script.remaining() != 0 {
		t.Fatalf("%d scripted provider calls did not run", script.remaining())
	}
	// Close and join the actual event stream before counting, so a duplicate
	// delayed first-record event cannot escape the assertion.
	sess.Close()
	select {
	case <-h.drained:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	h.mu.Lock()
	count := 0
	for _, ev := range h.routed {
		if ev.Kind == events.EventExecutionFirstRecorded && ev.Data.(events.ExecutionFirstRecordedData).TurnID == want {
			count++
		}
	}
	h.mu.Unlock()
	if count != 1 {
		t.Fatalf("first-record bridge deliveries for attention execution = %d, want one", count)
	}
	t.Log("real child attention opened a notification with no recorded entry, newer delivery and gap spans kept the previous frontier, and the first assistant persist moved every read shape before the typed backstop")
}

// This is event-driven condition checking, not polling: each retry awaits the
// production notify callback, which runs after attention persistence and arm.
func awaitRootFrontierAttention(t *testing.T, h *rootFrontierHarness, notified <-chan struct{}, count int) {
	t.Helper()
	for {
		select {
		case <-notified:
		case <-h.ctx.Done():
			t.Fatalf("await real child attention arm: %v", h.ctx.Err())
		}
		found := 0
		for _, turn := range transcriptTurns(t, h.sess.TranscriptPath()) {
			if turn.AttentionID != "" {
				found++
			}
		}
		if found >= count {
			return
		}
	}
}

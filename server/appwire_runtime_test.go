package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

func TestAppCapabilities_SteerGatedOnActiveTurn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		state      string
		processing bool
		reserved   bool
		stale      bool
		setSteer   bool
		wantSteer  bool
	}{
		{"processing with steerFunc", "active", true, false, false, true, true},
		{"reserved idle with steerFunc", "idle", false, true, false, true, true},
		{"stale projected active turn with steerFunc", "idle", false, false, true, true, false},
		{"idle with steerFunc", "idle", false, false, false, true, false},
		{"awaiting with steerFunc", "awaiting", false, false, false, true, false},
		{"closed with steerFunc", "closed", false, false, false, true, false},
		{"processing without steerFunc", "active", true, false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			if tc.setSteer {
				s.SetSteerFunc(func(string) {})
			}
			if tc.reserved {
				s.appActiveTurnID = "turn_reserved"
				s.appReservedTurnID = "turn_reserved"
			}
			if tc.stale {
				s.appActiveTurnID = "turn_stale"
			}
			got := s.appCapabilities(tc.state, tc.processing)
			if got.Steer != tc.wantSteer {
				t.Fatalf("Steer = %v, want %v", got.Steer, tc.wantSteer)
			}
		})
	}
}

// TestAppStatusAndCapabilitiesAreOneDecision is the invariant that makes this
// whole family of bugs unrepresentable rather than merely unlikely.
//
// The status a thread publishes and the capability set published beside it used
// to be separate expressions over overlapping state: appStatus read `state` and
// `processing` and never the turn reservation, appCapabilities read `processing`
// and the reservation and never `state`. Each therefore had a window the other
// could not see, and in both of them a client was handed one frame describing
// two different threads:
//
//   - state active with the daemon's flag already cleared: status=active beside
//     steer=false interrupt=false send=true -- a busy composer with nothing on
//     it, which is exactly kata 06t8's report;
//   - a turn reserved with the session still idle: status=idle beside
//     steer=true interrupt=true -- controls offered for a thread the wire calls
//     settled.
//
// appCapabilities now derives `active` and `closed` from appStatus's result, so
// there is one decision and the two cannot drift. This test is the guard on
// that, not on either value: it asserts only that they answer the same
// question the same way.
func TestAppStatusAndCapabilitiesAreOneDecision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		state      string
		processing bool
		reserved   string
	}{
		{"session state active, daemon flag cleared", "active", false, ""},
		{"turn reserved, session state still idle", "idle", false, "turn_m2"},
		{"processing", "active", true, ""},
		{"idle", "idle", false, ""},
		{"awaiting", "awaiting", false, ""},
		{"closed", "closed", false, ""},
		{"closed with a reservation left behind", "closed", false, "turn_m2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			s.SetSteerFunc(func(string) {})
			s.SetCancelFunc(func() {})
			s.appReservedTurnID = tc.reserved

			status := appStatus(tc.state, tc.processing, strings.TrimSpace(tc.reserved) != "")
			caps := s.appCapabilities(tc.state, tc.processing)
			working := status == appwire.ThreadStatusActive

			if caps.Interrupt != working {
				t.Fatalf("status=%q (working=%v) but interrupt=%v: one frame, two threads", status, working, caps.Interrupt)
			}
			if caps.Steer != working {
				t.Fatalf("status=%q (working=%v) but steer=%v: one frame, two threads", status, working, caps.Steer)
			}
			// Send is the complement, and closed removes it outright.
			wantSend := !working && status != appwire.ThreadStatusClosed
			if caps.Send != wantSend {
				t.Fatalf("status=%q (working=%v) but send=%v, want %v", status, working, caps.Send, wantSend)
			}
		})
	}
}

// TestAppCapabilities_StopIsOfferedWheneverSteerIs is the invariant behind kata
// 5gdv: the set this daemon publishes must never say "a turn is running, and it
// cannot be stopped".
//
// Steer and Interrupt were derived from different facts on different clocks.
// Steer comes from `active` -- the reservation plus the processing flag, which
// is also what the wire publishes as the thread's status. Interrupt came from
// the ambient cancelFunc, which the session loop arms and clears once per turn,
// and cmd/serf/serve.go's drain path (nextTurnCtx) published processing BEFORE
// arming it, where the other two arming sites do it the other way round. In
// between, this set said steer=true interrupt=false -- and a composer applying
// it draws Steer and Send with no Stop, which is the shape Jesse reported.
//
// Deriving both from `active` makes the disagreement unrepresentable rather
// than merely unlikely, which matters because the set is PUSHED: a client keeps
// it until the next status change, so a frame stamped inside that window takes
// Stop away for the whole turn that follows.
//
// The state below is the drain path's, in its own order.
func TestAppCapabilities_StopIsOfferedWheneverSteerIs(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	s.SetSteerFunc(func(string) {})
	s.SetCancelFunc(func() {})

	// End of a turn: the loop clears processing and the cancel together.
	s.SetProcessing(false)
	s.SetCancelFunc(nil)

	// Start of the next one, as nextTurnCtx ordered it: processing first, cancel
	// after. Everything between these two calls is the window.
	s.SetProcessing(true)

	got := s.appCapabilities(string(appwire.ThreadStatusActive), true)
	if got.Steer && !got.Interrupt {
		t.Fatal("the daemon published steer=true interrupt=false: it told a client a turn is running and cannot be stopped, and the client keeps that set until the status changes again")
	}
}

func TestAppDiagnosticsFromDetailedStatus_MCPStatusError(t *testing.T) {
	ds := DetailedStatus{
		MCP: []MCPServerInfo{{Name: "test-server", Tools: []string{"tool1"}, Status: "degraded", Error: "boom"}},
	}
	got := appDiagnosticsFromDetailedStatus(ds)
	if len(got.MCP) != 1 {
		t.Fatalf("MCP = %v, want 1", got.MCP)
	}
	m := got.MCP[0]
	if m.Name != "test-server" || len(m.Tools) != 1 || m.Tools[0] != "tool1" {
		t.Errorf("MCP[0] = %+v, want Name:test-server Tools:[tool1]", m)
	}
	if m.Status != "degraded" {
		t.Errorf("MCP[0].Status = %q, want degraded", m.Status)
	}
	if m.Error != "boom" {
		t.Errorf("MCP[0].Error = %q, want boom", m.Error)
	}
}

func TestAppDiagnosticsFromDetailedStatus_Exhaustion(t *testing.T) {
	resumable := true
	got := appDiagnosticsFromDetailedStatus(DetailedStatus{Jobs: []JobStatusInfo{{
		JobID:            "job_exhausted",
		JobType:          "delegate",
		Status:           "exhausted",
		Reason:           "tool_round_budget_exhausted",
		ExhaustionBudget: "max_tool_rounds_per_input",
		ExhaustionLimit:  1,
		Resumable:        &resumable,
	}}})
	if len(got.Jobs) != 1 {
		t.Fatalf("jobs = %+v, want one exhausted job", got.Jobs)
	}
	job := got.Jobs[0]
	if job.Status != "exhausted" || job.Reason != "tool_round_budget_exhausted" ||
		job.ExhaustionBudget != "max_tool_rounds_per_input" || job.ExhaustionLimit != 1 ||
		job.Resumable == nil || !*job.Resumable {
		t.Fatalf("job = %+v", job)
	}
}

func TestAppDiagnosticsFromDetailedStatus_DelegatesLossless(t *testing.T) {
	valid := true
	message := json.RawMessage("null")
	input := DelegateStatusInfo{
		DelegateID: "dlg_wire", OwnerSessionID: "root", RootSessionID: "root", ChildSessionID: "child", TranscriptRef: "local:child",
		ParentDelegateID: "dlg_parent", Type: "delegate", Lifecycle: "idle", Phase: "idle", Status: "idle", Outcome: "completed",
		Resumable: true, ProjectionRevision: 6, Task: "task", Message: message, StructuredResult: json.RawMessage("null"), StructuredValid: &valid,
		Warnings: []string{"warning"}, Diagnostics: []string{"diagnostic"}, Usage: &appwire.SerfUsage{InputTokens: 3, TotalTokens: 3},
		Worktree: &appwire.JobActivityWorktree{Path: "/tmp/lane", Branch: "delegate/lane"},
	}
	got := appDiagnosticsFromDetailedStatus(DetailedStatus{Delegates: []DelegateStatusInfo{input}, TurnSlots: &TurnSlotStatus{InUse: 2, Cap: 50, Jobs: 1, Drives: 1}})
	if len(got.Delegates) != 1 {
		t.Fatalf("delegates = %+v", got.Delegates)
	}
	delegate := got.Delegates[0]
	if delegate.DelegateID != input.DelegateID || delegate.ParentDelegateID != input.ParentDelegateID || delegate.ProjectionRevision != input.ProjectionRevision ||
		!bytes.Equal(delegate.Message, message) || delegate.StructuredValid == nil || !*delegate.StructuredValid || delegate.Usage == nil || delegate.Worktree == nil ||
		!reflect.DeepEqual(delegate.Warnings, input.Warnings) || !reflect.DeepEqual(delegate.Diagnostics, input.Diagnostics) {
		t.Fatalf("app delegate diagnostics = %+v", delegate)
	}
	if got.TurnSlots == nil || got.TurnSlots.InUse != 2 || got.TurnSlots.Cap != 50 || got.TurnSlots.Jobs != 1 || got.TurnSlots.Drives != 1 {
		t.Fatalf("app turn slots = %+v", got.TurnSlots)
	}
	raw, err := json.Marshal(delegate)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("waitIgnoredReason")) || bytes.Contains(raw, []byte("wait_ignored_reason")) {
		t.Fatalf("stable diagnostics leaked call-scoped wait result: %s", raw)
	}
}

func TestAppTurnsFromNotificationsAccumulatesReasoningDeltas(t *testing.T) {
	records := []appserver.SequencedNotification{
		{Notification: appwire.Notification{Method: "turn/started", Params: []byte(`{"turnId":"turn_1"}`)}},
		{Notification: appwire.Notification{Method: "item/started", Params: []byte(`{"turnId":"turn_1","item":{"type":"reasoning","id":"item_reasoning_1","turnId":"turn_1","status":"inProgress"}}`)}},
		{Notification: appwire.Notification{Method: "item/reasoning/summaryTextDelta", Params: []byte(`{"turnId":"turn_1","itemId":"item_reasoning_1","delta":"Let me think"}`)}},
		{Notification: appwire.Notification{Method: "item/reasoning/summaryTextDelta", Params: []byte(`{"turnId":"turn_1","itemId":"item_reasoning_1","delta":" about this."}`)}},
		{Notification: appwire.Notification{Method: "turn/completed", Params: []byte(`{"turnId":"turn_1","turn":{"status":"completed"}}`)}},
	}
	turns := appTurnsFromNotifications(records)
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	items := turns[0].Items
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d: %+v", len(items), items)
	}
	if items[0].Type != "reasoning" || items[0].Text != "Let me think about this." {
		t.Fatalf("reasoning item=%+v", items[0])
	}
}

// TestAppTurnsFromNotificationsCarriesTurnTiming verifies that replaying a
// turn/started carrying Turn.StartedAt and a turn/completed carrying
// Turn.CompletedAt/Turn.DurationMS reconstructs a Turn with those three
// timing fields set — today appTurnsFromNotifications only copies
// ItemsView/Status off the wire Turn and silently drops the timing fields.
func TestAppTurnsFromNotificationsCarriesTurnTiming(t *testing.T) {
	records := []appserver.SequencedNotification{
		{Notification: appwire.Notification{Method: "turn/started", Params: []byte(`{"turnId":"turn_1","turn":{"id":"turn_1","status":"inProgress","startedAt":1700000000}}`)}},
		{Notification: appwire.Notification{Method: "turn/completed", Params: []byte(`{"turnId":"turn_1","turn":{"id":"turn_1","status":"completed","completedAt":1700000042,"durationMs":4200}}`)}},
	}
	turns := appTurnsFromNotifications(records)
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	turn := turns[0]
	if turn.StartedAt == nil || *turn.StartedAt != 1700000000 {
		t.Fatalf("turn StartedAt=%v, want 1700000000", turn.StartedAt)
	}
	if turn.CompletedAt == nil || *turn.CompletedAt != 1700000042 {
		t.Fatalf("turn CompletedAt=%v, want 1700000042", turn.CompletedAt)
	}
	if turn.DurationMS == nil || *turn.DurationMS != 4200 {
		t.Fatalf("turn DurationMS=%v, want 4200", turn.DurationMS)
	}
}

func TestAppThread_OverlaysPendingAskFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "awaiting"})
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.askPending = true })
	thread := srv.appThread()
	if !thread.Serf.AskPending {
		t.Fatal("expected appThread().Serf.AskPending=true")
	}
}

// TestAppThread_CarriesReasoningInfoFromLiveSessionState verifies (Task 4e)
// that a cold-attached client's thread snapshot carries reasoningEffort,
// reasoningEffortLevels, and supportsReasoning from reasoningInfoFn with no
// prior thread/model/changed or thread/reasoning-effort/changed notification.
func TestAppThread_CarriesReasoningInfoFromLiveSessionState(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "idle"})
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) {
		e.reasoningEffort = "high"
		e.reasoningLevels = []string{"low", "medium", "high"}
		e.supportsReason = true
	})

	thread := srv.appThread()
	if thread.Serf.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want high", thread.Serf.ReasoningEffort)
	}
	if len(thread.Serf.ReasoningEffortLevels) != 3 {
		t.Fatalf("ReasoningEffortLevels = %v, want 3 levels", thread.Serf.ReasoningEffortLevels)
	}
	if !thread.Serf.SupportsReasoning {
		t.Fatal("SupportsReasoning = false, want true")
	}
}

func TestAppThread_UsesGeneratedSessionNameFromMeta(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{
		SessionID:  "01TITLE",
		State:      "idle",
		WorkingDir: "/tmp/project",
	})
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) {
		e.meta = schema.SessionMeta{
			ID:             "01TITLE",
			Name:           "Fix appwire titles",
			NameSource:     "prompt",
			OriginalPrompt: "please fix the appwire title plumbing because the sidebar uses this whole prompt",
		}
	})

	thread := srv.appThread()
	if thread.Name != "Fix appwire titles" {
		t.Fatalf("thread.Name = %q, want generated session name", thread.Name)
	}
	if thread.Preview == thread.SessionID || thread.Preview == "" {
		t.Fatalf("thread.Preview = %q, want human preview rather than session id", thread.Preview)
	}
}

// appTurnSnapshotRecord marshals one notification into a sequenced record for
// the reducer tests below.
func appTurnSnapshotRecord(t *testing.T, seq uint64, method string, params any) appserver.SequencedNotification {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s params: %v", method, err)
	}
	return appserver.SequencedNotification{
		Seq:          seq,
		Notification: appwire.Notification{Method: method, Params: raw},
	}
}

// TestAppTurnSnapshotReducesAssistantMessageReset covers item/agentMessage/reset,
// which a retried model call emits so its replacement output supersedes the
// partial already streamed. A reducer that ignores it leaves the abandoned
// partial in authoritative state, and the retry's text appends to it -- the
// user sees the first attempt's fragment welded onto the second attempt.
func TestAppTurnSnapshotReducesAssistantMessageReset(t *testing.T) {
	snapshot := &appTurnSnapshot{
		threadID: "th_1",
		turns: []appwire.Turn{{
			ID:        "turn_1",
			ItemsView: "full",
			Status:    appwire.TurnStatusInProgress,
			Items: []appwire.ThreadItem{{
				Type:   "agentMessage",
				ID:     "item_partial",
				TurnID: "turn_1",
				Text:   "abandoned partial",
				Status: appwire.TurnStatusInProgress,
			}},
		}},
		turnIndex: map[string]int{"turn_1": 0},
	}

	snapshot.Apply([]appserver.SequencedNotification{
		appTurnSnapshotRecord(t, 1, appwire.NotifyAgentMessageReset, appwire.AgentMessageResetParams{
			ThreadID: "th_1",
			TurnID:   "turn_1",
			ItemID:   "item_partial",
		}),
	})

	turns := snapshot.Snapshot()
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(turns))
	}
	for _, item := range turns[0].Items {
		if item.ID == "item_partial" {
			t.Fatalf("reset left the abandoned partial in authoritative state: %+v", turns[0].Items)
		}
	}
}

// TestAppTurnSnapshotReducesSteeringIntoActiveTurn covers serf/steering/injected.
// Steering is the only item the daemon adds to a turn it did not itself start,
// and it carries identity (client mutation ID) the pane needs to retire its
// optimistic copy. A reducer that drops it loses the user's own message from
// authoritative state.
//
// The item shape must match the frontend reducer exactly
// (cmd/serf-hub/frontend/src/protocol/reducer.ts:777-790), including the
// per-turn steering index in the ID, so a rejoin projects what the live pane
// already rendered.
func TestAppTurnSnapshotReducesSteeringIntoActiveTurn(t *testing.T) {
	snapshot := &appTurnSnapshot{threadID: "th_1"}

	snapshot.Apply([]appserver.SequencedNotification{
		appTurnSnapshotRecord(t, 1, appwire.NotifyTurnStarted, appwire.TurnStartedParams{
			ThreadID: "th_1",
			Turn:     appwire.Turn{ID: "turn_1", Status: appwire.TurnStatusInProgress},
		}),
		appTurnSnapshotRecord(t, 2, appwire.NotifySerfSteeringInjected, appwire.SerfSteeringInjectedParams{
			ThreadID:         "th_1",
			Text:             "first steer",
			Source:           "user",
			ClientMutationID: "mutation-a",
		}),
		appTurnSnapshotRecord(t, 3, appwire.NotifySerfSteeringInjected, appwire.SerfSteeringInjectedParams{
			ThreadID: "th_1",
			Text:     "second steer",
			Images:   []appwire.InputItem{{Type: "image", MediaType: "image/png", Data: []byte("steer-image"), Name: "steer.png"}},
			Kind:     "budget",
		}),
	})

	turns := snapshot.Snapshot()
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(turns))
	}
	var steering []appwire.ThreadItem
	for _, item := range turns[0].Items {
		if item.Type == "steering" {
			steering = append(steering, item)
		}
	}
	if len(steering) != 2 {
		t.Fatalf("steering items = %d, want 2 (items=%+v)", len(steering), turns[0].Items)
	}
	if steering[0].ID != "item_steering_live_turn_1_0" || steering[1].ID != "item_steering_live_turn_1_1" {
		t.Fatalf("steering ids = %q, %q, want item_steering_live_turn_1_0 and _1", steering[0].ID, steering[1].ID)
	}
	for i, item := range steering {
		if item.TurnID != "turn_1" {
			t.Fatalf("steering[%d] turn = %q, want turn_1", i, item.TurnID)
		}
		if item.Status != appwire.TurnStatusCompleted {
			t.Fatalf("steering[%d] status = %q, want completed", i, item.Status)
		}
	}
	if steering[0].Text != "first steer" || steering[0].Source != "user" || steering[0].ClientMutationID != "mutation-a" {
		t.Fatalf("first steering item = %+v, want text/source/clientMutationId preserved", steering[0])
	}
	if steering[1].Text != "second steer" || steering[1].SteeringKind != "budget" {
		t.Fatalf("second steering item = %+v, want text and steering kind preserved", steering[1])
	}
	if len(steering[1].Images) != 1 ||
		steering[1].Images[0].Name != "steer.png" ||
		string(steering[1].Images[0].Data) != "steer-image" {
		t.Fatalf("second steering images = %+v, want the injected image preserved", steering[1].Images)
	}
}

// TestAppTurnSnapshotSeedIsDeepDefensiveCopy proves Seed does not alias the
// caller's projection. Production seeds from a transcript projection the caller
// still holds; if any nested pointer or slice were shared, later work on that
// projection would silently rewrite authoritative state that clients have
// already read.
func TestAppTurnSnapshotSeedIsDeepDefensiveCopy(t *testing.T) {
	started := int64(1700000000)
	duration := int64(4200)
	exitCode := int64(0)
	cause := appwire.DiagnosticCause{Kind: "provider", Provider: "openai", Status: 500}
	seed := []appwire.Turn{{
		ID:        "turn_1",
		ItemsView: "full",
		Status:    appwire.TurnStatusCompleted,
		StartedAt: &started,
		Error:     &appwire.TurnError{Message: "original", Cause: &cause},
		Items: []appwire.ThreadItem{{
			Type: "agentMessage",
			ID:   "item_1",
			// DurationMS and ExitCode are pointers the transcript projector
			// populates on tool-call items, and they were the two fields the
			// clone helper missed.
			TurnID:     "turn_1",
			Text:       "original text",
			DurationMS: &duration,
			ExitCode:   &exitCode,
			Raw:        json.RawMessage(`{"k":"original"}`),
			Images: []appwire.InputItem{{
				Type:      "image",
				MediaType: "image/png",
				Data:      []byte("original-bytes"),
				Name:      "original.png",
				Metadata:  map[string]string{"source": "original"},
			}},
		}},
	}}

	snapshot := &appTurnSnapshot{threadID: "th_1"}
	snapshot.Seed(seed)
	before := snapshot.Snapshot()

	// Mutate every level the caller still owns.
	seed[0].ID = "mutated"
	seed[0].Status = appwire.TurnStatusInProgress
	*seed[0].StartedAt = 1
	seed[0].Error.Message = "mutated"
	seed[0].Error.Cause.Provider = "mutated"
	seed[0].Items[0].Text = "mutated text"
	seed[0].Items[0].Raw[6] = 'X'
	seed[0].Items[0].Images[0].Data[0] = 'X'
	seed[0].Items[0].Images[0].Metadata["source"] = "mutated"
	seed[0].Items[0].Images[0].Name = "mutated.png"
	*seed[0].Items[0].DurationMS = 9999
	*seed[0].Items[0].ExitCode = 137

	after := snapshot.Snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Seed aliased caller state:\nbefore=%+v\nafter=%+v", before, after)
	}

	// Two reads must not hand back the same pointers either, or one caller
	// mutating its copy would rewrite another's.
	a, b := snapshot.Snapshot(), snapshot.Snapshot()
	if a[0].Items[0].DurationMS == b[0].Items[0].DurationMS {
		t.Fatal("Snapshot shares one *DurationMS across reads")
	}
	if a[0].Items[0].ExitCode == b[0].Items[0].ExitCode {
		t.Fatal("Snapshot shares one *ExitCode across reads")
	}
}

// TestAppTurnSnapshotSteeringIndexIsPerTurn pins the per-turn steering counter.
// A global counter satisfies every single-turn assertion, so without a second
// turn nothing distinguishes the two -- and a global one would produce IDs that
// disagree with both the frontend reducer and the transcript reload shape.
func TestAppTurnSnapshotSteeringIndexIsPerTurn(t *testing.T) {
	snapshot := &appTurnSnapshot{threadID: "th_1"}
	snapshot.Apply([]appserver.SequencedNotification{
		appTurnSnapshotRecord(t, 1, appwire.NotifyTurnStarted, appwire.TurnStartedParams{
			ThreadID: "th_1", Turn: appwire.Turn{ID: "turn_1", Status: appwire.TurnStatusInProgress},
		}),
		appTurnSnapshotRecord(t, 2, appwire.NotifySerfSteeringInjected, appwire.SerfSteeringInjectedParams{
			ThreadID: "th_1", Text: "steer turn 1",
		}),
		appTurnSnapshotRecord(t, 3, appwire.NotifyTurnCompleted, appwire.TurnCompletedParams{
			ThreadID: "th_1", Turn: appwire.Turn{ID: "turn_1", Status: appwire.TurnStatusCompleted},
		}),
		appTurnSnapshotRecord(t, 4, appwire.NotifyTurnStarted, appwire.TurnStartedParams{
			ThreadID: "th_1", Turn: appwire.Turn{ID: "turn_2", Status: appwire.TurnStatusInProgress},
		}),
		appTurnSnapshotRecord(t, 5, appwire.NotifySerfSteeringInjected, appwire.SerfSteeringInjectedParams{
			ThreadID: "th_1", Text: "steer turn 2",
		}),
	})

	turns := snapshot.Snapshot()
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	if len(turns[0].Items) != 1 || turns[0].Items[0].ID != "item_steering_live_turn_1_0" {
		t.Fatalf("turn_1 items = %+v, want item_steering_live_turn_1_0", turns[0].Items)
	}
	// The second turn's first steer restarts at index 0. A counter shared
	// across turns would name this one _1.
	if len(turns[1].Items) != 1 || turns[1].Items[0].ID != "item_steering_live_turn_2_0" {
		t.Fatalf("turn_2 items = %+v, want item_steering_live_turn_2_0", turns[1].Items)
	}
}

// TestAppTurnSnapshotSeedReplacesPriorReducedState pins that a seed is a
// replacement, not a merge: whatever the snapshot had already reduced is gone,
// and activeTurnID is re-derived from the seed rather than left naming a turn
// no longer in the index (after which every steer would be silently dropped).
func TestAppTurnSnapshotSeedReplacesPriorReducedState(t *testing.T) {
	snapshot := &appTurnSnapshot{threadID: "th_1"}
	// Reduce state from a previous identity.
	for seq := uint64(1); seq <= 3; seq++ {
		snapshot.Apply([]appserver.SequencedNotification{
			appTurnSnapshotRecord(t, seq, appwire.NotifyTurnStarted, appwire.TurnStartedParams{
				ThreadID: "th_1",
				Turn:     appwire.Turn{ID: fmt.Sprintf("old_turn_%d", seq), Status: appwire.TurnStatusCompleted},
			}),
		})
	}

	snapshot.Seed([]appwire.Turn{{ID: "seeded_turn", Status: appwire.TurnStatusInProgress}})

	snapshot.Apply([]appserver.SequencedNotification{
		appTurnSnapshotRecord(t, 9, appwire.NotifySerfSteeringInjected, appwire.SerfSteeringInjectedParams{
			ThreadID: "th_1", Text: "after seed",
		}),
	})

	turns := snapshot.Snapshot()
	if len(turns) != 1 || turns[0].ID != "seeded_turn" {
		t.Fatalf("turns = %v, want only the seeded turn", turnIDs(turns))
	}
	if len(turns[0].Items) != 1 || turns[0].Items[0].ID != "item_steering_live_seeded_turn_0" {
		t.Fatalf("seeded turn items = %+v, want steering to still reach the seeded active turn", turns[0].Items)
	}
}

// TestAppTurnSnapshotSeedWithoutLiveTurnClearsSteeringTarget pins the other
// half of Seed's replacement contract: a seed that names no in-progress turn
// leaves no steering target at all. Turn ids are projector-local and restart
// at turn_1 for a new identity, so an activeTurnID carried across a seed can
// name a live id that now belongs to a completed turn from a different
// conversation, and steering would be welded onto it.
func TestAppTurnSnapshotSeedWithoutLiveTurnClearsSteeringTarget(t *testing.T) {
	snapshot := &appTurnSnapshot{threadID: "th_1"}
	snapshot.Apply([]appserver.SequencedNotification{
		appTurnSnapshotRecord(t, 1, appwire.NotifyTurnStarted, appwire.TurnStartedParams{
			ThreadID: "th_1",
			Turn:     appwire.Turn{ID: "turn_1", Status: appwire.TurnStatusInProgress},
		}),
	})

	snapshot.Seed([]appwire.Turn{{ID: "turn_1", Status: appwire.TurnStatusCompleted}})

	snapshot.Apply([]appserver.SequencedNotification{
		appTurnSnapshotRecord(t, 2, appwire.NotifySerfSteeringInjected, appwire.SerfSteeringInjectedParams{
			ThreadID: "th_1", Text: "steer nothing",
		}),
	})

	turns := snapshot.Snapshot()
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(turns))
	}
	if len(turns[0].Items) != 0 {
		t.Fatalf("seeded completed turn items = %+v, want steering dropped with no live turn", turns[0].Items)
	}
}

// TestAppTurnSnapshotSeedFindsLastInProgressTurn pins which turn steering
// attaches to after a seed. A transcript can hold an earlier turn that never
// completed because the daemon died mid-turn, so "the first in-progress turn"
// would aim steering at a turn that ended long ago.
func TestAppTurnSnapshotSeedFindsLastInProgressTurn(t *testing.T) {
	snapshot := &appTurnSnapshot{threadID: "th_1"}
	snapshot.Seed([]appwire.Turn{
		{ID: "turn_1", Status: appwire.TurnStatusInProgress},
		{ID: "turn_2", Status: appwire.TurnStatusCompleted},
		{ID: "turn_3", Status: appwire.TurnStatusInProgress},
	})

	snapshot.Apply([]appserver.SequencedNotification{
		appTurnSnapshotRecord(t, 1, appwire.NotifySerfSteeringInjected, appwire.SerfSteeringInjectedParams{
			ThreadID: "th_1", Text: "steer the live turn",
		}),
	})

	turns := snapshot.Snapshot()
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(turns))
	}
	if len(turns[0].Items) != 0 || len(turns[1].Items) != 0 {
		t.Fatalf("steering landed on a stale turn: %+v", turns)
	}
	if len(turns[2].Items) != 1 || turns[2].Items[0].ID != "item_steering_live_turn_3_0" {
		t.Fatalf("turn_3 items = %+v, want one steering item", turns[2].Items)
	}
}

// TestAppTurnSnapshotCompletedTurnClearsActiveSteeringTarget proves a settled
// turn stops absorbing steering. Without this, steering that races a turn's own
// completion would be welded onto the finished turn instead of being left for
// the next authoritative snapshot to place.
func TestAppTurnSnapshotCompletedTurnClearsActiveSteeringTarget(t *testing.T) {
	snapshot := &appTurnSnapshot{threadID: "th_1"}
	snapshot.Apply([]appserver.SequencedNotification{
		appTurnSnapshotRecord(t, 1, appwire.NotifyTurnStarted, appwire.TurnStartedParams{
			ThreadID: "th_1",
			Turn:     appwire.Turn{ID: "turn_1", Status: appwire.TurnStatusInProgress},
		}),
		appTurnSnapshotRecord(t, 2, appwire.NotifyTurnCompleted, appwire.TurnCompletedParams{
			ThreadID: "th_1",
			Turn:     appwire.Turn{ID: "turn_1", Status: appwire.TurnStatusCompleted},
		}),
		appTurnSnapshotRecord(t, 3, appwire.NotifySerfSteeringInjected, appwire.SerfSteeringInjectedParams{
			ThreadID: "th_1", Text: "steer after completion",
		}),
	})

	turns := snapshot.Snapshot()
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want 1 (no fabricated turn)", len(turns))
	}
	if len(turns[0].Items) != 0 {
		t.Fatalf("steering attached to a completed turn: %+v", turns[0].Items)
	}
}

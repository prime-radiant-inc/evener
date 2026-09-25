package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

func TestTaskPatchPreservesFullyCancelledOutcome(t *testing.T) {
	got := taskPatch(appwire.TaskUpdatedParams{Total: 3, Cancelled: 3, Remaining: 0})
	if got == nil || got.Total != 3 || got.Done != 0 || got.Cancelled != 3 || got.Remaining != 0 || got.Current != nil {
		t.Fatalf("taskPatch() = %+v, want fully cancelled task state", got)
	}
}

// Steer advertises harness support, not a turn in flight: the daemon accepts
// turn/drainAsSteer and turn/promoteQueuedAsSteer with nothing running (they
// release a queue a Stop parked), and clients apply the status themselves for
// turn/steer. Only a closed thread withholds it (#1363).
func TestAppCapabilities_SteerAdvertisesHarnessSupport(t *testing.T) {
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
		{"stale projected active turn with steerFunc", "idle", false, false, true, true, true},
		{"idle with steerFunc", "idle", false, false, false, true, true},
		{"awaiting with steerFunc", "awaiting", false, false, false, true, true},
		{"closed with steerFunc", "closed", false, false, false, true, false},
		{"processing without steerFunc", "active", true, false, false, false, false},
		{"idle without steerFunc", "idle", false, false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			if tc.setSteer {
				wireRetrySafeCapabilities(s)
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

// The capability has to describe what the handlers can actually do, and both
// turn/queue and turn/steer dispatch through the retry-safe callbacks. A server
// wired only through those - no legacy SetQueueFunc/SetSteerFunc - must
// therefore advertise the actions; reading the legacy pair alone advertised
// queue:false and steer:false for a harness that would have taken the request,
// so clients suppressed the action (#1375 review).
func TestAppCapabilities_AdvertisesTheRetrySafeRegistrations(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Queue: func(appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			return appwire.TurnQueueResponse{}, nil
		},
		Steer: func(appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			return appwire.TurnSteerResponse{}, nil
		},
	})
	if s.queueFunc != nil || s.steerFunc != nil || s.steerWithImagesFunc != nil {
		t.Fatal("precondition: no legacy callback is registered")
	}
	caps := s.appCapabilities(appwire.ThreadStatusIdle, false)
	if !caps.Queue {
		t.Fatalf("Queue = false while handleAppTurnQueue can dispatch: %+v", caps)
	}
	if !caps.Steer {
		t.Fatalf("Steer = false while handleAppTurnSteer can dispatch: %+v", caps)
	}
	// Closed still withholds both, as the daemon does.
	closed := s.appCapabilities(appwire.ThreadStatusClosed, false)
	if closed.Queue || closed.Steer {
		t.Fatalf("closed thread advertised queue/steer: %+v", closed)
	}
}

// The mirror of the case above, and the one the review flagged: a server wired
// only through the legacy SetSteerFunc/SetQueueFunc pair - which no handler
// reads - must NOT advertise the action. turn/steer and turn/queue dispatch
// through the retry-safe callbacks alone and would answer Unavailable, so
// advertising it offered a control that could only fail.
func TestAppCapabilities_IgnoresTheLegacySettersNoHandlerReads(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	s.SetSteerFunc(func(string) error { return nil })
	s.SetSteerWithImagesFunc(func(string, []ImageAttachment) error { return nil })
	s.SetQueueFunc(func(string) error { return nil })
	s.SetQueueWithImagesFunc(func(string, []ImageAttachment) error { return nil })
	s.SetCancelFunc(func() {})
	caps := s.appCapabilities(appwire.ThreadStatusIdle, false)
	if caps.Steer || caps.Queue || caps.Interrupt {
		t.Fatalf("legacy-only wiring advertised actions the RPC cannot serve: %+v", caps)
	}
}

// Interrupt must be advertised from the durable wiring a daemon has at startup,
// not from the per-turn cancel it arms later. A fresh idle daemon installs its
// authoritative turn/interrupt handler during setup, so its first thread/read
// has to say interrupt:true; deriving the bit from the armed cancel alone
// understated the harness until the first turn finished (#1375 review).
func TestAppCapabilities_InterruptWiredByTheStartupHandler(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Interrupt: func(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
			return appwire.TurnInterruptResponse{}, nil
		},
	})
	if s.cancelFunc != nil {
		t.Fatal("precondition: no cancel is armed before the first turn")
	}
	if caps := s.appCapabilities(appwire.ThreadStatusIdle, false); !caps.Interrupt {
		t.Fatalf("idle daemon with its turn/interrupt handler wired advertised interrupt=false: %+v", caps)
	}
	// Closed still withholds it, and a daemon without the handler still reports
	// it unsupported.
	if caps := s.appCapabilities(appwire.ThreadStatusClosed, false); caps.Interrupt {
		t.Fatalf("closed thread advertised interrupt: %+v", caps)
	}
	unwired := NewServer(ServerConfig{})
	unwired.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{})
	if caps := unwired.appCapabilities(appwire.ThreadStatusIdle, false); caps.Interrupt {
		t.Fatalf("daemon without a turn/interrupt handler advertised interrupt: %+v", caps)
	}
}

// Interrupt and Queue advertise harness support, exactly as Steer does: a
// wired interrupt handler or queue seam means "this harness can stop a turn"
// and "this harness can queue work", not "a turn is running right now". Clients
// apply the status themselves; only a closed thread withholds either. Folding
// `active` in left one struct carrying two semantics, so every client had to
// know which flags were pre-gated (#1375).
func TestAppCapabilities_InterruptAndQueueAdvertiseHarnessSupport(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		state          string
		processing     bool
		interruptWired bool
		queueWired     bool
		wantInterrupt  bool
		wantQueue      bool
	}{
		// interruptWired and queueWired vary independently: a swap of the two
		// seams (Interrupt reading the queue callback, Queue reading the
		// interrupt handler) has to fail here, not pass on a case where both are
		// true together.
		{"processing, both wired", "active", true, true, true, true, true},
		{"idle, both wired", "idle", false, true, true, true, true},
		{"awaiting, both wired", "awaiting", false, true, true, true, true},
		{"closed, both wired", "closed", false, true, true, false, false},
		{"processing, only interrupt wired", "active", true, true, false, true, false},
		{"processing, only queue wired", "active", true, false, true, false, true},
		{"idle, only interrupt wired", "idle", false, true, false, true, false},
		{"idle, only queue wired", "idle", false, false, true, false, true},
		{"processing, unwired", "active", true, false, false, false, false},
		{"idle, unwired", "idle", false, false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			// One registration, because SetRetrySafeTurnFunctions replaces the
			// whole set: two calls would leave only the second seam wired.
			functions := RetrySafeTurnFunctions{}
			if tc.interruptWired {
				functions.Interrupt = func(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
					return appwire.TurnInterruptResponse{}, nil
				}
			}
			if tc.queueWired {
				functions.Queue = func(appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
					return appwire.TurnQueueResponse{}, nil
				}
			}
			s.SetRetrySafeTurnFunctions(functions)
			got := s.appCapabilities(tc.state, tc.processing)
			if got.Interrupt != tc.wantInterrupt {
				t.Fatalf("Interrupt = %v, want %v (harness support, closed withholds)", got.Interrupt, tc.wantInterrupt)
			}
			if got.Queue != tc.wantQueue {
				t.Fatalf("Queue = %v, want %v (harness support, closed withholds)", got.Queue, tc.wantQueue)
			}
		})
	}
}

// An armed cancel is not evidence the RPC can interrupt: handleAppTurnInterrupt
// dispatches through the retry-safe handler alone and answers Unavailable when
// there is none, so a cancel-only harness must not advertise Stop (#1375
// review).
func TestAppCapabilities_ArmedCancelDoesNotAdvertiseInterrupt(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	s.SetCancelFunc(func() {})
	if s.cancelFunc == nil {
		t.Fatal("precondition: the cancel is armed")
	}
	if caps := s.appCapabilities(appwire.ThreadStatusIdle, false); caps.Interrupt {
		t.Fatalf("cancel-only harness advertised interrupt: %+v", caps)
	}
}

func TestAppCapabilities_AdvertisesClearWhenConfiguredAndSettled(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	s.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error { return nil })

	for _, tc := range []struct {
		name       string
		state      string
		processing bool
		wantClear  bool
	}{
		{name: "idle", state: appwire.ThreadStatusIdle, wantClear: true},
		{name: "active", state: appwire.ThreadStatusActive, processing: true},
		{name: "closed", state: appwire.ThreadStatusClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.appCapabilities(tc.state, tc.processing).Clear; got != tc.wantClear {
				t.Fatalf("Clear = %v, want %v", got, tc.wantClear)
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
// question the same way. What each flag folds in has since narrowed: Steer,
// Interrupt and Queue advertise harness support and fold in `closed` alone,
// while Send alone stays the complement of `active` (#1363, #1375).
func TestAppStatusAndCapabilitiesAreOneDecision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		state      string
		processing bool
		reserved   string
	}{
		{"session state active, daemon flag cleared", "active", false, ""},
		// The two reserved cases are NOT reachable in the shipped daemon:
		// reserveAppTurnIDForStart is the only writer of appReservedTurnID and
		// has no non-test callers. They are kept deliberately, and labelled so
		// nobody reads them as evidence of a live path -- appStatus must stay
		// correct for the reservation if it is ever wired up, and these are the
		// only cases that would catch it diverging from appCapabilities again.
		{"turn reserved, session state still idle (test-only state)", "idle", false, "turn_m2"},
		{"processing", "active", true, ""},
		{"idle", "idle", false, ""},
		{"awaiting", "awaiting", false, ""},
		{"closed", "closed", false, ""},
		{"closed with a reservation left behind (test-only state)", "closed", false, "turn_m2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			wireRetrySafeCapabilities(s)
			s.SetCancelFunc(func() {})
			s.appReservedTurnID = tc.reserved

			status := appStatus(tc.state, tc.processing, strings.TrimSpace(tc.reserved) != "")
			caps := s.appCapabilities(tc.state, tc.processing)
			working := status == appwire.ThreadStatusActive

			// Steer, Interrupt and Queue advertise harness support, withheld
			// only by closed: they do not follow `working`. The harness is
			// wired for all three (see
			// TestAppCapabilities_InterruptAndQueueAdvertiseHarnessSupport), so
			// the guard reads the same rule the comment claims.
			wantHarnessSupport := status != appwire.ThreadStatusClosed
			if caps.Steer != wantHarnessSupport {
				t.Fatalf("status=%q but steer=%v, want %v (harness support, closed withholds)", status, caps.Steer, wantHarnessSupport)
			}
			if caps.Interrupt != wantHarnessSupport {
				t.Fatalf("status=%q but interrupt=%v, want %v (harness support, closed withholds)", status, caps.Interrupt, wantHarnessSupport)
			}
			if caps.Queue != wantHarnessSupport {
				t.Fatalf("status=%q but queue=%v, want %v (harness support, closed withholds)", status, caps.Queue, wantHarnessSupport)
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
// Steer is harness support (steerAvailable && !closed; the client applies the
// status, #1363), so on its own it says nothing about a running turn. The
// forbidden shape is narrower: for an ACTIVE status, steer=true beside
// interrupt=false. Interrupt used to come from the ambient cancelFunc, which the
// session loop arms and clears once per turn, and cmd/evener/serve.go's drain
// path (nextTurnCtx) published processing BEFORE arming it, where the other two
// arming sites do it the other way round. In that window the set said steer=true
// interrupt=false for an active thread -- and a composer applying it draws Steer
// and Send with no Stop, which is the shape Jesse reported.
//
// Interrupt now advertises harness support (the retry-safe interrupt handler
// installed at startup, #1375), so on a harness that stops turns at all it is
// true for every status but closed and the disagreement is unrepresentable
// rather than merely unlikely. That matters because the set is PUSHED: a client
// keeps it until the next status change, so a frame stamped inside the window
// used to take Stop away for the whole turn that follows.
//
// The fixtures wire the retry-safe seams, which is what the capabilities read;
// wiring only the legacy setters would leave steer and interrupt both false and
// make the guard below vacuous.
//
// The state below is the drain path's, in its own order.
func TestAppCapabilities_StopIsOfferedWheneverSteerIs(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	wireRetrySafeCapabilities(s)
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

func TestAppDiagnosticsFromDetailedStatus_PreservesPluginPresence(t *testing.T) {
	empty := appDiagnosticsFromDetailedStatus(DetailedStatus{Plugins: []PluginStatusInfo{}})
	if empty.Plugins == nil {
		t.Fatal("explicit empty Plugins became nil")
	}
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty diagnostics: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode empty diagnostics: %v", err)
	}
	if got := string(wire["plugins"]); got != "[]" {
		t.Fatalf("serialized empty plugins = %s, want []", got)
	}

	legacy := appDiagnosticsFromDetailedStatus(DetailedStatus{})
	if legacy.Plugins != nil {
		t.Fatalf("nil Plugins became non-nil: %#v", legacy.Plugins)
	}
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy diagnostics: %v", err)
	}
	wire = nil
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode legacy diagnostics: %v", err)
	}
	if _, ok := wire["plugins"]; ok {
		t.Fatalf("nil plugins must remain absent: %s", raw)
	}
}

// TestAppDiagnosticsFromDetailedStatus_ProjectsWatches proves the structured
// watch rows reach the wire verbatim, including the cadence list, and that the
// projection copies the mutable event slice rather than aliasing agent state.
func TestAppDiagnosticsFromDetailedStatus_ProjectsWatches(t *testing.T) {
	ds := DetailedStatus{Watches: []agent.WatchStatusInfo{{
		ID:          "w1",
		Source:      "self",
		Target:      "caller",
		SendTo:      "caller",
		Note:        "wake me",
		Cadence:     []agent.WatchCadenceInfo{{Kind: "after", Seconds: 600}, {Kind: "events"}},
		OutputMatch: "",
		Events:      []string{"assistant.tool"},
		Deliveries:  3,
		DeliveryTimes: []string{
			"1970-01-01T00:16:40Z",
			"1970-01-01T00:16:41Z",
		},
		CreatedAt: "1970-01-01T00:16:40Z",
		Active:    true,
		EndReason: "",
	}}}
	got := appDiagnosticsFromDetailedStatus(ds)
	if len(got.Watches) != 1 {
		t.Fatalf("Watches = %+v, want 1 row", got.Watches)
	}
	w := got.Watches[0]
	if w.ID != "w1" || w.Source != "self" || w.Target != "caller" || w.SendTo != "caller" ||
		w.Note != "wake me" || w.Deliveries != 3 || w.CreatedAt != "1970-01-01T00:16:40Z" || !w.Active {
		t.Fatalf("watch row = %+v, want the projected fields", w)
	}
	wantCadence := []appwire.EvenerWatchCadence{{Kind: "after", Seconds: 600}, {Kind: "events"}}
	if !reflect.DeepEqual(w.Cadence, wantCadence) {
		t.Fatalf("Cadence = %+v, want %+v", w.Cadence, wantCadence)
	}
	if !reflect.DeepEqual(w.Events, []string{"assistant.tool"}) {
		t.Fatalf("Events = %+v, want [assistant.tool]", w.Events)
	}
	wantDeliveryTimes := []string{"1970-01-01T00:16:40Z", "1970-01-01T00:16:41Z"}
	if !reflect.DeepEqual(w.DeliveryTimes, wantDeliveryTimes) {
		t.Fatalf("DeliveryTimes = %+v, want %+v", w.DeliveryTimes, wantDeliveryTimes)
	}
	got.Watches[0].Events[0] = "mutated"
	if ds.Watches[0].Events[0] != "assistant.tool" {
		t.Fatal("projection aliased the agent event slice")
	}
	got.Watches[0].DeliveryTimes[0] = "mutated"
	if ds.Watches[0].DeliveryTimes[0] != "1970-01-01T00:16:40Z" {
		t.Fatal("projection aliased the agent delivery-time slice")
	}
}

// TestAppDiagnosticsFromDetailedStatus_ProjectsEventEveryAndFilter proves the
// events cadence's every-Nth count and filter summary survive the wire
// projection: without the carry a throttled or filtered event watch would be
// indistinguishable from one that fires on every matching event.
func TestAppDiagnosticsFromDetailedStatus_ProjectsEventEveryAndFilter(t *testing.T) {
	ds := DetailedStatus{Watches: []agent.WatchStatusInfo{{
		ID:        "w2",
		Source:    "self",
		Cadence:   []agent.WatchCadenceInfo{{Kind: "events", Every: 3, Filter: "tool_name=Bash, status=error"}},
		Events:    []string{"assistant.tool"},
		CreatedAt: "1970-01-01T00:16:40Z",
		Active:    true,
	}}}
	got := appDiagnosticsFromDetailedStatus(ds)
	if len(got.Watches) != 1 {
		t.Fatalf("Watches = %+v, want 1 row", got.Watches)
	}
	wantCadence := []appwire.EvenerWatchCadence{{Kind: "events", Every: 3, Filter: "tool_name=Bash, status=error"}}
	if !reflect.DeepEqual(got.Watches[0].Cadence, wantCadence) {
		t.Fatalf("Cadence = %+v, want %+v", got.Watches[0].Cadence, wantCadence)
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
		Resumable: true, NeedsAttention: true, ProjectionRevision: 6, Task: "task", Message: message, StructuredResult: json.RawMessage("null"), StructuredValid: &valid,
		Warnings: []string{"warning"}, Diagnostics: []string{"diagnostic"}, Usage: &appwire.EvenerUsage{InputTokens: 3, TotalTokens: 3},
		Worktree: &appwire.JobActivityWorktree{Path: "/tmp/lane", Branch: "delegate/lane"},
	}
	got := appDiagnosticsFromDetailedStatus(DetailedStatus{Delegates: []DelegateStatusInfo{input}, TurnSlots: &TurnSlotStatus{InUse: 2, Cap: 50, Jobs: 1, Drives: 1}})
	if len(got.Delegates) != 1 {
		t.Fatalf("delegates = %+v", got.Delegates)
	}
	delegate := got.Delegates[0]
	if delegate.DelegateID != input.DelegateID || delegate.ParentDelegateID != input.ParentDelegateID || !delegate.NeedsAttention || delegate.ProjectionRevision != input.ProjectionRevision ||
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

// A statusOnly thread/list is what the hub's liveness probe asks for every few
// seconds per daemon. Its root diagnostics carry only what a probe reads --
// jobs, watches, and each delegate's identity and lifecycle -- and none of the
// catalog or per-delegate payload (task text, final message, structured
// result) that make a full answer megabytes on a session with many delegates.
// A plain list is unchanged.
func TestThreadListStatusOnlyCarriesProbeDiagnosticsOnly(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	exitCode := 0
	srv.mu.Lock()
	srv.appEnvelope.Detailed = &DetailedStatus{
		Tools:  []ToolInfo{{Name: "read_file", Source: "builtin"}},
		Skills: []SkillInfo{{Name: "skill", Description: "a skill"}},
		Jobs:   []JobStatusInfo{{JobID: "job_shell", JobType: "shell", Status: "completed", ExitCode: &exitCode, Command: "make", Intent: "build"}},
		Delegates: []DelegateStatusInfo{{
			DelegateID: "dlg_1", OwnerSessionID: "root", RootSessionID: "root", ChildSessionID: "child-1",
			Lifecycle: "idle", Phase: "idle", Status: "idle", Task: "a long task", Description: "desc",
			Message: json.RawMessage(`"final report"`), StructuredResult: json.RawMessage(`{"k":"v"}`),
		}},
		Watches:   []agent.WatchStatusInfo{{ID: "watch-root", Source: "timer"}},
		TurnSlots: &TurnSlotStatus{InUse: 1, Cap: 50},
	}
	srv.mu.Unlock()

	full, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	if got := full.Data[0].Evener.Diagnostics; got == nil || len(got.Tools) != 1 || len(got.Delegates) != 1 || got.Delegates[0].Task != "a long task" {
		t.Fatalf("plain list root diagnostics = %+v, want the full answer", got)
	}

	slim, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{StatusOnly: true})
	if err != nil {
		t.Fatalf("statusOnly thread/list: %v", err)
	}
	want := &appwire.EvenerDiagnostics{
		Jobs:      []appwire.EvenerJobInfo{{JobID: "job_shell", JobType: "shell", Status: "completed", ExitCode: &exitCode, Command: "make", Intent: "build"}},
		Delegates: []appwire.EvenerDelegateInfo{{DelegateID: "dlg_1", ChildSessionID: "child-1", Lifecycle: "idle"}},
		Watches:   []appwire.EvenerWatchInfo{{ID: "watch-root", Source: "timer"}},
	}
	if got := slim.Data[0].Evener.Diagnostics; !reflect.DeepEqual(got, want) {
		t.Fatalf("statusOnly root diagnostics = %+v, want %+v", got, want)
	}
}

func TestAppTurnsFromNotificationsAccumulatesReasoningDeltas(t *testing.T) {
	records := []appserver.SequencedNotification{
		{Notification: appwire.Notification{Method: "turn/started", Params: []byte(`{"turn":{"id":"turn_1","status":"inProgress"}}`)}},
		{Notification: appwire.Notification{Method: "item/started", Params: []byte(`{"turnId":"turn_1","item":{"type":"reasoning","id":"item_reasoning_1","turnId":"turn_1","status":"inProgress"}}`)}},
		{Notification: appwire.Notification{Method: "item/reasoning/summaryTextDelta", Params: []byte(`{"turnId":"turn_1","itemId":"item_reasoning_1","delta":"Let me think"}`)}},
		{Notification: appwire.Notification{Method: "item/reasoning/summaryTextDelta", Params: []byte(`{"turnId":"turn_1","itemId":"item_reasoning_1","delta":" about this."}`)}},
		{Notification: appwire.Notification{Method: "turn/completed", Params: []byte(`{"turn":{"id":"turn_1","status":"completed"}}`)}},
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
	// The settle stamp names its turn the way the wire does, in turn.id, which
	// is the only name appTurnsFromNotifications reads: a frame naming it
	// anywhere else is dropped and leaves the turn open.
	if turns[0].Status != appwire.TurnStatusCompleted {
		t.Fatalf("turn status = %q, want %q", turns[0].Status, appwire.TurnStatusCompleted)
	}
}

// TestAppTurnsFromNotificationsCarriesTurnTiming verifies that replaying a
// turn/started carrying Turn.StartedAt and a turn/completed carrying
// Turn.CompletedAt/Turn.DurationMS reconstructs a Turn with those three
// timing fields set — today appTurnsFromNotifications only copies
// ItemsView/Status off the wire Turn and silently drops the timing fields.
func TestAppTurnsFromNotificationsCarriesTurnTiming(t *testing.T) {
	records := []appserver.SequencedNotification{
		{Notification: appwire.Notification{Method: "turn/started", Params: []byte(`{"turn":{"id":"turn_1","status":"inProgress","startedAt":1700000000}}`)}},
		{Notification: appwire.Notification{Method: "turn/completed", Params: []byte(`{"turn":{"id":"turn_1","status":"completed","completedAt":1700000042,"durationMs":4200}}`)}},
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
	if !thread.Evener.AskPending {
		t.Fatal("expected appThread().Evener.AskPending=true")
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
	if thread.Evener.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want high", thread.Evener.ReasoningEffort)
	}
	if len(thread.Evener.ReasoningEffortLevels) != 3 {
		t.Fatalf("ReasoningEffortLevels = %v, want 3 levels", thread.Evener.ReasoningEffortLevels)
	}
	if !thread.Evener.SupportsReasoning {
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

// TestAppTurnSnapshotReducesSteeringIntoActiveTurn covers evener/steering/injected.
// Steering is the only item the daemon adds to a turn it did not itself start,
// and it carries identity (client mutation ID) the pane needs to retire its
// optimistic copy. A reducer that drops it loses the user's own message from
// authoritative state.
//
// The item shape must match the frontend reducer exactly
// (appwire-client/typescript/reducer.ts:777-790), including the
// per-turn steering index in the ID, so a rejoin projects what the live pane
// already rendered.
func TestAppTurnSnapshotReducesSteeringIntoActiveTurn(t *testing.T) {
	snapshot := &appTurnSnapshot{threadID: "th_1"}

	snapshot.Apply([]appserver.SequencedNotification{
		appTurnSnapshotRecord(t, 1, appwire.NotifyTurnStarted, appwire.TurnStartedParams{
			ThreadID: "th_1",
			Turn:     appwire.Turn{ID: "turn_1", Status: appwire.TurnStatusInProgress},
		}),
		appTurnSnapshotRecord(t, 2, appwire.NotifyEvenerSteeringInjected, appwire.EvenerSteeringInjectedParams{
			ThreadID:         "th_1",
			Text:             "first steer",
			Source:           "user",
			ClientMutationID: "mutation-a",
		}),
		appTurnSnapshotRecord(t, 3, appwire.NotifyEvenerSteeringInjected, appwire.EvenerSteeringInjectedParams{
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
	position := appwire.ThreadItemPosition{Entry: 4, Item: 2}
	wantPosition := position
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
			TurnID:        "turn_1",
			Text:          "original text",
			DurationMS:    &duration,
			ExitCode:      &exitCode,
			Position:      &position,
			TranscriptKey: appitempaging.TranscriptItemKey("turn_1", position),
			Raw:           json.RawMessage(`{"k":"original"}`),
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
	*seed[0].Items[0].Position = appwire.ThreadItemPosition{Entry: 99, Item: 99}

	after := snapshot.Snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Seed aliased caller state:\nbefore=%+v\nafter=%+v", before, after)
	}
	if got := after[0].Items[0].Position; got == nil || *got != wantPosition {
		t.Fatalf("Seed position = %+v, want unchanged position %+v", got, wantPosition)
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
	if a[0].Items[0].Position == b[0].Items[0].Position {
		t.Fatal("Snapshot shares one *Position across reads")
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
		appTurnSnapshotRecord(t, 2, appwire.NotifyEvenerSteeringInjected, appwire.EvenerSteeringInjectedParams{
			ThreadID: "th_1", Text: "steer turn 1",
		}),
		appTurnSnapshotRecord(t, 3, appwire.NotifyTurnCompleted, appwire.TurnCompletedParams{
			ThreadID: "th_1", Turn: appwire.Turn{ID: "turn_1", Status: appwire.TurnStatusCompleted},
		}),
		appTurnSnapshotRecord(t, 4, appwire.NotifyTurnStarted, appwire.TurnStartedParams{
			ThreadID: "th_1", Turn: appwire.Turn{ID: "turn_2", Status: appwire.TurnStatusInProgress},
		}),
		appTurnSnapshotRecord(t, 5, appwire.NotifyEvenerSteeringInjected, appwire.EvenerSteeringInjectedParams{
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
		appTurnSnapshotRecord(t, 9, appwire.NotifyEvenerSteeringInjected, appwire.EvenerSteeringInjectedParams{
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
		appTurnSnapshotRecord(t, 2, appwire.NotifyEvenerSteeringInjected, appwire.EvenerSteeringInjectedParams{
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
		appTurnSnapshotRecord(t, 1, appwire.NotifyEvenerSteeringInjected, appwire.EvenerSteeringInjectedParams{
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
		appTurnSnapshotRecord(t, 3, appwire.NotifyEvenerSteeringInjected, appwire.EvenerSteeringInjectedParams{
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

func TestAppWireItemPagingSubscriptionCut(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_1", schema.NewTurn(schema.TurnUserInput, llm.User("prompt")))
	srv := st.srv
	httpServer := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
	t.Cleanup(httpServer.Close)
	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}

	var reads int
	restore := apptranscript.InstallReadObserverForTesting(func(apptranscript.ReadStats) { reads++ })
	t.Cleanup(restore)
	response, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, Subscribe: true, ItemLimit: 1})
	if err != nil {
		t.Fatalf("item read: %v", err)
	}
	if len(response.Thread.Turns) == 0 || len(response.Thread.Turns[0].Items) != 1 {
		t.Fatalf("item read response = %+v, want one positioned item fragment", response)
	}
	if !response.Thread.Evener.MutationStateAuthoritative {
		t.Fatal("daemon read must carry authoritative mutation state")
	}
	item := response.Thread.Turns[0].Items[0]
	if item.TranscriptKey == "" || item.Position == nil {
		t.Fatalf("item metadata = %+v, want transcript key and position", item)
	}
	if reads != 0 {
		t.Fatalf("subscribed item read went through the legacy transcript reader %d time(s); history comes from the transcript index", reads)
	}
}

func TestHandleAppThreadReadUnknownThreadRemainsEmpty(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "known")

	itemResponse, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{
		Subscribe:    false,
		Ref:          "local:missing",
		IncludeTurns: true,
		ItemLimit:    1,
	})
	if err != nil {
		t.Fatalf("item-mode thread/read: %v", err)
	}
	wantItemResponse := appwire.ThreadReadResponse{}
	if !reflect.DeepEqual(itemResponse, wantItemResponse) {
		t.Errorf("item-mode response = %+v, want otherwise empty response %+v", itemResponse, wantItemResponse)
	}
	if err := appwire.ValidateThreadReadItemResponse(itemResponse); err != nil {
		t.Errorf("ValidateThreadReadItemResponse: %v", err)
	}

	turnResponse, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{
		Subscribe:    false,
		Ref:          "local:missing",
		IncludeTurns: true,
	})
	if err != nil {
		t.Fatalf("item-only thread/read: %v", err)
	}
	if want := (appwire.ThreadReadResponse{}); !reflect.DeepEqual(turnResponse, want) {
		t.Fatalf("item-only response = %+v, want empty response %+v", turnResponse, want)
	}
}

func TestSubscribedThreadReadWithoutConnectionPreservesTheTarget(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_1", schema.NewTurn(schema.TurnUserInput, llm.User("root history")))
	srv := st.srv
	childPath := writeDelegateTranscript(t, "child", "child history")
	srv.SetDescendantTranscriptPathFunc(func(threadID string) string {
		if threadID == "child" {
			return childPath
		}
		return ""
	})
	srv.RecordDescendantAppEvent("th_1", threadEvent("child", events.SessionStartData{}))
	for ref, want := range map[string]string{"local:th_1": "root history", "local:child": "child history"} {
		response, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: ref, Subscribe: true, IncludeTurns: true})
		if err != nil {
			t.Fatalf("subscribed thread/read without connection (%s): %v", ref, err)
		}
		if texts := readTexts(response); len(texts) != 1 || texts[0] != want {
			t.Fatalf("subscribed no-connection response (%s) items = %q, want %q", ref, texts, want)
		}
	}
}

func TestHistoryUpdatedItemCarriesItsIdentity(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_identity")
	cursor := st.srv.appNotifier.CurrentSequence()
	st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("identity")))
	st.settle(t)

	for _, record := range st.srv.AppNotificationsAfter(cursor, "th_identity") {
		if record.Notification.Method != appwire.NotifyHistoryUpdated {
			continue
		}
		update := notificationParams[appwire.HistoryUpdatedParams](t, record)
		if len(update.Items) != 1 || update.Items[0].TranscriptKey == "" || update.Items[0].Position == nil || update.Items[0].Version == 0 {
			t.Fatalf("history/updated items = %+v, want one item with transcriptKey, position and version", update.Items)
		}
		return
	}
	t.Fatal("the recorded entry produced no history/updated notification")
}

func TestResumedThreadPublishesItsNextEntryWithTheReadsIdentity(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "resume", schema.NewTurn(schema.TurnUserInput, llm.User("historical")))
	cursor := st.srv.appNotifier.CurrentSequence()
	st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("live")))
	st.settle(t)

	var published appwire.ThreadItem
	for _, record := range st.srv.AppNotificationsAfter(cursor, "resume") {
		if record.Notification.Method != appwire.NotifyHistoryUpdated {
			continue
		}
		for _, item := range notificationParams[appwire.HistoryUpdatedParams](t, record).Items {
			if item.Text == "live" {
				published = item
			}
		}
	}
	if published.TranscriptKey == "" {
		t.Fatal("the resumed thread's next entry produced no history/updated item")
	}
	if published.TurnID != "turn_2" {
		t.Fatalf("published item turn = %q, want the entry's own turn_2", published.TurnID)
	}
	var read appwire.ThreadItem
	for _, turn := range st.read(t).Thread.Turns {
		for _, item := range turn.Items {
			switch item.Text {
			case "live":
				read = item
			case "historical":
				if item.TurnID != "turn_1" {
					t.Fatalf("the historical item moved to turn %q", item.TurnID)
				}
			}
		}
	}
	if !reflect.DeepEqual(read, published) {
		t.Fatalf("read item=%+v differs from the published item=%+v", read, published)
	}
}

func TestDescendantReadDoesNotClaimDurableMutationAuthority(t *testing.T) {
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	serveRootWithoutHistory(t, srv, "root")
	childPath := writeDelegateTranscript(t, "child", "child work")
	srv.SetDescendantTranscriptPathFunc(func(string) string { return childPath })
	srv.RecordDescendantAppEvent("root", events.SessionEvent{
		Kind: events.EventUserInput, SessionID: "child", Data: events.UserInputData{Text: "child work"},
	})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{
		Kind: events.EventSessionStart, SessionID: "child", Data: events.SessionStartData{},
	})
	started := false
	for _, notification := range srv.AppNotificationsAfter(0, "child") {
		if notification.Notification.Method != appwire.NotifyThreadStarted {
			continue
		}
		var params appwire.ThreadStartedParams
		if err := json.Unmarshal(notification.Notification.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params.Thread.Evener.ParentRef != "local:root" {
			t.Fatalf("started owner = %q", params.Thread.Evener.ParentRef)
		}
		started = true
	}
	if !started {
		t.Fatal("child start was not published")
	}
	peer := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
	defer peer.Close()
	transport, err := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(peer.URL, "http"), peer.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	client := appwire.NewClient(transport)
	client.Start(t.Context())
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	for _, subscribe := range []bool{false, true} {
		for _, includeTurns := range []bool{false, true} {
			response, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:child", IncludeTurns: includeTurns, Subscribe: subscribe, ItemLimit: 1})
			if err != nil {
				t.Fatal(err)
			}
			if response.Thread.Evener.ParentRef != "local:root" {
				t.Fatalf("descendant owner = %q", response.Thread.Evener.ParentRef)
			}
			if response.Thread.ID != "child" || response.Thread.Evener.MutationStateAuthoritative {
				t.Fatalf("descendant subscribe=%v turns=%v thread=%+v", subscribe, includeTurns, response.Thread)
			}
			if includeTurns && len(response.Thread.Turns) == 0 {
				t.Fatal("descendant transcript was lost")
			}
		}
	}
	root, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:root"})
	if err != nil {
		t.Fatal(err)
	}
	if !root.Thread.Evener.MutationStateAuthoritative {
		t.Fatal("root durable projection lost authority")
	}
}

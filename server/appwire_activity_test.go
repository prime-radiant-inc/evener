package server

import (
	"context"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func activityTestServer(t *testing.T, threadID string) (*Server, *activityTestClock) {
	t.Helper()
	clock := &activityTestClock{now: activityTestStart}
	srv := NewServer(ServerConfig{})
	srv.appActivity.now = clock.Now
	srv.SetAppIdentity("local", threadID)
	return srv, clock
}

func listedRootActivity(t *testing.T, srv *Server) (*appwire.ThreadActivity, []appwire.Thread) {
	t.Helper()
	list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{IncludeSubagents: true, StatusOnly: true})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	return list.Data[0].Evener.Activity, list.Data[1:]
}

// The root row's meter counts the root's own transcript items and tool output
// and every in-process descendant's (Jesse's ruling for S5: a coordinator's
// meter shows its whole tree). Descendant rows and thread/read carry none.
func TestThreadListRootRowCarriesTheWholeTreesActivity(t *testing.T) {
	srv, clock := activityTestServer(t, "root")
	// The root: a user message finishes (1), a tool streams two output
	// deltas (2) and finishes (1).
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "root", Data: events.UserInputData{Text: "run the tests"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "root", Data: events.ToolCallStartData{ToolName: "shell", CallID: "call-1"}})
	for range 2 {
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventToolCallOutputDelta, SessionID: "root", Data: events.ToolCallOutputDeltaData{ToolName: "shell", CallID: "call-1", Delta: "ok\n"}})
	}
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "root", Data: events.ToolCallEndData{ToolName: "shell", CallID: "call-1", Output: "ok"}})
	// A subagent's message finishes a minute later (1).
	clock.now = activityTestStart.Add(time.Minute)
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventUserInput, SessionID: "child-1", Data: events.UserInputData{Text: "fix the race"}})

	activity, descendants := listedRootActivity(t, srv)
	if activity == nil {
		t.Fatal("the root row carries no activity")
	}
	if want := []int{0, 0, 0, 0, 0, 4, 1}; !reflect.DeepEqual(activity.Minutes, want) {
		t.Fatalf("root minutes = %v, want %v", activity.Minutes, want)
	}
	if activity.LastActivityAt != clock.now.UnixMilli() {
		t.Fatalf("last activity = %d, want the subagent's %d", activity.LastActivityAt, clock.now.UnixMilli())
	}
	if len(descendants) == 0 {
		t.Fatal("the list carries no descendant row")
	}
	for _, row := range descendants {
		if row.Evener.Activity != nil {
			t.Fatalf("descendant row %s carries activity %+v", row.ID, row.Evener.Activity)
		}
	}
	if read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{}); read.Thread.Evener.Activity != nil {
		t.Fatalf("thread/read carries activity %+v; nothing announces its changes to a subscriber", read.Thread.Evener.Activity)
	}
}

// The row's intent is the root session's own words. The meter counts a
// descendant's motion toward the root's bars, but a subagent's tool call must
// never word the root's row, so only RecordAppEvent notes an intent.
func TestThreadListRootRowIntentIsTheRootsOwn(t *testing.T) {
	srv, _ := activityTestServer(t, "root")
	srv.RecordAppEvent(toolCallStart("Running the suite."))
	srv.RecordDescendantAppEvent("root", threadEvent("child-1", events.ToolCallStartData{
		ToolName: "read_file", ArgumentsJSON: "{}", Description: "Reading the child's notes.",
	}))

	activity, _ := listedRootActivity(t, srv)
	if activity == nil || activity.LatestIntent != "Running the suite." {
		t.Fatalf("root row activity = %+v, want the root's own words", activity)
	}
}

// A replaced identity is a different session: its meter starts empty, its
// quiet clock starts when it is installed, and a late event from the replaced
// tree counts for nothing.
func TestReplacedIdentityStartsAFreshMeter(t *testing.T) {
	srv, clock := activityTestServer(t, "old")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "old", Data: events.UserInputData{Text: "work"}})
	clock.now = activityTestStart.Add(30 * time.Second)
	srv.SetAppIdentity("local", "new")
	srv.RecordDescendantAppEvent("old", events.SessionEvent{Kind: events.EventUserInput, SessionID: "old-child", Data: events.UserInputData{Text: "late"}})

	activity, _ := listedRootActivity(t, srv)
	if want := []int{0, 0, 0, 0, 0, 0, 0}; activity == nil || !reflect.DeepEqual(activity.Minutes, want) {
		t.Fatalf("new identity's activity = %+v, want empty minutes", activity)
	}
	if activity.LastActivityAt != clock.now.UnixMilli() {
		t.Fatalf("new identity's quiet clock starts at %d, want its install time %d", activity.LastActivityAt, clock.now.UnixMilli())
	}
	if activity.LastMovedAt != 0 {
		t.Fatalf("new identity's moved time = %d, want none: the old session's motion is not its own", activity.LastMovedAt)
	}
}

// A session the daemon just began serving lists no moved time, so a client
// comparing it with a seen mark finds nothing new after a restart; its first
// transcript motion sets it.
func TestThreadListReportsNoMovedTimeUntilTheSessionMoves(t *testing.T) {
	srv, clock := activityTestServer(t, "th_fresh_serve")
	if activity, _ := listedRootActivity(t, srv); activity.LastMovedAt != 0 {
		t.Fatalf("moved at serve start = %d, want none", activity.LastMovedAt)
	}
	clock.now = clock.now.Add(time.Minute)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_fresh_serve", Data: events.AssistantTextDeltaData{Delta: "x"}})
	if activity, _ := listedRootActivity(t, srv); activity.LastMovedAt != clock.now.UnixMilli() {
		t.Fatalf("moved after motion = %d, want %d", activity.LastMovedAt, clock.now.UnixMilli())
	}
}

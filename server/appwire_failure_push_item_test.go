package server

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
)

// itemCompletedNotifications pulls every item/completed the server broadcast,
// decoded only far enough to read the failedToolCalls field this kata adds
// (kata 895d): item/completed's params ride as a map[string]any
// (internal/appprojector builds them that way, unlike thread/status/changed's
// typed struct), so this narrow shape is enough to check what rode along.
func itemCompletedNotifications(t *testing.T, srv *Server, threadID string) []struct {
	FailedToolCalls *int `json:"failedToolCalls"`
} {
	t.Helper()
	var out []struct {
		FailedToolCalls *int `json:"failedToolCalls"`
	}
	for _, n := range srv.AppNotificationsAfter(0, threadID) {
		if n.Notification.Method != appwire.NotifyItemCompleted {
			continue
		}
		var params struct {
			FailedToolCalls *int `json:"failedToolCalls"`
		}
		if err := json.Unmarshal(n.Notification.Params, &params); err != nil {
			t.Fatalf("decode item/completed params: %v", err)
		}
		out = append(out, params)
	}
	return out
}

// A long turn accumulates failures the watching client cannot see until the
// turn ends (thread/status/changed is the only push today) - kata 895d's live
// measurement (a real anthropic/claude-haiku-4-5 session run for this kata:
// a single turn with 3 shell failures spanned ~26s wall-clock, and a trivial
// 5-tool-call turn spanned ~8s) shows the lag is real, not theoretical. This
// asserts item/completed carries the count too, so it moves the instant a
// failure lands rather than waiting for the turn boundary - but ONLY on the
// item whose completion changed the running figure, so a turn with no
// failures adds no payload to any of its item/completed notifications (the
// volume objection the kata itself raised).
func TestItemCompletedCarriesTheRunningFailureCountWhenItChanges(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	publishFailureCount(srv, 0)

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "go"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "first"}})

	publishFailureCount(srv, 1)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "second"}})

	// Unchanged: must not resend the same figure on every later item.
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "third"}})

	items := itemCompletedNotifications(t, srv, "th_1")
	// EventUserInput itself emits one item/completed (the user message item)
	// ahead of the three assistant ones this test drives.
	if len(items) != 4 {
		t.Fatalf("got %d item/completed notifications, want 4", len(items))
	}
	if items[0].FailedToolCalls == nil || *items[0].FailedToolCalls != 0 {
		t.Fatalf("user-input item FailedToolCalls = %v, want a measured 0 (first observation)", items[0].FailedToolCalls)
	}
	if items[1].FailedToolCalls != nil {
		t.Fatalf("first assistant item FailedToolCalls = %v, want absent: still 0, unchanged since the last stamp", items[1].FailedToolCalls)
	}
	if items[2].FailedToolCalls == nil || *items[2].FailedToolCalls != 1 {
		t.Fatalf("second assistant item FailedToolCalls = %v, want 1 (the count just moved)", items[2].FailedToolCalls)
	}
	if items[3].FailedToolCalls != nil {
		t.Fatalf("third assistant item FailedToolCalls = %v, want absent: the count did not change since the last stamp", items[3].FailedToolCalls)
	}
}

func TestItemCompletedOmitsAnUnmeasuredFailureCount(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.failedToolCalls = 0; e.failuresMeasured = false })

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "go"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "first"}})

	for _, params := range itemCompletedNotifications(t, srv, "th_1") {
		if params.FailedToolCalls != nil {
			t.Fatalf("item/completed carried %d for an unmeasured session, want absent", *params.FailedToolCalls)
		}
	}
}

func TestItemCompletedOmitsTheCountOnADaemonThatNeverWiredIt(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "go"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "first"}})

	for _, params := range itemCompletedNotifications(t, srv, "th_1") {
		if params.FailedToolCalls != nil {
			t.Fatalf("item/completed carried %d with no callback wired, want absent", *params.FailedToolCalls)
		}
	}
}

// A fresh identity (a new session attached to this server) must start with no
// memory of a previous session's last-stamped count - otherwise a new
// session's first genuinely-zero measurement could be swallowed as "no
// change" if it happened to match whatever the last session left behind.
func TestItemCompletedLastStampResetsOnNewIdentity(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	publishFailureCount(srv, 3)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "go"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "first"}})

	srv.SetAppIdentity("local", "th_2")
	// The identity swap zeroed the envelope along with the identity it
	// described, so the replacement session publishes its own count -- which is
	// what serve.go's RefreshThreadEnvelope does immediately after
	// ReplaceAppIdentity. Same count as the outgoing session's last stamp:
	// must still stamp, since this is a different session's first observation.
	publishFailureCount(srv, 3)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_2", Data: events.UserInputData{Text: "go"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_2", Data: events.AssistantTextEndData{Text: "first"}})

	items := itemCompletedNotifications(t, srv, "th_2")
	// EventUserInput's own item/completed, plus the assistant one this test
	// drives.
	if len(items) != 2 {
		t.Fatalf("got %d item/completed notifications for th_2, want 2", len(items))
	}
	if items[0].FailedToolCalls == nil || *items[0].FailedToolCalls != 3 {
		t.Fatalf("th_2's first item FailedToolCalls = %v, want 3 (its own first observation, not suppressed by th_1's last stamp)", items[0].FailedToolCalls)
	}
	if items[1].FailedToolCalls != nil {
		t.Fatalf("th_2's second item FailedToolCalls = %v, want absent: unchanged since th_2's own first stamp", items[1].FailedToolCalls)
	}
}
